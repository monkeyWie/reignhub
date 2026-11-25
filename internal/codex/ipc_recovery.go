package codex

import (
	"context"
	"errors"
	"time"

	"github.com/monkeyWie/reignhub/internal/provider"
)

type followerRefresh struct {
	done chan struct{}
	err  error
}

// Repair an explicitly invalidated follower on existing watch/history reads.
// Revision gaps and owner announcements wake those reads through the event log.
// Healthy streams do not periodically refetch state.
func (b *appIPC) refreshFollower(ctx context.Context, id string) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if b.owns(id) {
		return nil
	}
	b.mu.Lock()
	f := b.followed[id]
	if f == nil || f.owner == "" || len(f.state) == 0 {
		b.mu.Unlock()
		return nil
	}
	if pending := f.refresh; pending != nil {
		b.mu.Unlock()
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-pending.done:
			return pending.err
		}
	}
	if f.err == nil && f.refreshErr == nil && f.refreshGeneration == f.refreshedGeneration {
		b.mu.Unlock()
		return nil
	}
	if time.Now().Before(f.refreshAfter) {
		err := f.refreshErr
		b.mu.Unlock()
		return err
	}
	pending := &followerRefresh{done: make(chan struct{})}
	f.refresh = pending
	serial := f.snapshots
	generation := f.refreshGeneration
	b.mu.Unlock()

	attempt, cancel := context.WithTimeout(ctx, 5*time.Second)
	stop := context.AfterFunc(b.ctx, cancel)
	owner, err := b.followForRead(attempt, id, false)
	if err == nil && owner == "" {
		// Definitive discovery found no owner. Retire the old projection so a
		// persisted-history read can recover without trusting stale running state.
		b.mu.Lock()
		if b.followed[id] == f {
			b.invalidateQueue(id, f)
			delete(b.followed, id)
			b.signal(f)
		}
		b.mu.Unlock()
	} else if err == nil {
		err = b.waitFreshState(attempt, id, f, owner, serial)
	}
	stop()
	cancel()
	b.mu.Lock()
	pending.err = err
	f.refresh = nil
	f.refreshErr = err
	if err == nil {
		f.refreshedGeneration = generation
	}
	if err != nil && !errors.Is(err, context.Canceled) {
		f.refreshAfter = time.Now().Add(5 * time.Second)
	} else {
		f.refreshAfter = time.Time{}
	}
	close(pending.done)
	b.mu.Unlock()
	if err == nil {
		// Queues have their own notifications. Reconcile their read-only sources
		// too, even when the owner's conversation revision has not changed.
		b.p.stream().log.Publish(provider.StreamEvent{Kind: "snapshot", SessionID: id})
	}
	return err
}

func (b *appIPC) waitFreshState(ctx context.Context, id string, expected *followedThread, owner string, after uint64) error {
	for {
		if err := ctx.Err(); err != nil {
			return err
		}
		b.mu.Lock()
		f := b.followed[id]
		if f != expected || f.owner != owner {
			b.mu.Unlock()
			return errors.New("Codex 会话持有者已变化")
		}
		fresh, err, changed := f.snapshots > after && len(f.state) > 0, f.err, f.changed
		b.mu.Unlock()
		if fresh && err == nil {
			return nil
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-changed:
		}
	}
}
