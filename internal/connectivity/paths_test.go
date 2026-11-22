package connectivity

import (
	"context"
	"errors"
	"sync/atomic"
	"testing"
	"time"
)

func TestPathPoolRanksLatencyAndRecoversFromTimeouts(t *testing.T) {
	type peer struct {
		name  string
		delay time.Duration
		fail  atomic.Bool
	}
	var generations atomic.Uint64
	pool := NewPathPool(t.Context(), PathOps[*peer]{
		Name: func(p *peer) string { return p.name },
		Probe: func(ctx context.Context, p *peer) error {
			if p.fail.Load() {
				return context.DeadlineExceeded
			}
			select {
			case <-ctx.Done():
				return ctx.Err()
			case <-time.After(p.delay):
				return nil
			}
		},
		Select: func(ctx context.Context, p *peer, g uint64) error {
			if p.fail.Load() || g <= generations.Swap(g) {
				return errors.New("selection rejected")
			}
			return nil
		},
		Close: func(*peer) {},
	})
	defer pool.Close()
	slow := &peer{name: "Relay · WebSocket", delay: 80 * time.Millisecond}
	node := &peer{name: "Relay · WSS", delay: time.Millisecond}
	direct := &peer{name: "P2P · WebRTC", delay: 10 * time.Millisecond}
	expect := func(want *peer) {
		t.Helper()
		deadline := time.Now().Add(4 * time.Second)
		for pool.Active() != want {
			if time.Now().After(deadline) {
				t.Fatalf("selected %p, want %p", pool.Active(), want)
			}
			time.Sleep(time.Millisecond)
		}
	}
	pool.Add(t.Context(), slow, slow.delay)
	expect(slow)
	pool.Add(t.Context(), node, node.delay)
	expect(node)
	pool.Add(t.Context(), direct, direct.delay)
	expect(direct)
	direct.fail.Store(true)
	pool.Check()
	expect(node)
	direct.fail.Store(false)
	pool.Check()
	expect(direct)
}
