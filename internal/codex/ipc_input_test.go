package codex

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"testing"
	"time"

	"github.com/monkeyWie/reignhub/internal/codexipc"
	"github.com/monkeyWie/reignhub/internal/config"
	"github.com/monkeyWie/reignhub/internal/control"
	"github.com/monkeyWie/reignhub/internal/events"
	"github.com/monkeyWie/reignhub/internal/rpc"
)

func TestDirectInputsCarryDistinctDesktopMessageIDs(t *testing.T) {
	p := New(config.Config{}, "", events.New())
	requests := make(chan rpc.Message, 3)
	backendFixture(t, p, func(m rpc.Message) (any, *rpc.Error) {
		if m.Method == "thread/resume" {
			return map[string]any{}, nil
		}
		requests <- m
		return map[string]any{}, nil
	})
	if err := p.Send(t.Context(), "session", "same"); err != nil {
		t.Fatal(err)
	}
	for range 2 {
		if err := p.Steer(t.Context(), "session", "turn", "same"); err != nil {
			t.Fatal(err)
		}
	}
	seen := map[string]bool{}
	for _, method := range []string{"turn/start", "turn/steer", "turn/steer"} {
		m := <-requests
		params := decodeObject(m.Params)
		id := stringValue(params["clientUserMessageId"])
		if m.Method != method || id == "" || seen[id] || method == "turn/steer" && params["expectedTurnId"] != "turn" {
			t.Fatalf("invalid message identity or routing for %s: %s", m.Method, m.Params)
		}
		seen[id] = true
	}
}

func TestIPCTextInputsSafeForOptimisticAppRendering(t *testing.T) {
	p := New(config.Config{}, "", events.New())
	backendFixture(t, p, func(m rpc.Message) (any, *rpc.Error) {
		t.Errorf("unexpected private backend write: %s", m.Method)
		return nil, nil
	})
	var f *ipcFixture
	requests := make(chan codexipc.Message, 4)
	f = attachIPCFixture(t, p, func(m codexipc.Message) (codexipc.Message, bool) {
		if m.Method == "thread-stream-following-changed" {
			go f.snapshot("external", 1, "hello")
		}
		if m.Method == "thread-follower-start-turn" || m.Method == "thread-follower-steer-turn" {
			requests <- m
		}
		return codexipc.Message{}, false
	})
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	input := []any{map[string]string{"type": "text", "text": "fixture"}, map[string]string{"type": "localImage", "path": "/fixture/image.png"}, map[string]string{"type": "text", "text": "file reference"}}
	for _, send := range []func() error{
		func() error { return p.sendInput(ctx, "external", input, "") },
		func() error { return p.steerInput(ctx, "external", "t", input) },
	} {
		if err := send(); err != nil {
			t.Fatal(err)
		}
		m := <-requests
		params := decodeObject(m.Params)
		if m.Method == "thread-follower-start-turn" {
			params = object(object(params["turnStart"])["request"])
		}
		for _, value := range array(params["input"]) {
			item := object(value)
			if item["type"] == "text" {
				if _, ok := item["text_elements"].([]any); !ok {
					t.Fatal("App renderer would crash on text input", item)
				}
			}
		}
	}
	if len(input[0].(map[string]string)) != 2 {
		t.Fatal("mutated caller input")
	}
}

