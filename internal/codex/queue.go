package codex

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"maps"
	"path"
	"strings"
	"time"

	"github.com/monkeyWie/reignhub/internal/codexipc"
	"github.com/monkeyWie/reignhub/internal/provider"
)

const maxQueueBytes = 4 << 20

func normalizeQueue(raw json.RawMessage) (*provider.MessageQueue, error) {
	raw = bytes.TrimSpace(raw)
	if len(raw) > maxQueueBytes || len(raw) == 0 || raw[0] != '[' {
		return nil, errors.New("invalid queued message list")
	}
	var messages []map[string]any
	if err := json.Unmarshal(raw, &messages); err != nil || len(messages) > 100 {
		return nil, errors.New("invalid queued message list")
	}
	out := &provider.MessageQueue{State: "ready", Messages: []provider.QueuedMessage{}}
	seen := map[string]bool{}
	for _, message := range messages {
		id, text := stringValue(message["id"]), stringValue(message["text"])
		if value := message["text"]; value != nil {
			if _, ok := value.(string); !ok {
				return nil, errors.New("invalid queued message text")
			}
		}
		if id == "" || seen[id] || len(id) > 1024 {
			return nil, errors.New("invalid queued message identity")
		}
		seen[id] = true
		item := provider.QueuedMessage{ID: id, Text: text, Status: "queued"}
		if message["preparation"] != nil {
			item.Status = "preparing"
		}
		if stringValue(message["pausedReason"]) != "" {
			item.Status = "paused"
		}
		if object(message["submission"])["status"] == "outcome-unknown" {
			item.Status = "uncertain"
		}
		context := object(message["context"])
		for _, key := range []string{"attachments", "imageAttachments", "fileAttachments", "pastedTextAttachments", "uploadedFileAttachments", "commentAttachments", "mcpAppModelContextAttachments"} {
			if value := context[key]; value != nil {
				if _, ok := value.([]any); !ok {
					return nil, errors.New("invalid queued attachments")
				}
			}
			for _, raw := range array(context[key]) {
				if len(item.Attachments) >= 100 {
					return nil, errors.New("too many queued attachments")
				}
				a := object(raw)
				name := stringValue(a["name"])
				if name == "" {
					name = stringValue(a["fileName"])
				}
				if name == "" {
					name = stringValue(a["path"])
				}
				if name == "" {
					name = stringValue(a["filePath"])
				}
				if name == "" {
					name = stringValue(a["localPath"])
				}
				name = path.Base(strings.ReplaceAll(name, `\`, "/"))
				if name == "." || name == "/" {
					name = ""
				}
				kind := "file"
				if key == "imageAttachments" || a["type"] == "image" || a["type"] == "localImage" || strings.HasPrefix(stringValue(a["mimeType"]), "image/") {
					kind = "image"
				}
				item.Attachments = append(item.Attachments, provider.QueuedAttachment{Name: name, Kind: kind})
			}
		}
		out.Messages = append(out.Messages, item)
	}
	return out, nil
}

// Called under b.mu. Previously shown data is retained by controllers as stale,
// while a new owner/connection must establish its own queue baseline.
func (b *appIPC) invalidateQueue(id string, f *followedThread) {
	f.queue = nil
	f.serverQueue = nil
	f.accepted = nil
	f.queueRevision++
	b.p.stream().log.Publish(provider.StreamEvent{Kind: "queue", SessionID: id, Queue: &provider.MessageQueue{State: "unknown"}})
}

func (b *appIPC) receiveQueue(m codexipc.Message, id string) {
	if m.Version != 3 || m.HostID != "" {
		return
	}
	var params struct{ Messages json.RawMessage }
	if json.Unmarshal(m.Params, &params) != nil {
		return
	}
	queue, err := normalizeQueue(params.Messages)
	b.mu.Lock()
	defer b.mu.Unlock()
	f := b.followed[id]
	if f == nil || f.owner == "" || f.owner != m.Source {
		return
	}
	if err != nil {
		queue = &provider.MessageQueue{State: "unknown"}
		if f.queue != nil {
			queue.Messages = f.queue.Messages
		}
	}
	f.queue = queue
	b.p.stream().log.Publish(provider.StreamEvent{Kind: "queue", SessionID: id, Queue: projectQueue(queue, f.serverQueue, f.accepted)})
}

func (p *Provider) messageQueue(ctx context.Context, id, cursor string, accepted map[string]bool) *provider.MessageQueue {
	b := p.appBridge()
	if cursor != "" || b == nil && p.sharedBackend() == nil {
		return nil
	}
	ctx, cancel := context.WithTimeout(ctx, 500*time.Millisecond)
	defer cancel()
	if b == nil {
		server := p.readServerQueue(ctx, id)
		staged := readStagedQueue(ctx, codexProjectHome(), vscodeQueueFiles(), id)
		return projectQueue(staged, server, accepted)
	}
	b.mu.Lock()
	f, o := b.followed[id], b.owned[id]
	var revision uint64
	if f != nil {
		revision = f.queueRevision
	}
	if o != nil {
		revision = o.queueRevision
	}
	b.mu.Unlock()
	server := p.readServerQueue(ctx, id)
	staged := readStagedQueue(ctx, b.home, b.queueFiles, id)
	b.mu.Lock()
	defer b.mu.Unlock()
	// Never promote a read across owner loss or a newer server notification.
	if f != b.followed[id] || o != b.owned[id] || (f != nil && revision != f.queueRevision) || (o != nil && revision != o.queueRevision) {
		return &provider.MessageQueue{State: "unknown"}
	}
	if o := b.owned[id]; o != nil {
		server = retainServerQueue(o.serverQueue, server)
		o.serverQueue = server
		return projectQueue(ownedMessageQueue(o), server, accepted)
	}
	if f := b.followed[id]; f != nil {
		server = retainServerQueue(f.serverQueue, server)
		f.serverQueue = server
		if f.queue != nil {
			staged = f.queue
		}
		ids := maps.Clone(f.accepted)
		if ids == nil {
			ids = map[string]bool{}
		}
		maps.Copy(ids, accepted)
		return projectQueue(staged, server, ids)
	}
	return projectQueue(staged, server, accepted)
}

// A missed consumption broadcast (or a lagging staging export) must not show an
// acknowledged message as waiting to be sent. Only explicit server receipts
// count; optimistic/pending items and equal text do not establish delivery.
func acceptedQueueMessages(turns []map[string]any) map[string]bool {
	ids := map[string]bool{}
	for t := len(turns) - 1; t >= 0 && len(ids) < 256; t-- {
		items := array(turns[t]["items"])
		for i := len(items) - 1; i >= 0 && len(ids) < 256; i-- {
			item := object(items[i])
			var fields []string
			switch item["type"] {
			case "userMessage":
				fields = []string{"clientId"}
			case "steeringUserMessage":
				if item["status"] == "accepted" || item["steerAcknowledged"] == true {
					fields = []string{"clientUserMessageId", "serverClientUserMessageId"}
				}
			}
			for _, field := range fields {
				if id := stringValue(item[field]); id != "" && len(ids) < 256 {
					ids[id] = true
				}
			}
		}
	}
	return ids
}

func projectQueue(staged *provider.MessageQueue, server *serverQueue, accepted map[string]bool) *provider.MessageQueue {
	q := combineQueues(staged, server)
	remaining := q.Messages[:0]
	for _, message := range q.Messages {
		if accepted[message.ID] || server != nil && accepted[server.clientIDs[message.ID]] {
			continue
		}
		remaining = append(remaining, message)
	}
	q.Messages = remaining
	return q
}

// Caller holds b.mu; project execution uncertainty without changing the vendor
// queue or replaying a submission just to make its presentation current.
func ownedMessageQueue(o *ownedThread) *provider.MessageQueue {
	raw := rawJSON(o.queue)
	if o.queue == nil {
		raw = json.RawMessage("[]")
	}
	q, err := normalizeQueue(raw)
	if err != nil {
		return &provider.MessageQueue{State: "unknown"}
	}
	for i := range q.Messages {
		m := &q.Messages[i]
		if o.inflight == m.ID {
			m.Status = "preparing"
		}
		if o.uncertain && o.blocked == m.ID {
			m.Status = "uncertain"
		}
	}
	return q
}
