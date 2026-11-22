package remote

import "time"

func (w *wire) hasDirectPath() bool {
	w.mu.Lock()
	direct := w.direct
	w.mu.Unlock()
	return w.ctx.Err() == nil && (direct || w.tcpSnapshot() != nil || w.native.direct.Load() != nil)
}

// A signal lease only discovers peers and transports negotiation. Its loss
// does not revoke the already authenticated P2P channel or its request scope.
// Per-request pairing/entitlement checks remain in force on detached peers.
func (w *wire) detachSignal(status *HostStatus) {
	if !w.signalLost.CompareAndSwap(false, true) {
		return
	}
	go func() {
		tick := time.NewTicker(time.Second)
		defer tick.Stop()
		defer w.close()
		defer status.remove(w.peer, w)
		var lost time.Time
		for {
			select {
			case <-w.ctx.Done():
				return
			case <-tick.C:
				if w.hasDirectPath() {
					lost = time.Time{}
					continue
				}
				if lost.IsZero() {
					lost = time.Now()
				}
				if time.Since(lost) >= 20*time.Second {
					return
				}
			}
		}
	}()
}
