package codex

import (
	"context"
	"errors"
	"path/filepath"
	"time"

	"github.com/monkeyWie/reignhub/internal/codexipc"
)

var errOwnerUnreachable = errors.New("暂时无法连接持有此会话的 Codex，发送和实时更新已暂停")

func (b *appIPC) ownerUnreachable(id string) bool {
	b.mu.Lock()
	defer b.mu.Unlock()
	_, unavailable := b.unreachableOwners[id]
	return unavailable
}

func (b *appIPC) clearOwnerUnreachable(id string) {
	b.mu.Lock()
	defer b.mu.Unlock()
	delete(b.unreachableOwners, id)
}

func (b *appIPC) markOwnerUnreachable(id string, started time.Time, conn *codexipc.Client) bool {
	b.mu.Lock()
	defer b.mu.Unlock()
	if conn == nil || conn != b.conn || !conn.Alive() || b.owned[id] != nil && b.backendAlive() {
		return false
	}
	if f := b.followed[id]; f != nil && f.lastOwnerContact.After(started) {
		return false // Discovery or a valid owner snapshot overtook this failed read.
	}
	if b.unreachableOwners == nil {
		b.unreachableOwners = make(map[string]time.Time)
	}
	if _, exists := b.unreachableOwners[id]; !exists && len(b.unreachableOwners) >= 128 {
		oldest := ""
		for key, last := range b.unreachableOwners {
			if oldest == "" || last.Before(b.unreachableOwners[oldest]) {
				oldest = key
			}
		}
		delete(b.unreachableOwners, oldest)
	}
	b.unreachableOwners[id] = time.Now()
	// A previous owner's projection may still be in memory. Future reads
	// must join a fresh follower or use persisted history.
	if f := b.followed[id]; f != nil {
		b.invalidateQueue(id, f)
		delete(b.followed, id)
		b.signal(f)
	}
	return true
}

// Retry only discovery, under the directory or watch budget. A held writer
// and two unsuccessful lookups on a healthy broker identify the affected
// session without issuing any Agent command.
func (b *appIPC) followForRead(ctx context.Context, id string, directory bool) (string, error) {
	if !safeThreadID(id) || b.owns(id) {
		return b.followFor(ctx, id, directory)
	}
	path := filepath.Join(b.home, "thread-writer-locks", id+".lock")
	locked, err := codexipc.WriterLocked(path)
	if !locked {
		if err == nil {
			b.clearOwnerUnreachable(id)
		}
		return b.followFor(ctx, id, directory)
	}
	started, conn := time.Now(), b.client()
	// A directory page has a two-second budget shared by all rows. Leave room
	// for the second lookup even if the broker never answers the first one.
	timeout := 1500 * time.Millisecond
	if directory {
		timeout = 800 * time.Millisecond
	}
	var owner string
	for range 2 {
		attempt, cancel := context.WithTimeout(ctx, timeout)
		owner, err = b.followFor(attempt, id, directory)
		cancel()
		if ctx.Err() != nil {
			return "", ctx.Err()
		}
		var discovery *ownerDiscoveryError
		if owner != "" || err != nil && !errors.As(err, &discovery) {
			return owner, err
		}
	}
	locked, lockErr := codexipc.WriterLocked(path)
	if lockErr == nil && !locked {
		b.clearOwnerUnreachable(id)
	} else if locked && b.markOwnerUnreachable(id, started, conn) {
		return "", errOwnerUnreachable
	}
	return "", err
}
