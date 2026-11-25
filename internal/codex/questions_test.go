package codex

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/monkeyWie/reignhub/internal/codexipc"
	"github.com/monkeyWie/reignhub/internal/config"
	"github.com/monkeyWie/reignhub/internal/control"
	"github.com/monkeyWie/reignhub/internal/events"
	"github.com/monkeyWie/reignhub/internal/provider"
	"github.com/monkeyWie/reignhub/internal/rpc"
)

func TestSharedBackendQuestionThroughDispatcher(t *testing.T) {
	for _, status := range []string{"inProgress", "completed"} {
		t.Run(status, func(t *testing.T) {
			p := New(config.Config{DataDir: t.TempDir()}, "", events.New())
			desktop := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				_ = json.NewEncoder(w).Encode(desktopState{Ready: true})
			}))
			t.Cleanup(desktop.Close)
			p.shared = &sharedConnection{
				sharedEndpoint: sharedEndpoint{Control: strings.TrimPrefix(desktop.URL, "https://")},
				http:           desktop.Client(),
			}
			var expired, reject, unavailable atomic.Bool
			var writes atomic.Int32
			backendFixture(t, p, func(m rpc.Message) (any, *rpc.Error) {
				params := decodeObject(m.Params)
				switch m.Method {
				case "thread/turns/list":
					if number := params["limit"]; number != float64(1) || params["itemsView"] != "full" {
						t.Errorf("unexpected approval read: %s", m.Params)
					}
					if unavailable.Load() {
						return nil, &rpc.Error{Code: -32000, Message: "fixture read failure"}
					}
					if expired.Load() || params["threadId"] != "session" {
						return rawJSON(map[string]any{"data": []any{map[string]any{"id": "next", "status": "inProgress", "items": []any{}}}}), nil
					}
					turn := object(array(questionState()["turns"])[0])
					turn["status"] = status
					return map[string]any{"data": []any{turn}}, nil
				case "turn/steer", "turn/start":
					writes.Add(1)
					wantSteer := status == "inProgress"
					if params["threadId"] != "session" || wantSteer != (m.Method == "turn/steer") ||
						wantSteer && params["expectedTurnId"] != "turn" {
						t.Errorf("answer routed to %s with %s", m.Method, m.Params)
					}
					text := stringValue(object(array(params["input"])[0])["text"])
					if !strings.HasPrefix(text, questionReplyOpen) || !strings.HasSuffix(text, questionReplyClose) {
						t.Errorf("missing structured reply envelope: %q", text)
					}
					var replies []asyncQuestionReply
					err := json.Unmarshal([]byte(strings.TrimSuffix(strings.TrimPrefix(text, questionReplyOpen), questionReplyClose)), &replies)
					want := []asyncQuestionReply{{`["request_user_input_async","call",0]`, "Pick a scope", "Local"}}
					if err != nil || !reflect.DeepEqual(replies, want) {
						t.Errorf("wrong answers: %v %v", replies, err)
					}
					if reject.Load() {
						return nil, &rpc.Error{Code: -32602, Message: "fixture rejection"}
					}
					return map[string]any{}, nil
				default:
					t.Errorf("unexpected method: %s", m.Method)
					return nil, &rpc.Error{Code: -32601, Message: "unexpected method"}
				}
			})
			dispatcher := control.Dispatcher{Providers: map[string]provider.Provider{"codex": p}}
			read := func(session string) []provider.Approval {
				t.Helper()
				out := dispatcher.Handle(t.Context(), control.Request{Method: "approvals", Provider: "codex", Session: session})
				var approvals []provider.Approval
				if err := json.Unmarshal(out.Result, &approvals); out.Error != "" || err != nil {
					t.Fatalf("approvals response %v: %v", out, err)
				}
				return approvals
			}
			approvals := read("session")
			if len(approvals) != 1 || len(approvals[0].Questions) != 2 || !approvals[0].AllowSkip ||
				len(approvals[0].Questions[0].Options) != 2 || approvals[0].Questions[0].Options[0].Label != "Local" {
				t.Fatalf("question card data missing: %+v", approvals)
			}
			a := approvals[0]
			if len(read("other")) != 0 {
				t.Fatal("question leaked into another session")
			}
			unavailable.Store(true)
			if pending := read("session"); len(pending) != 1 || pending[0].ID != a.ID {
				t.Fatal("failed refresh discarded the existing question")
			}
			unavailable.Store(false)
			decide := control.Request{Method: "decide", Provider: "codex", Session: "session",
				Approval: a.ID, Decision: questionAnswer(a.Questions[0].ID, "Local")}
			reject.Store(true)
			if out := dispatcher.Handle(t.Context(), decide); out.Error == "" {
				t.Fatal("explicit rejection was not returned")
			}
			if pending := read("session"); len(pending) != 1 || pending[0].ID != a.ID {
				t.Fatal("explicit rejection discarded the form")
			}
			reject.Store(false)
			if out := dispatcher.Handle(t.Context(), decide); out.Error != "" || writes.Load() != 2 {
				t.Fatalf("retry failed: %v (writes=%d)", out, writes.Load())
			}
			if len(read("session")) != 0 || dispatcher.Handle(t.Context(), decide).Error == "" || writes.Load() != 2 {
				t.Fatal("accepted question reappeared or could be submitted again")
			}
			expired.Store(true)
			if len(read("session")) != 0 {
				t.Fatal("a newer turn revived the old question")
			}
		})
	}
}

