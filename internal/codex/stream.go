package codex

import (
	"context"
	"encoding/json"
	"strings"
	"sync"
	"time"
	"unicode/utf16"

	"github.com/monkeyWie/reignhub/internal/provider"
	"github.com/monkeyWie/reignhub/internal/rpc"
)

type liveItem struct {
	offset   int
	summary  string
	part     int
	lastSync time.Time
}

type liveStream struct {
	mu    sync.Mutex
	log   *provider.EventLog
	items map[string]*liveItem
}

func (p *Provider) stream() *liveStream {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.live == nil {
		p.live = &liveStream{log: provider.NewEventLog(), items: map[string]*liveItem{}}
	}
	return p.live
}

func (p *Provider) Watch(ctx context.Context, session, epoch string, after int64) (provider.StreamBatch, error) {
	return p.watch(ctx, session, epoch, after, false)
}
func (p *Provider) Probe(ctx context.Context, session, epoch string, after int64) (provider.StreamBatch, error) {
	return p.watch(ctx, session, epoch, after, true)
}
func (p *Provider) watch(ctx context.Context, session, epoch string, after int64, probe bool) (provider.StreamBatch, error) {
	log := p.stream().log
	// A replay cursor describes buffered events, not the lifetime of the
	// upstream subscription. History may supply it before the first follow.
	batch, err := log.Probe(ctx, session, epoch, after)
	if err != nil {
		return batch, err
	}
	if b := p.appBridge(); b != nil {
		if err := b.refreshFollower(ctx, session); err != nil {
			return provider.StreamBatch{}, err
		}
		b.mu.Lock()
		f := b.followed[session]
		repair := f == nil || f.err != nil || f.owner == "" || len(f.state) == 0
		if f != nil {
			// Opening a sidebar-followed thread retains its subscription even
			// when directory cleanup observes a released writer lock.
			f.directory = false
		}
		b.mu.Unlock()
		if (repair || batch.Reset) && !b.owns(session) {
			owner, err := b.follow(ctx, session)
			if err != nil {
				return provider.StreamBatch{}, err
			}
			if owner == "" && repair && f != nil {
				return provider.StreamBatch{}, provider.ErrUnavailable
			}
			if owner != "" {
				if _, err = b.waitState(ctx, session, owner); err != nil {
					return provider.StreamBatch{}, err
				}
			}
		}
	} else if shared := p.activeShared(); shared {
		if err := p.followShared(ctx, session); err != nil {
			return provider.StreamBatch{}, err
		}
	} else if batch.Reset {
		if _, err := p.threadClient(ctx, session); err != nil {
			return provider.StreamBatch{}, err
		}
	}
	if batch.Reset {
		return log.Probe(ctx, session, "", 0)
	}
	if probe {
		return log.Probe(ctx, session, epoch, after)
	}
	return log.Watch(ctx, session, epoch, after)
}

func utf16Length(s string) int {
	n := 0
	for _, r := range s {
		n += utf16.RuneLen(r)
	}
	return n
}

