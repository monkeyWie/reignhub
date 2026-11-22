package transport

import (
	"context"
	"sync"
)

type Priority uint8

const (
	Urgent Priority = iota
	Normal
	Bulk
)

type priorityKey struct{}
type probeKey struct{}

func IsProbe(method string) bool            { return method == "network" || method == "peer.select" }
func ProbeContext(ctx context.Context) bool { v, _ := ctx.Value(probeKey{}).(bool); return v }

func WithMethod(ctx context.Context, method string) context.Context {
	if IsProbe(method) {
		ctx = context.WithValue(ctx, probeKey{}, true)
	}
	p := Urgent
	switch method {
	case "transcript", "attachment.read", "attachment.write", "terminal.read", "terminal.attach", "terminal.open", "terminal.openProfile":
		p = Bulk
	case "providers", "sessions", "sync", "models":
		p = Normal
	}
	return context.WithValue(ctx, priorityKey{}, p)
}
func PriorityFor(ctx context.Context, size int) Priority {
	if p, ok := ctx.Value(priorityKey{}).(Priority); ok {
		return p
	}
	if size > 16<<10 {
		return Bulk
	}
	return Urgent
}

type frameJob struct {
	ctx   context.Context
	write func() error
	done  chan error
}

// Sender has at most one outstanding frame per caller. Queues are bounded;
// urgent writes pass bulk transfers, while a bulk frame is serviced after at
// most eight other frames so sustained live output cannot starve history.
type Sender struct {
	once   sync.Once
	queues [3]chan *frameJob
}

func (s *Sender) Send(ctx, lifetime context.Context, p Priority, write func() error) error {
	s.once.Do(func() {
		for i := range s.queues {
			s.queues[i] = make(chan *frameJob, 64)
		}
		go s.run(lifetime)
	})
	j := &frameJob{ctx: ctx, write: write, done: make(chan error, 1)}
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-lifetime.Done():
		return lifetime.Err()
	case s.queues[p] <- j:
	}
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-lifetime.Done():
		return lifetime.Err()
	case err := <-j.done:
		return err
	}
}
func (s *Sender) run(ctx context.Context) {
	high, other := 0, 0
	for {
		var job *frameJob
		var picked Priority
		take := func(p Priority) bool {
			select {
			case job = <-s.queues[p]:
				picked = p
				return true
			default:
				return false
			}
		}
		if high < 8 {
			take(Urgent)
		}
		if job == nil && other >= 8 {
			take(Bulk)
		}
		if job == nil {
			take(Normal)
		}
		if job == nil {
			take(Urgent)
		}
		if job == nil {
			take(Bulk)
		}
		if job == nil {
			select {
			case <-ctx.Done():
				return
			case job = <-s.queues[Urgent]:
				picked = Urgent
			case job = <-s.queues[Normal]:
				picked = Normal
			case job = <-s.queues[Bulk]:
				picked = Bulk
			}
		}
		if ctx.Err() != nil {
			return
		}
		if job.ctx.Err() != nil {
			job.done <- job.ctx.Err()
			continue
		}
		if picked == Urgent {
			high++
		} else {
			high = 0
		}
		if picked == Bulk {
			other = 0
		} else {
			other++
		}
		job.done <- job.write()
	}
}
