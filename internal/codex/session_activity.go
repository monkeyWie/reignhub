package codex

import (
	"context"
	"encoding/json"
	"path/filepath"
	"sync"
	"time"

	"github.com/monkeyWie/reignhub/internal/codexipc"
	"github.com/monkeyWie/reignhub/internal/provider"
)

// The private reader's notLoaded status is not the App owner's runtime status.
// Probe only OS-locked threads, follow their existing owner without resuming,
// and return metadata to the controller. No history RPC is issued for the list.
func (p *Provider) enrichSessionActivity(ctx context.Context, sessions []provider.Session) {
	b := p.appBridge()
	if b == nil {
		return
	}
	ctx, cancel := context.WithTimeout(ctx, 2*time.Second)
	defer cancel()
	var wg sync.WaitGroup
	slots := make(chan struct{}, 4)
	for i := range sessions {
		ss := &sessions[i]
		if ss.ID == "" || filepath.Base(ss.ID) != ss.ID || ss.ID == "." || ss.ID == ".." {
			continue
		}
		if b.owns(ss.ID) {
			continue // The private backend owns this thread and its list status.
		}
		locked, err := codexipc.WriterLocked(filepath.Join(b.home, "thread-writer-locks", ss.ID+".lock"))
		if err != nil {
			ss.Status = "unknown"
			continue
		}
		if !locked {
			b.clearOwnerUnreachable(ss.ID)
			ss.CodexOwnerUnreachable = false
			// Owners may release their writer immediately after publishing the
			// final state. Preserve that terminal observation before unsubscribing.
			if raw := b.activityState(ss.ID); len(raw) > 0 {
				last := *ss
				last.Status = "unknown"
				applyIPCActivity(&last, raw)
				if last.Status == "completed" || last.Status == "failed" || last.Status == "interrupted" || last.Status == "idle" {
					*ss = last
				}
			}
			b.releaseDirectoryFollower(ctx, ss.ID)
			continue
		}
		ss.Status = "unknown" // A failed owner read cannot assert idle/completion.
		select {
		case slots <- struct{}{}:
		case <-ctx.Done():
			continue
		}
		wg.Add(1)
		go func() {
			defer wg.Done()
			defer func() { <-slots }()
			raw := b.activityState(ss.ID)
			if len(raw) == 0 {
				owner, err := b.followForRead(ctx, ss.ID, true)
				if err != nil || owner == "" {
					ss.CodexOwnerUnreachable = b.ownerUnreachable(ss.ID)
					return
				}
				raw, err = b.waitState(ctx, ss.ID, owner)
				if err != nil {
					return
				}
			}
			ss.CodexOwnerUnreachable = b.ownerUnreachable(ss.ID)
			applyIPCActivity(ss, raw)
		}()
	}
	wg.Wait()
}

func (b *appIPC) activityState(id string) json.RawMessage {
	b.mu.Lock()
	defer b.mu.Unlock()
	if f := b.followed[id]; f != nil && f.owner != "" && f.err == nil {
		return f.state
	}
	return nil
}

func applyIPCActivity(ss *provider.Session, raw json.RawMessage) {
	state := decodeObject(raw)
	if stringValue(state["id"]) != ss.ID {
		return
	}
	turns, _, err := stateTurns(state)
	if err != nil {
		return
	}
	for _, turn := range turns {
		ss.RecencyAt = max(ss.RecencyAt, provider.ConversationRecency([]provider.Turn{normalizeTurn(rawJSON(turn))}))
	}
	runtime := object(state["threadRuntimeStatus"])
	status := stringValue(runtime["type"])
	ss.ActiveTurnID = ""
	if status == "active" {
		ss.Status = "running"
		if len(array(runtime["activeFlags"])) > 0 || len(array(state["requests"])) > 0 {
			ss.Status = "waiting"
		}
	} else if status == "systemError" {
		ss.Status = "failed"
	} else if len(turns) > 0 {
		last := turns[len(turns)-1]
		ss.Status = stringValue(last["status"])
		if ss.Status == "inProgress" {
			ss.Status = "running"
			if len(array(state["requests"])) > 0 {
				ss.Status = "waiting"
			}
		}
	} else if status == "idle" {
		ss.Status = "idle"
	}
	if provider.SessionBusy(*ss) && len(turns) > 0 {
		ss.ActiveTurnID = stringValue(turns[len(turns)-1]["id"])
	}
	if updated, ok := ipcTimestamp(state["updatedAt"]); ok && updated > ss.UpdatedAt {
		ss.UpdatedAt = updated
	}
}

func (b *appIPC) releaseDirectoryFollower(ctx context.Context, id string) {
	b.mu.Lock()
	f := b.followed[id]
	if f == nil || !f.directory {
		b.mu.Unlock()
		return
	}
	delete(b.followed, id)
	b.signal(f)
	owner := f.owner
	b.mu.Unlock()
	if owner != "" {
		_ = b.broadcast(ctx, "thread-stream-following-changed", map[string]any{"hostId": "local", "conversationId": id, "following": false}, []string{owner})
	}
}