func (p *Provider) observeStream(m rpc.Message) {
	if m.Method == "thread/queue/changed" {
		p.serverQueueChanged(m.Params)
		return
	}
	// Only forward an explicit presentation allowlist. In particular, raw
	// reasoning deltas, command output and tool arguments never enter this log.
	switch m.Method {
	case "turn/started", "turn/completed", "item/started", "item/completed",
		"item/agentMessage/delta", "item/reasoning/summaryTextDelta":
	default:
		return
	}
	var params struct {
		ThreadID, TurnID, ItemID, Delta string
		SummaryIndex                    int
		Turn                            struct {
			ID, Status                         string
			DurationMS, StartedAt, CompletedAt *int64
		}
		Item                       json.RawMessage
		StartedAtMS, CompletedAtMS *int64
	}
	if json.Unmarshal(m.Params, &params) != nil || params.ThreadID == "" {
		return
	}
	s := p.stream()
	s.mu.Lock()
	defer s.mu.Unlock()
	event := provider.StreamEvent{SessionID: params.ThreadID, TurnID: params.TurnID, ItemID: params.ItemID}
	if strings.HasPrefix(m.Method, "turn/") {
		event.Kind, event.TurnID, event.Status = "turn", params.Turn.ID, params.Turn.Status
		event.DurationMS = turnDuration(params.Turn.DurationMS, params.Turn.StartedAt, params.Turn.CompletedAt)
		if event.Status == "" {
			if m.Method == "turn/started" {
				event.Status = "inProgress"
			} else {
				event.Status = "completed"
			}
		}
		event.RecencyAt = recordedTurnRecency(event.Status, params.Turn.StartedAt, params.Turn.CompletedAt, nil, nil, params.Turn.DurationMS)
		p.rememberSessionRecency(params.ThreadID, event.RecencyAt)
		if event.TurnID != "" {
			s.log.Publish(event)
			if m.Method == "turn/completed" {
				prefix := params.ThreadID + "/" + event.TurnID + "/"
				for key := range s.items {
					if strings.HasPrefix(key, prefix) {
						delete(s.items, key)
					}
				}
			}
		}
		return
	}
	if params.TurnID == "" {
		return
	}
	if m.Method == "item/started" || m.Method == "item/completed" {
		raw, _ := json.Marshal(map[string]any{"id": params.TurnID, "status": "inProgress", "items": []json.RawMessage{params.Item}})
		turn := normalizeTurn(raw)
		if len(turn.Items) != 1 || turn.Items[0].ID == "" {
			return
		}
		item := turn.Items[0]
		if item.Role == "internal" || item.Type == "contextCompaction" && m.Method == "item/started" {
			return
		}
		if item.Role == "tool" {
			item.Text = ""
			if item.Status == "" {
				item.Status = "inProgress"
				if m.Method == "item/completed" {
					item.Status = "completed"
				}
			}
		}
		if params.StartedAtMS != nil {
			item.TimestampMS = params.StartedAtMS
		} else if params.CompletedAtMS != nil {
			item.TimestampMS = params.CompletedAtMS
		}
		p.rememberSessionRecency(params.ThreadID, provider.ConversationRecency([]provider.Turn{{Items: []provider.Item{item}}}))
		key := params.ThreadID + "/" + params.TurnID + "/" + item.ID
		if len(s.items) >= 256 {
			s.items = map[string]*liveItem{}
		}
		if m.Method == "item/started" {
			s.items[key] = &liveItem{offset: utf16Length(item.Text), summary: item.ReasoningSummary}
		} else {
			delete(s.items, key)
		}
		event.Kind, event.ItemID, event.Item = "item", item.ID, &item
		if item.Role == "user" && userAttachmentSnapshot(params.Item) || item.Type == "imageGeneration" {
			event.Kind, event.Item = "snapshot", nil
		}
		s.log.Publish(event)
		return
	}
	if params.ItemID == "" || params.Delta == "" {
		return
	}
	key := params.ThreadID + "/" + params.TurnID + "/" + params.ItemID
	item := s.items[key]
	if m.Method == "item/reasoning/summaryTextDelta" {
		if item == nil {
			item = &liveItem{}
			s.items[key] = item
		}
		if item.part != params.SummaryIndex {
			item.part, item.summary = params.SummaryIndex, ""
		}
		if len(item.summary)+len(params.Delta) > 8192 {
			return
		}
		item.summary += params.Delta
		event.Kind, event.Summary = "summary", item.summary
		s.log.Publish(event)
		return
	}
	if item == nil || item.offset < 0 {
		// We may attach halfway through an existing output. Without its start
		// we cannot safely append deltas; reconcile snapshots until completion.
		if item == nil {
			item = &liveItem{offset: -1}
			s.items[key] = item
		}
		if time.Since(item.lastSync) >= 250*time.Millisecond {
			item.lastSync = time.Now()
			event.Kind = "snapshot"
			s.log.Publish(event)
		}
		return
	}
	event.Kind, event.Delta, event.Offset = "text", params.Delta, item.offset
	item.offset += utf16Length(params.Delta)
	s.log.Publish(event)
}

// Plain user messages are already complete in the item event. Only file/image
// inputs need the attachment importer to reread history and preserve their bytes.
func userAttachmentSnapshot(raw json.RawMessage) bool {
	var item struct {
		Type           string
		Content, Input []struct{ Type, Text string }
	}
	if json.Unmarshal(raw, &item) != nil {
		return true
	}
	if item.Type == "steeringUserMessage" {
		item.Content = item.Input
	}
	for _, content := range item.Content {
		if content.Type == "localImage" || content.Type == "image" {
			return true
		}
		if content.Type == "text" && (strings.Contains(content.Text, "# Files mentioned by the user:") || strings.Contains(content.Text, "](")) {
			return true
		}
	}
	return false
}
