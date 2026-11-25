package codex

import (
	"context"
	"encoding/json"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/monkeyWie/reignhub/internal/codexipc"
	"github.com/monkeyWie/reignhub/internal/config"
	"github.com/monkeyWie/reignhub/internal/events"
	"github.com/monkeyWie/reignhub/internal/provider"
	"github.com/monkeyWie/reignhub/internal/rpc"
)

func TestServerQueueMergeAndLiveConsumption(t *testing.T) {
	p := New(config.Config{}, "", events.New())
	var mu sync.Mutex
	data := json.RawMessage(`[{"id":"server-first","clientUserMessageId":"first","input":[{"type":"text","text":"same text"},{"type":"localImage","path":"/private/photo.png"}]},{"id":"server-staged","clientUserMessageId":"local","input":[{"type":"text","text":"same text"}]}]`)
	backendFixture(t, p, func(m rpc.Message) (any, *rpc.Error) {
		if m.Method != "thread/queue/list" {
			t.Errorf("queue read wrote to backend: %s", m.Method)
		}
		mu.Lock()
		defer mu.Unlock()
		return map[string]any{"data": data}, nil
	})
	f := attachIPCFixture(t, p, nil)
	f.b.mu.Lock()
	f.b.followed["selected"] = &followedThread{owner: "app-owner", changed: make(chan struct{}), state: rawJSON(map[string]any{"id": "selected", "turns": []any{}})}
	f.b.mu.Unlock()
	writeQueueJSON(t, f.b.home, []any{map[string]any{"id": "local", "text": "same text"}})
	q := p.messageQueue(context.Background(), "selected", "", nil)
	if q.State != "ready" || len(q.Messages) != 2 || q.Messages[0].ID != "server-first" || q.Messages[1].ID != "local" {
		t.Fatalf("combined order/identity: %+v", q)
	}
	if q.Messages[0].Attachments[0].Name != "photo.png" || strings.Contains(string(rawJSON(q)), "private") {
		t.Fatal("unsafe attachment projection")
	}
	epoch, cursor := p.stream().log.Position()
	// Clearing the desktop staging queue must retain independent server input.
	f.b.Broadcast(codexipc.Message{Method: "thread-queued-followups-changed", Version: 3, Source: "app-owner", Params: rawJSON(map[string]any{"hostId": "local", "conversationId": "selected", "messages": []any{}})})
	batch, _ := p.stream().log.Probe(context.Background(), "selected", epoch, cursor)
	if len(batch.Events) != 1 || len(batch.Events[0].Queue.Messages) != 2 {
		t.Fatal("desktop event hid durable queue", batch)
	}
	mu.Lock()
	data = json.RawMessage(`[]`)
	mu.Unlock()
	p.observeStream(rpc.Message{Method: "thread/queue/changed", Params: rawJSON(map[string]any{"threadId": "selected"})})
	batch, _ = p.stream().log.Probe(context.Background(), "selected", epoch, batch.Cursor)
	if len(batch.Events) != 1 || batch.Events[0].Kind != "snapshot" {
		t.Fatal("queue notification not reconciled", batch)
	}
	q = p.messageQueue(context.Background(), "selected", "", nil)
	if q.State != "ready" || len(q.Messages) != 0 {
		t.Fatal("consumed queue retained", q)
	}
	if p.messageQueue(context.Background(), "selected", "older", nil) != nil {
		t.Fatal("older history overwrites queue")
	}
}

func TestSharedServerQueueClearsAfterNotification(t *testing.T) {
	home := t.TempDir()
	t.Setenv("CODEX_HOME", home)
	t.Setenv("HOME", home)
	t.Setenv("XDG_CONFIG_HOME", home)
	t.Setenv("AppData", home)
	p := New(config.Config{}, "", events.New())
	p.shared = &sharedConnection{}
	var mu sync.Mutex
	entries := json.RawMessage(`[{"id":"server","clientUserMessageId":"local","input":[{"type":"text","text":"queued"}]}]`)
	backendFixture(t, p, func(m rpc.Message) (any, *rpc.Error) {
		if m.Method != "thread/queue/list" || stringValue(decodeObject(m.Params)["threadId"]) != "selected" {
			t.Errorf("unexpected shared RPC: %s", m.Method)
		}
		mu.Lock()
		defer mu.Unlock()
		return map[string]any{"data": entries}, nil
	})
	ctx := context.Background()
	writeQueueJSON(t, home, []any{map[string]any{"id": "local", "text": "queued"}})
	q := p.messageQueue(ctx, "selected", "", nil)
	if q == nil || q.State != "ready" || len(q.Messages) != 1 || q.Messages[0].ID != "local" {
		t.Fatal("shared staging and server queue did not reconcile", q)
	}
	epoch, cursor := p.stream().log.Position()
	mu.Lock()
	entries = json.RawMessage(`[]`)
	mu.Unlock()
	writeQueueJSON(t, home, []any{})
	p.observeStream(rpc.Message{Method: "thread/queue/changed", Params: rawJSON(map[string]any{"threadId": "selected"})})
	batch, err := p.stream().log.Probe(ctx, "selected", epoch, cursor)
	if err != nil || len(batch.Events) != 1 || batch.Events[0].Kind != "snapshot" {
		t.Fatal("shared queue change did not request a snapshot", err, batch)
	}
	q = p.messageQueue(ctx, "selected", "", nil)
	if q == nil || q.State != "ready" || len(q.Messages) != 0 || p.messageQueue(ctx, "selected", "older", nil) != nil {
		t.Fatal("shared queue did not clear on the latest page", q)
	}
}

