package codex

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"strings"
	"time"

	"github.com/monkeyWie/reignhub/internal/provider"
	"github.com/monkeyWie/reignhub/internal/rpc"
)

// Desktop broadcasts contain only locally staged messages. The durable server
// queue is a separate read-only source, joined by explicit client message IDs.
type serverQueue struct {
	queue     *provider.MessageQueue
	clientIDs map[string]string
}

func (p *Provider) readServerQueue(ctx context.Context, id string) *serverQueue {
	ctx, cancel := context.WithTimeout(ctx, 300*time.Millisecond)
	defer cancel()
	unknown := &serverQueue{queue: &provider.MessageQueue{State: "unknown"}}
	result := &serverQueue{queue: &provider.MessageQueue{State: "ready", Messages: []provider.QueuedMessage{}}, clientIDs: map[string]string{}}
	var cursor *string
	seen := map[string]bool{}
	size := 0
	for page := 0; page < 4; page++ {
		var raw json.RawMessage
		err := p.call(ctx, "thread/queue/list", map[string]any{"threadId": id, "cursor": cursor}, &raw)
		if err != nil {
			var rpcErr *rpc.Error
			if page == 0 && errors.As(err, &rpcErr) && rpcErr.Code == -32601 {
				return result
			}
			return unknown
		}
		size += len(raw)
		var out struct {
			Data       json.RawMessage
			NextCursor *string
		}
		if size > maxQueueBytes || json.Unmarshal(raw, &out) != nil {
			return unknown
		}
		messages, err := normalizeServerQueue(out.Data)
		if err != nil || len(result.queue.Messages)+len(messages.queue.Messages) > 100 {
			return unknown
		}
		for _, message := range messages.queue.Messages {
			if _, exists := result.clientIDs[message.ID]; exists {
				return unknown
			}
			result.clientIDs[message.ID] = messages.clientIDs[message.ID]
			result.queue.Messages = append(result.queue.Messages, message)
		}
		if ctx.Err() != nil {
			return unknown
		}
		if out.NextCursor == nil {
			return result
		}
		if *out.NextCursor == "" || len(*out.NextCursor) > 1024 || seen[*out.NextCursor] {
			return unknown
		}
		cursor = out.NextCursor
		seen[*cursor] = true
	}
	return unknown
}

func normalizeServerQueue(raw json.RawMessage) (*serverQueue, error) {
	var entries []struct {
		ID, ClientUserMessageID string
		Input                   []map[string]any
	}
	raw = bytes.TrimSpace(raw)
	if len(raw) == 0 || len(raw) > maxQueueBytes || raw[0] != '[' || json.Unmarshal(raw, &entries) != nil || len(entries) > 100 {
		return nil, errors.New("invalid server queue")
	}
	messages := make([]map[string]any, 0, len(entries))
	clients := map[string]string{}
	for _, entry := range entries {
		if len(entry.Input) > 200 || len(entry.ClientUserMessageID) > 1024 {
			return nil, errors.New("invalid queued input")
		}
		clients[entry.ID] = entry.ClientUserMessageID
		text := []string{}
		attachments := []any{}
		for _, input := range entry.Input {
			switch input["type"] {
			case "text":
				value, ok := input["text"].(string)
				if !ok {
					return nil, errors.New("invalid queued text")
				}
				// The App can store the visible message inside a full-input placeholder;
				// never display its execution envelope or export the rest of its context.
				if elements := array(input["text_elements"]); len(elements) > 0 {
					element := object(elements[0])
					placeholder := stringValue(element["placeholder"])
					const prefix = "codex-untrusted-app-input:"
					if strings.HasPrefix(placeholder, prefix) {
						span := object(element["byteRange"])
						var envelope struct {
							Message struct {
								Text      string
								ImageURLs []string
							}
						}
						if span["start"] != float64(0) || span["end"] != float64(len(value)) || json.Unmarshal([]byte(strings.TrimPrefix(placeholder, prefix)), &envelope) != nil {
							return nil, errors.New("invalid queued app message")
						}
						value = envelope.Message.Text
						for range envelope.Message.ImageURLs {
							attachments = append(attachments, map[string]any{"type": "image"})
						}
					}
				}
				text = append(text, value)
			case "image", "localImage":
				attachments = append(attachments, map[string]any{"type": "image", "path": input["path"]})
			case "file", "fileReference":
				attachments = append(attachments, map[string]any{"type": "file", "path": input["path"], "name": input["name"]})
			default:
				// Skills/mentions and future input types remain context, not user text.
			}
		}
		messages = append(messages, map[string]any{"id": entry.ID, "text": strings.Join(text, "\n"), "context": map[string]any{"attachments": attachments}})
	}
	q, err := normalizeQueue(rawJSON(messages))
	return &serverQueue{queue: q, clientIDs: clients}, err
}

func retainServerQueue(old, next *serverQueue) *serverQueue {
	if next.queue.State == "unknown" && old != nil {
		return &serverQueue{queue: &provider.MessageQueue{State: "unknown", Messages: old.queue.Messages}, clientIDs: old.clientIDs}
	}
	return next
}

func combineQueues(desktop *provider.MessageQueue, server *serverQueue) *provider.MessageQueue {
	out := &provider.MessageQueue{State: "ready", Messages: []provider.QueuedMessage{}}
	if desktop == nil || desktop.State != "ready" || server == nil || server.queue.State != "ready" {
		out.State = "unknown"
	}
	ids := map[string]bool{}
	if desktop != nil {
		for _, m := range desktop.Messages {
			ids[m.ID] = true
		}
	}
	if server != nil {
		for _, m := range server.queue.Messages {
			if !ids[m.ID] && !ids[server.clientIDs[m.ID]] {
				out.Messages = append(out.Messages, m)
			}
		}
	}
	if desktop != nil {
		out.Messages = append(out.Messages, desktop.Messages...)
	}
	return out
}

func (p *Provider) serverQueueChanged(raw json.RawMessage) {
	var params struct{ ThreadID string }
	if json.Unmarshal(raw, &params) != nil || params.ThreadID == "" {
		return
	}
	b := p.appBridge()
	if b == nil {
		if p.sharedBackend() != nil {
			p.stream().log.Publish(provider.StreamEvent{Kind: "snapshot", SessionID: params.ThreadID})
		}
		return
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	f, o := b.followed[params.ThreadID], b.owned[params.ThreadID]
	if f == nil && o == nil {
		return
	}
	if f != nil {
		f.queueRevision++
		if f.serverQueue != nil {
			f.serverQueue = retainServerQueue(f.serverQueue, &serverQueue{queue: &provider.MessageQueue{State: "unknown"}})
		}
	}
	if o != nil {
		o.queueRevision++
		if o.serverQueue != nil {
			o.serverQueue = retainServerQueue(o.serverQueue, &serverQueue{queue: &provider.MessageQueue{State: "unknown"}})
		}
	}
	// Notifications contain no queue data. Reuse the selected session's snapshot
	// reconciliation rather than reading on the RPC receive loop or adding polls.
	p.stream().log.Publish(provider.StreamEvent{Kind: "snapshot", SessionID: params.ThreadID})
}
