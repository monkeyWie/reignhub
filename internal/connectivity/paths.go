// Package connectivity ranks verified end-to-end connections to one device.
package connectivity

import (
	"context"
	"errors"
	"strings"
	"sync"
	"time"
)

type PathOps[T comparable] struct {
	Name   func(T) string
	Probe  func(context.Context, T) error
	Select func(context.Context, T, uint64) error
	Closed func(T) bool
	Close  func(T)
}
type pathSample struct {
	latency           time.Duration
	failures, samples int
	name              string
}
type PathPool[T comparable] struct {
	ctx           context.Context
	cancel        context.CancelFunc
	ops           PathOps[T]
	mu, selection sync.Mutex
	paths         map[T]pathSample
	active        T
	generation    uint64
	sealed        bool
	used          time.Time
	ready         chan struct{}
	first         sync.Once
	wake          chan struct{}
}

func NewPathPool[T comparable](parent context.Context, ops PathOps[T]) *PathPool[T] {
	ctx, cancel := context.WithCancel(context.WithoutCancel(parent))
	p := &PathPool[T]{ctx: ctx, cancel: cancel, ops: ops, paths: map[T]pathSample{}, ready: make(chan struct{}), wake: make(chan struct{}, 1)}
	go p.monitor()
	return p
}
func (p *PathPool[T]) Context() context.Context { return p.ctx }
func (p *PathPool[T]) Ready() <-chan struct{}   { return p.ready }
func (p *PathPool[T]) Active() T                { p.mu.Lock(); defer p.mu.Unlock(); return p.active }
func (p *PathPool[T]) Use() T {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.used = time.Now()
	return p.active
}

// Seal records the completion of the initial dial race. A late candidate can
// still rescue the connection until every outstanding setup has finished.
func (p *PathPool[T]) Seal() { p.mu.Lock(); p.sealed = true; p.mu.Unlock(); p.Check() }
func (p *PathPool[T]) Check() {
	select {
	case p.wake <- struct{}{}:
	default:
	}
}
func (p *PathPool[T]) Add(ctx context.Context, c T, latency time.Duration) {
	p.mu.Lock()
	if p.ctx.Err() != nil {
		p.mu.Unlock()
		p.ops.Close(c)
		return
	}
	p.paths[c] = pathSample{name: p.ops.Name(c), latency: latency, samples: 1}
	p.mu.Unlock()
	p.choose(ctx)
	p.Check()
}
func pathClass(name string) int {
	if strings.HasPrefix(name, "P2P") {
		return 2
	}
	if strings.HasPrefix(name, "Relay") {
		return 1
	}
	return 0
}
func (s pathSample) timeout() time.Duration {
	return min(StageTimeout, max(ResumeTimeout, 3*s.latency))
}

func (s pathSample) prefers(old pathSample) bool {
	rank, prior := pathClass(s.name), pathClass(old.name)
	if rank == 0 || s.failures >= 2 {
		return false
	}
	if prior == 0 || old.failures >= 2 {
		return true
	}
	if rank != prior {
		return rank > prior
	}
	return s.samples >= 2 && s.latency > 0 && old.latency-s.latency > max(12*time.Millisecond, old.latency/5)
}
func (p *PathPool[T]) choose(ctx context.Context) {
	p.selection.Lock()
	defer p.selection.Unlock()
	for ctx.Err() == nil && p.ctx.Err() == nil {
		p.mu.Lock()
		previous := p.active
		best := previous
		current := p.paths[previous]
		sample := current
		for c, s := range p.paths {
			// Apply the switching margin against the active path once. Ranking
			// two eligible standbys must not depend on map iteration order.
			if c == previous || !s.prefers(current) {
				continue
			}
			if best == previous || pathClass(s.name) > pathClass(sample.name) ||
				pathClass(s.name) == pathClass(sample.name) && s.latency < sample.latency {
				best, sample = c, s
			}
		}
		p.mu.Unlock()
		if best == previous {
			return
		}
		p.generation++
		selectCtx, cancel := context.WithTimeout(ctx, sample.timeout())
		err := p.ops.Select(selectCtx, best, p.generation)
		cancel()
		p.mu.Lock()
		if p.ctx.Err() != nil {
			p.mu.Unlock()
			return
		}
		if err == nil {
			p.active = best
			p.first.Do(func() { close(p.ready) })
			p.mu.Unlock()
			return
		}
		// Try the next verified peer even when the fastest admission is rejected.
		sample = p.paths[best]
		sample.failures = 2
		p.paths[best] = sample
		p.mu.Unlock()
	}
}

// Calls remain on their originating peer. Standbys remain available to finish
// outstanding replies, and fresh samples follow use rather than waking idle relays.
func (p *PathPool[T]) monitor() {
	tick := time.NewTicker(time.Second)
	defer tick.Stop()
	var last time.Time
	for {
		forced := false
		select {
		case <-p.ctx.Done():
			return
		case <-p.wake:
			forced = true
		case <-tick.C:
		}
		p.mu.Lock()
		peers := make([]T, 0, len(p.paths))
		changed := false
		repeat := false
		for c, s := range p.paths {
			if p.ops.Closed != nil && p.ops.Closed(c) {
				delete(p.paths, c)
				if c == p.active {
					var zero T
					p.active = zero
					changed = true
				}
				continue
			}
			name := p.ops.Name(c)
			if s.name != name {
				changed = true
				s.name = name
				s.samples = 0
				s.latency = 0
				p.paths[c] = s
			}
			if name != "Disconnected" {
				peers = append(peers, c)
				repeat = repeat || s.failures == 1 || s.samples == 0
			} else {
				s.name = name
				p.paths[c] = s
			}
		}
		need := forced || changed || repeat || len(peers) > 1 && (last.IsZero() || p.used.After(last) && time.Since(last) > 30*time.Second)
		retire := p.sealed && len(p.paths) == 0
		p.mu.Unlock()
		if retire {
			p.Close()
			return
		}
		if !need {
			continue
		}
		if changed {
			p.choose(p.ctx)
		}
		last = time.Now()
		var probes sync.WaitGroup
		for _, c := range peers {
			probes.Go(func() {
				before := time.Now()
				p.mu.Lock()
				budget := p.paths[c].timeout()
				p.mu.Unlock()
				ctx, cancel := context.WithTimeout(p.ctx, budget)
				err := p.ops.Probe(ctx, c)
				cancel()
				p.mu.Lock()
				s, ok := p.paths[c]
				if ok {
					s.name = p.ops.Name(c)
					if err == nil {
						elapsed := time.Since(before)
						if s.latency == 0 {
							s.latency = elapsed
						} else {
							s.latency = (s.latency + elapsed) / 2
						}
						s.samples++
						s.failures = 0
					} else {
						s.failures++
					}
					p.paths[c] = s
				}
				p.mu.Unlock()
			})
		}
		probes.Wait()
		p.choose(p.ctx)
	}
}
func (p *PathPool[T]) Close() {
	p.cancel()
	p.mu.Lock()
	peers := p.paths
	p.paths = nil
	var zero T
	p.active = zero
	p.mu.Unlock()
	for c := range peers {
		p.ops.Close(c)
	}
}

var ErrNoRoute = errors.New("设备未确认可用路径")
