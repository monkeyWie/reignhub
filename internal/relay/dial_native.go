//go:build !js

package relay

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/monkeyWie/reignhub/internal/nativepath"
)

type admissionCandidate struct {
	link  *nativepath.Link
	proof []byte
	peer  string
	err   error
}

func (e *Enrollment) prepare(ctx context.Context, network, op string, invite Invitation) admissionCandidate {
	if invite.Peer == "" {
		invite.Peer = ID()
	}
	link, err := nativepath.Dial(ctx, e.Endpoint, network)
	if err != nil {
		return admissionCandidate{err: fmt.Errorf("%s dial: %w", network, err)}
	}
	stop := context.AfterFunc(ctx, link.Close)
	proof, err := e.answerChallenge(ctx, link, op, invite)
	stop()
	if err != nil {
		link.Close()
		return admissionCandidate{err: fmt.Errorf("%s challenge: %w", network, err)}
	}
	return admissionCandidate{link: link, peer: invite.Peer, proof: proof}
}

// Dial opens one admitted leg on the enrolled node. The node has one registration
// per host and one acceptance per invitation, so only one prepared candidate at
// a time may send the proof that commits either operation.
func (e *Enrollment) Dial(ctx context.Context, op string, invite Invitation) (*nativepath.Link, string, error) {
	life, cancel := context.WithCancel(ctx)
	ready := make(chan admissionCandidate)
	// A losing dial (including OS name resolution) must not delay a usable leg.
	defer cancel()
	remaining := 0
	for _, network := range []string{"QUIC", "TCP", "WSS"} {
		if e.AddressFor(network) == "" {
			continue
		}
		remaining++
		go func() {
			candidate := e.prepare(life, network, op, invite)
			select {
			case ready <- candidate:
			case <-life.Done():
				if candidate.link != nil {
					candidate.link.Close()
				}
			}
		}()
	}

	var pending []admissionCandidate
	defer func() {
		for _, candidate := range pending {
			candidate.link.Close()
		}
	}()
	var active admissionCandidate
	var confirmed chan error
	var failures error
	var timer *time.Timer
	var fallback <-chan time.Time
	for remaining > 0 || len(pending) > 0 || confirmed != nil {
		if confirmed == nil && len(pending) > 0 {
			active, pending = pending[0], pending[1:]
			confirmed = make(chan error, 1)
			go func(c admissionCandidate, done chan<- error) {
				done <- confirmAdmission(ctx, c.link, c.proof)
			}(active, confirmed)
		}
		// Give a lone path the entire setup budget. Only shorten its wait
		// once another transport has returned a valid node challenge.
		if confirmed != nil && len(pending) > 0 && timer == nil {
			grace := 2 * time.Second
			if op == "connect" {
				// A controller's ready also waits for a host invitation round trip.
				grace = 10 * time.Second
			}
			if deadline, ok := ctx.Deadline(); ok {
				grace = min(grace, time.Until(deadline)/time.Duration(1+len(pending)+remaining))
			}
			timer = time.NewTimer(grace)
			fallback = timer.C
		}
		var err error
		select {
		case candidate := <-ready:
			remaining--
			if candidate.err != nil {
				failures = errors.Join(failures, candidate.err)
			} else {
				pending = append(pending, candidate)
			}
			continue
		case err = <-confirmed:
		case <-fallback:
			// Prefer an acknowledgement already delivered at the timer boundary.
			select {
			case err = <-confirmed:
			default:
				active.link.Close()
				<-confirmed
				err = context.DeadlineExceeded
			}
		case <-ctx.Done():
			if confirmed != nil {
				active.link.Close()
				<-confirmed
			}
		}
		if timer != nil {
			timer.Stop()
			timer, fallback = nil, nil
		}
		if ctx.Err() != nil {
			if active.link != nil {
				active.link.Close()
			}
			return nil, "", ctx.Err()
		}
		if err == nil {
			return active.link, active.peer, nil
		}
		failures = errors.Join(failures, fmt.Errorf("%s admission: %w", active.link.Network, err))
		active.link.Close()
		confirmed = nil
	}
	if failures == nil {
		failures = errors.New("no relay endpoint")
	}
	return nil, "", failures
}