func TestServerQueuePagingFailureCancellationAndRecovery(t *testing.T) {
	for _, mode := range []string{"pages", "unsupported", "malformed", "repeat", "oversized", "cancel"} {
		t.Run(mode, func(t *testing.T) {
			p := New(config.Config{}, "", events.New())
			var mu sync.Mutex
			calls := 0
			release := make(chan struct{})
			defer close(release)
			backendFixture(t, p, func(m rpc.Message) (any, *rpc.Error) {
				mu.Lock()
				calls++
				n := calls
				mu.Unlock()
				if m.Method != "thread/queue/list" {
					t.Errorf("unexpected method %s", m.Method)
				}
				switch mode {
				case "unsupported":
					return nil, &rpc.Error{Code: -32601}
				case "malformed":
					return map[string]any{"data": nil}, nil
				case "repeat":
					return map[string]any{"data": []any{}, "nextCursor": "same"}, nil
				case "oversized":
					return map[string]any{"data": []any{map[string]any{"id": "too-large", "input": []any{map[string]any{"type": "text", "text": strings.Repeat("x", maxQueueBytes)}}}}}, nil
				case "cancel":
					<-release
					return map[string]any{"data": []any{}}, nil
				}
				if n == 1 {
					return map[string]any{"data": []any{map[string]any{"id": "first"}}, "nextCursor": "second"}, nil
				}
				if stringValue(decodeObject(m.Params)["cursor"]) != "second" {
					t.Error("cursor lost")
				}
				return map[string]any{"data": []any{map[string]any{"id": "second"}}}, nil
			})
			ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
			defer cancel()
			if mode != "cancel" {
				ctx = context.Background()
			}
			start := time.Now()
			q := p.readServerQueue(ctx, "selected")
			want := "unknown"
			if mode == "pages" || mode == "unsupported" {
				want = "ready"
			}
			if q.queue.State != want {
				t.Fatalf("queue state: %s", q.queue.State)
			}
			if mode == "pages" && len(q.queue.Messages) != 2 {
				t.Fatal("page missing")
			}
			if time.Since(start) > time.Second {
				t.Fatal("queue read blocked navigation")
			}
			if mode == "repeat" {
				mu.Lock()
				defer mu.Unlock()
				if calls != 2 {
					t.Fatal("unbounded cursor retry")
				}
			}
			previous := &serverQueue{queue: &provider.MessageQueue{State: "ready", Messages: []provider.QueuedMessage{{ID: "cached"}}}}
			stale := retainServerQueue(previous, &serverQueue{queue: &provider.MessageQueue{State: "unknown"}})
			if stale.queue.State != "unknown" || len(stale.queue.Messages) != 1 {
				t.Fatal("failed read erased queue")
			}
			empty := &serverQueue{queue: &provider.MessageQueue{State: "ready"}}
			if len(retainServerQueue(stale, empty).queue.Messages) != 0 {
				t.Fatal("recovery did not clear queue")
			}
		})
	}
}

func TestServerQueueDoesNotPublishAcrossNewerNotification(t *testing.T) {
	p := New(config.Config{}, "", events.New())
	entered, release := make(chan struct{}), make(chan struct{})
	backendFixture(t, p, func(m rpc.Message) (any, *rpc.Error) {
		close(entered)
		<-release
		return map[string]any{"data": []any{map[string]any{"id": "obsolete"}}}, nil
	})
	f := attachIPCFixture(t, p, nil)
	f.b.mu.Lock()
	f.b.followed["selected"] = &followedThread{owner: "app-owner", changed: make(chan struct{})}
	f.b.mu.Unlock()
	result := make(chan *provider.MessageQueue, 1)
	go func() { result <- p.messageQueue(context.Background(), "selected", "", nil) }()
	select {
	case <-entered:
	case <-time.After(time.Second):
		t.Fatal("read not started")
	}
	p.observeStream(rpc.Message{Method: "thread/queue/changed", Params: rawJSON(map[string]any{"threadId": "selected"})})
	close(release)
	q := <-result
	if q.State != "unknown" || len(q.Messages) != 0 {
		t.Fatal("old read promoted across notification", q)
	}
}

func TestServerQueueAppMessageAndComposerAttachments(t *testing.T) {
	prompt := "Respond to the user input in the context of our conversation."
	envelope := map[string]any{"message": map[string]any{"text": "visible message", "imageUrls": []string{"data:image/png;base64,AA=="}}, "promptContextWithoutReviewedMessages": "private context"}
	raw := rawJSON([]any{map[string]any{"id": "app", "input": []any{map[string]any{"type": "text", "text": prompt, "text_elements": []any{map[string]any{"byteRange": map[string]any{"start": 0, "end": len(prompt)}, "placeholder": "codex-untrusted-app-input:" + string(rawJSON(envelope))}}}}}})
	q, err := normalizeServerQueue(raw)
	if err != nil || q.queue.Messages[0].Text != "visible message" || len(q.queue.Messages[0].Attachments) != 1 || strings.Contains(string(rawJSON(q.queue)), "private") {
		t.Fatal(q, err)
	}
	desktop, err := normalizeQueue(rawJSON([]any{map[string]any{"id": "local", "context": map[string]any{"imageAttachments": []any{map[string]any{"localPath": `C:\private\a.png`}}, "fileAttachments": []any{map[string]any{"name": "report.pdf"}}}}}))
	if err != nil || !reflect.DeepEqual(desktop.Messages[0].Attachments, []provider.QueuedAttachment{{Name: "a.png", Kind: "image"}, {Name: "report.pdf", Kind: "file"}}) {
		t.Fatal(desktop, err)
	}
}