func questionState() map[string]any {
	return decodeObject(json.RawMessage(`{"id":"session","cwd":"/fixture","turns":[{"id":"turn","status":"inProgress","items":[{"type":"agentMessage","id":"call","text":"","questions":[{"title":"Pick a scope","options":["Local","Remote"]},{"title":"Any constraints?"}]}]}]}`))
}

func setQuestionState(t *testing.T, p *Provider, state map[string]any) {
	p.ipc.mu.Lock()
	p.ipc.followed["session"] = &followedThread{owner: "app-owner", state: rawJSON(state), changed: make(chan struct{})}
	p.ipc.mu.Unlock()
	p.syncIPCApprovals("session", "app-owner", state, fixtureStateTurns(t, state))
}

func questionAnswer(id, value string) provider.Decision {
	var d provider.Decision
	_ = json.Unmarshal(rawJSON(map[string]any{"decision": "input", "answers": map[string]any{id: map[string]any{"answers": []string{value}}}}), &d)
	return d
}

func TestAsyncQuestionDiscoveryAndExpiry(t *testing.T) {
	state := questionState()
	requests := asyncQuestionRequests("session", fixtureStateTurns(t, state), nil)
	if len(requests) != 1 {
		t.Fatalf("textless question missing: %v", requests)
	}
	params := object(object(requests[0])["params"])
	questions := array(params["questions"])
	id := stringValue(object(questions[0])["id"])
	if id != `["request_user_input_async","call",0]` || len(array(object(questions[0])["options"])) != 2 {
		t.Fatalf("wrong question contract: %v", questions)
	}
	turn := object(array(state["turns"])[0])
	answer := questionReplyOpen + "\n" + string(rawJSON([]asyncQuestionReply{{id, "Pick a scope", "Local"}})) + "\n" + questionReplyClose
	steer := map[string]any{"id": "reply", "type": "steeringUserMessage", "status": "pending", "input": []any{map[string]any{"type": "text", "text": answer}}}
	turn["items"] = append(array(turn["items"]), steer)
	if len(array(object(object(asyncQuestionRequests("session", fixtureStateTurns(t, state), nil)[0])["params"])["questions"])) != 2 {
		t.Fatal("unaccepted steer hid a question")
	}
	for _, acknowledged := range []string{"accepted", "acknowledged", "user"} {
		steer["status"] = acknowledged
		steer["steerAcknowledged"] = acknowledged == "acknowledged"
		if acknowledged == "user" {
			steer["type"], steer["content"] = "userMessage", steer["input"]
		}
		got := asyncQuestionRequests("session", fixtureStateTurns(t, state), nil)
		qs := array(object(object(got[0])["params"])["questions"])
		if len(qs) != 1 || stringValue(object(qs[0])["id"]) == id {
			t.Fatalf("%s did not resolve exact question: %v", acknowledged, qs)
		}
	}
	turn["status"] = "completed"
	if len(asyncQuestionRequests("session", fixtureStateTurns(t, state), nil)) != 1 {
		t.Fatal("completion discarded latest unanswered question")
	}
	state["turns"] = append(array(state["turns"]), map[string]any{"id": "next", "status": "inProgress", "items": []any{}})
	if len(asyncQuestionRequests("session", fixtureStateTurns(t, state), nil)) != 0 {
		t.Fatal("historical question was revived")
	}
}

