package codex

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/coder/websocket"
	"github.com/monkeyWie/reignhub/internal/config"
	"github.com/monkeyWie/reignhub/internal/events"
	"github.com/monkeyWie/reignhub/internal/provider"
	"github.com/monkeyWie/reignhub/internal/rpc"
)

// Reproduce Codex's deferred materialization: thread/start subscribes the
// creating connection, but resume/turns-list cannot read a rollout yet.
func TestNewThreadFirstSendDoesNotResumeUnmaterializedHistory(t *testing.T) {
	t.Setenv("CODEX_HOME", t.TempDir())
	for _, attachments := range []bool{false, true} {
		t.Run(map[bool]string{false: "text", true: "attachments"}[attachments], func(t *testing.T) {
			var starts, resumes, sends atomic.Int32
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				ws, err := websocket.Accept(w, r, nil)
				if err != nil {
					return
				}
				defer ws.CloseNow()
				for {
					_, raw, err := ws.Read(r.Context())
					if err != nil {
						return
					}
					var m rpc.Message
					if json.Unmarshal(raw, &m) != nil {
						return
					}
					if m.Method == "initialized" {
						continue
					}
					result := any(map[string]any{})
					var rpcError *rpc.Error
					switch m.Method {
					case "initialize":
					case "thread/start":
						starts.Add(1)
						result = map[string]any{"thread": map[string]any{"id": "fresh", "cwd": "/project", "status": map[string]any{"type": "idle"}}}
					case "thread/resume":
						resumes.Add(1)
						rpcError = &rpc.Error{Code: -32600, Message: "no rollout found for thread id fresh"}
					case "thread/read":
						result = map[string]any{"thread": map[string]any{"id": "fresh", "cwd": "/project", "status": map[string]any{"type": "idle"}}}
					case "thread/turns/list":
						rpcError = &rpc.Error{Code: -32600, Message: "thread fresh is not materialized yet; thread/turns/list is unavailable before first user message"}
					case "turn/start":
						var params struct {
							ThreadID string
							Input    []struct{ Text string }
						}
						_ = json.Unmarshal(m.Params, &params)
						if params.ThreadID != "fresh" || len(params.Input) != 1 || params.Input[0].Text != "first message" {
							t.Error("first message changed identity or input", string(m.Params))
						}
						sends.Add(1)
					default:
						t.Error("unexpected method", m.Method)
					}
					response := map[string]any{"id": m.ID, "result": result}
					if rpcError != nil {
						delete(response, "result")
						response["error"] = rpcError
					}
					payload, _ := json.Marshal(response)
					_ = ws.Write(r.Context(), websocket.MessageText, payload)
				}
			}))
			defer server.Close()
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			p := New(config.Config{DataDir: t.TempDir()}, "", events.New())
			dial := func() *rpc.Client {
				c, e := rpc.Dial(ctx, "ws"+strings.TrimPrefix(server.URL, "http"), p.observe)
				if e != nil {
					t.Fatal(e)
				}
				return c
			}
			p.client = dial()
			defer p.Close()
			session, e := p.Create(ctx, provider.CreateOptions{CWD: "/project"})
			if e != nil || session.ID != "fresh" {
				t.Fatal(session, e)
			}
			transcript, e := p.Transcript(ctx, session.ID, "")
			if e != nil || len(transcript.Turns) != 0 || transcript.Session.ID != "fresh" {
				t.Fatal(transcript, e)
			}
			batch, e := p.Watch(ctx, session.ID, "", 0)
			if e != nil || !batch.Reset {
				t.Fatal(batch, e)
			}
			if attachments {
				e = p.SendAttachments(ctx, session.ID, "first message", "", nil)
			} else {
				e = p.Send(ctx, session.ID, "first message")
			}
			if e != nil {
				t.Fatal(e)
			}
			if resumes.Load() != 0 || sends.Load() != 1 || starts.Load() != 1 {
				t.Fatal("new thread was resumed or duplicated")
			}
			// A server unload invalidates the exception. Never create another session
			// to recover an unavailable old one.
			p.observe(rpc.Message{Method: "thread/closed", Params: json.RawMessage(`{"threadId":"fresh"}`)}, nil)
			if e = p.Send(ctx, session.ID, "second message"); e == nil || !strings.Contains(e.Error(), "no rollout found") {
				t.Fatal("closed thread skipped resume", e)
			}
			p.mu.Lock()
			p.created["fresh"] = p.client
			old := p.client
			p.mu.Unlock()
			replacement := dial()
			p.mu.Lock()
			p.client = replacement
			p.mu.Unlock()
			_ = old.Close()
			if e = p.Send(ctx, session.ID, "third message"); e == nil {
				t.Fatal("new connection reused creating subscription")
			}
			if starts.Load() != 1 || sends.Load() != 1 || resumes.Load() != 2 {
				t.Fatal("reconnect silently recreated or sent to the wrong session")
			}
			if _, e = p.Transcript(ctx, session.ID, ""); e == nil {
				t.Fatal("unknown history failure was hidden after reconnect")
			}
		})
	}
}