func TestIPCSteerUsesOwnerCompletedStateBeforeDispatch(t *testing.T) {
	p := New(config.Config{}, "", events.New())
	backendFixture(t, p, func(m rpc.Message) (any, *rpc.Error) {
		t.Errorf("unexpected private writer: %s", m.Method)
		return nil, nil
	})
	var f *ipcFixture
	requests := make(chan string, 2)
	f = attachIPCFixture(t, p, func(m codexipc.Message) (codexipc.Message, bool) {
		if m.Method == "thread-stream-following-changed" {
			state := map[string]any{"id": "external", "cwd": "/project", "turns": []any{map[string]any{"turnId": "finished", "status": "completed", "items": []any{}}}}
			go f.send(codexipc.Message{Type: "broadcast", Method: "thread-stream-state-changed", Source: "app-owner", Version: 11, Params: rawJSON(map[string]any{"hostId": "local", "conversationId": "external", "change": map[string]any{"type": "snapshot", "revision": 1, "conversationState": state}})})
		}
		if m.Method == "thread-follower-start-turn" || m.Method == "thread-follower-steer-turn" {
			requests <- m.Method
		}
		return codexipc.Message{}, false
	})
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	if err := p.Steer(ctx, "external", "stale-controller-turn", "new request"); err != nil {
		t.Fatal(err)
	}
	if method := <-requests; method != "thread-follower-start-turn" || len(requests) != 0 {
		t.Fatal("submitted a steer or replayed it", method)
	}
}

func TestIPCJavaScriptWriteErrorsAreUncertainNotReplayable(t *testing.T) {
	for _, message := range []string{"Cannot read properties of undefined (reading 'length')", "renderer error after persistence", "request-timeout"} {
		err := ipcWriteError("thread-follower-start-turn", codexipc.RemoteError(message))
		if !control.OutcomeUnknown(err) {
			t.Fatal("possible execution classified as rejected", err)
		}
	}
	err := ipcWriteError("thread-follower-steer-turn", codexipc.RemoteError("Cannot steer conversation fixture because its active turn already ended"))
	if control.OutcomeUnknown(err) {
		t.Fatal("pre-dispatch rejection classified as executed")
	}
	if !errors.Is(ipcWriteError("send", context.Canceled), context.Canceled) {
		t.Fatal("lost local error identity")
	}
}

func TestIPCOwnerPublicationNormalizesTextWithoutDiscardingRichElements(t *testing.T) {
	p := New(config.Config{}, "", events.New())
	b := newAppIPC(p)
	defer b.cancel()
	rich := map[string]any{"type": "text", "text": "reference", "text_elements": []any{map[string]any{"byteRange": map[string]any{"start": 0, "end": 9}, "placeholder": "reference"}}}
	o := &ownedThread{metadata: map[string]any{"cwd": "/fixture"}, settings: map[string]any{}, turns: []map[string]any{{"id": "turn", "status": "completed", "items": []any{
		map[string]any{"id": "user", "type": "userMessage", "content": []any{map[string]string{"type": "text", "text": "raw server input"}, rich}},
		map[string]any{"id": "steer", "type": "steeringUserMessage", "input": []any{map[string]string{"type": "text", "text": "steer"}}},
	}}}}
	state := b.ownerState("fixture", o)
	turns, _, err := stateTurns(state)
	if err != nil {
		t.Fatal(err)
	}
	items := array(turns[0]["items"])
	for _, input := range [][]any{array(object(items[0])["content"]), array(object(items[1])["input"])} {
		for _, value := range input {
			if _, ok := object(value)["text_elements"].([]any); !ok {
				t.Fatal("unsafe published input", value)
			}
		}
	}
	if len(array(object(array(object(items[0])["content"])[1])["text_elements"])) != 1 {
		t.Fatal("rich input elements lost")
	}
	if _, ok := object(array(o.turns[0]["items"])[0])["content"].([]any)[0].(map[string]string); !ok {
		t.Fatal("publication mutated executor state")
	}
}

func TestInstalledAppTextInputContract(t *testing.T) {
	asar := os.Getenv("REIGNHUB_CODEX_ASAR")
	if asar == "" {
		t.Skip("set REIGNHUB_CODEX_ASAR for offline renderer verification")
	}
	input := ipcInput([]any{map[string]string{"type": "text", "text": "isolated fixture"}, map[string]string{"type": "localImage", "path": "/fixture/image.png"}})
	var result json.RawMessage
	cmd := exec.Command("node", "testdata/installed-app/input.cjs", asar, string(rawJSON(input)))
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("installed renderer contract: %v: %s", err, out)
	}
	if json.Unmarshal(out, &result) != nil {
		t.Fatalf("invalid verifier output: %s", out)
	}
	t.Log(string(out))
}