func TestAsyncQuestionRepliesUseOwnerAndClaimOnce(t *testing.T) {
	for _, status := range []string{"inProgress", "completed"} {
		t.Run(status, func(t *testing.T) {
			p := New(config.Config{}, "", events.New())
			writes := make(chan codexipc.Message, 2)
			release := make(chan struct{})
			attachIPCFixture(t, p, func(m codexipc.Message) (codexipc.Message, bool) {
				if m.Method == "thread-follower-steer-turn" || m.Method == "thread-follower-start-turn" {
					writes <- m
					<-release
				}
				return codexipc.Message{}, false
			})
			released := false
			defer func() {
				if !released {
					close(release)
				}
			}()
			state := questionState()
			object(array(state["turns"])[0])["status"] = status
			setQuestionState(t, p, state)
			a := p.Approvals()[0]
			if !a.AllowSkip || len(a.Questions) != 2 || a.SessionID != "session" {
				t.Fatalf("not normalized: %+v", a)
			}
			done := make(chan error, 1)
			d := questionAnswer(a.Questions[0].ID, "A custom scope <x>\nnext line")
			go func() { done <- p.Decide(context.Background(), a.ID, d) }()
			var write codexipc.Message
			select {
			case write = <-writes:
			case <-time.After(3 * time.Second):
				t.Fatal("no question reply")
			}
			wantMethod := "thread-follower-steer-turn"
			if status == "completed" {
				wantMethod = "thread-follower-start-turn"
			}
			if write.Method != wantMethod || write.Target != "app-owner" {
				t.Fatalf("wrong owner transport: %+v", write)
			}
			params := decodeObject(write.Params)
			input := array(params["input"])
			if status == "completed" {
				input = array(object(object(params["turnStart"])["request"])["input"])
			} else if object(params["restoreMessage"])["id"] != params["clientUserMessageId"] {
				t.Fatal("missing composer restore context")
			}
			item := object(input[0])
			if item["text_elements"] == nil {
				t.Fatal("unsafe optimistic input")
			}
			text := stringValue(item["text"])
			var replies []asyncQuestionReply
			if len(text) < len(questionReplyOpen)+len(questionReplyClose) || json.Unmarshal([]byte(text[len(questionReplyOpen):len(text)-len(questionReplyClose)]), &replies) != nil || !reflect.DeepEqual(replies, []asyncQuestionReply{{a.Questions[0].ID, a.Questions[0].Question, d.Answers[a.Questions[0].ID].Answers[0]}}) {
				t.Fatalf("wrong reply: %s", text)
			}
			if err := p.Decide(context.Background(), a.ID, d); err == nil {
				t.Fatal("concurrent duplicate accepted")
			}
			p.syncIPCApprovals("session", "app-owner", state, fixtureStateTurns(t, state))
			if pending := p.Approvals(); len(pending) != 1 || pending[0].ID != a.ID {
				t.Fatal("in-flight snapshot replaced the pending form and its draft")
			}
			close(release)
			released = true
			if err := <-done; err != nil {
				t.Fatal(err)
			}
			// The owner's echo resolves one answer. The skipped sibling stays hidden.
			turn := object(array(state["turns"])[0])
			turn["items"] = append(array(turn["items"]), map[string]any{"type": "userMessage", "id": "reply", "content": input})
			setQuestionState(t, p, state)
			if len(p.Approvals()) != 0 {
				t.Fatal("skipped sibling reappeared after acknowledgement")
			}
		})
	}
}

func TestAsyncQuestionFailureRecoveryAndSkip(t *testing.T) {
	for _, outcome := range []string{"rejected", "unknown", "cancelled", "skip", "expired", "owner-changed"} {
		t.Run(outcome, func(t *testing.T) {
			p := New(config.Config{}, "", events.New())
			var writes atomic.Int32
			attachIPCFixture(t, p, func(m codexipc.Message) (codexipc.Message, bool) {
				if m.Method == "thread-follower-steer-turn" {
					if writes.Add(1) == 1 && (outcome == "rejected" || outcome == "unknown") {
						err := "no-client-found"
						if outcome == "unknown" {
							err = "request-timeout"
						}
						return codexipc.Message{ResultType: "error", Error: err}, true
					}
				}
				return codexipc.Message{}, false
			})
			state := questionState()
			setQuestionState(t, p, state)
			a := p.Approvals()[0]
			d := questionAnswer(a.Questions[0].ID, "Local")
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			if outcome == "cancelled" {
				cancel()
			}
			if outcome == "skip" {
				d = provider.Decision{Decision: "skip"}
			}
			if outcome == "expired" || outcome == "owner-changed" {
				p.ipc.mu.Lock()
				if outcome == "expired" {
					p.ipc.followed["session"].state = json.RawMessage(`{"turns":[]}`)
				} else {
					p.ipc.followed["session"].owner = "new-owner"
				}
				p.ipc.mu.Unlock()
			}
			err := p.Decide(ctx, a.ID, d)
			if (err == nil) != (outcome == "skip") {
				t.Fatalf("unexpected outcome: %v", err)
			}
			if outcome == "unknown" {
				var uncertain *codexipc.UnknownOutcome
				if !errors.As(err, &uncertain) {
					t.Fatalf("lost uncertain result: %v", err)
				}
			}
			p.syncIPCApprovals("session", "app-owner", state, fixtureStateTurns(t, state))
			if outcome == "unknown" || outcome == "skip" {
				if len(p.Approvals()) != 0 {
					t.Fatal("claimed question reappeared")
				}
			} else if outcome == "rejected" || outcome == "cancelled" {
				if len(p.Approvals()) != 1 {
					t.Fatal("definite rejection lost retry")
				}
				if err := p.Decide(context.Background(), a.ID, d); err != nil {
					t.Fatal(err)
				}
			} else if writes.Load() != 0 {
				t.Fatal("expired question was sent")
			}
			if outcome == "skip" && writes.Load() != 0 {
				t.Fatal("skip sent an Agent message")
			}
		})
	}
}

func TestSynchronousQuestionExactAnswers(t *testing.T) {
	p := New(config.Config{}, "", events.New())
	writes := make(chan codexipc.Message, 2)
	attachIPCFixture(t, p, func(m codexipc.Message) (codexipc.Message, bool) {
		if m.Method == "thread-follower-submit-user-input" {
			writes <- m
		}
		return codexipc.Message{}, false
	})
	state := decodeObject(json.RawMessage(`{"id":"session","requests":[{"requestId":42,"method":"item/tool/requestUserInput","params":{"turnId":"turn","questions":[{"id":"scope","header":"Scope","question":"Which scope?","options":[{"label":"Local","description":"This device"}]},{"id":"notes","question":"Notes?","isSecret":true}]}}]}`))
	setQuestionState(t, p, state)
	a := p.Approvals()[0]
	if a.AllowSkip || !a.Questions[1].IsSecret || a.Questions[0].Options[0].Description != "This device" {
		t.Fatalf("question metadata lost: %+v", a)
	}
	d := questionAnswer("scope", "Local")
	if err := p.Decide(context.Background(), a.ID, d); err == nil {
		t.Fatal("incomplete synchronous form accepted")
	}
	d.Answers["notes"] = questionAnswer("notes", "custom").Answers["notes"]
	if err := p.Decide(context.Background(), a.ID, d); err != nil {
		t.Fatal(err)
	}
	select {
	case write := <-writes:
		params := decodeObject(write.Params)
		if params["requestId"] != float64(42) || !reflect.DeepEqual(object(params["response"])["answers"], decodeObject(rawJSON(d))["answers"]) {
			t.Fatalf("wrong synchronous response: %s", write.Params)
		}
	case <-time.After(time.Second):
		t.Fatal("missing native user input response")
	}
	p.syncIPCApprovals("session", "app-owner", state, fixtureStateTurns(t, state))
	if len(p.Approvals()) != 0 {
		t.Fatal("snapshot revived sync response")
	}
	// The native app-server path uses the same normalization and exact IDs.
	id := p.addPending(rpc.Message{ID: json.RawMessage(`43`), Method: "item/tool/requestUserInput", Params: rawJSON(object(array(state["requests"])[0])["params"])}, func(_ context.Context, result any) error {
		if !reflect.DeepEqual(decodeObject(rawJSON(result))["answers"], decodeObject(rawJSON(d))["answers"]) {
			t.Fatal("native response changed answers")
		}
		return nil
	}, "service")
	if err := p.Decide(context.Background(), id, d); err != nil {
		t.Fatal(err)
	}
}

func fixtureStateTurns(t testing.TB, state map[string]any) []map[string]any {
	t.Helper()
	turns, _, err := stateTurns(state)
	if err != nil {
		t.Fatal(err)
	}
	return turns
}
