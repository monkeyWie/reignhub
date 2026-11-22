package transport

import (
	"sync"
	"time"
)

type trafficBucket struct {
	second         int64
	sent, received int64
}

// Meter counts authenticated RPC frames, not NIC bytes, retransmissions or
// packet loss. All samples come from existing traffic; no timers or probes.
type Meter struct {
	mu                                                             sync.Mutex
	started, firstTraffic, lastReceive, lastProgress, lastResponse time.Time
	sent, received                                                 int64
	pending                                                        int
	buckets                                                        [5]trafficBucket
	response                                                       time.Duration
	probe                                                          time.Duration
}

func (m *Meter) Connected() { m.mu.Lock(); m.started = time.Now(); m.mu.Unlock() }

func (m *Meter) traffic(n int, receive bool) {
	m.mu.Lock()
	defer m.mu.Unlock()
	now := time.Now()
	if m.firstTraffic.IsZero() {
		m.firstTraffic = now
	}
	second := now.Unix()
	b := &m.buckets[second%int64(len(m.buckets))]
	if b.second != second {
		*b = trafficBucket{second: second}
	}
	if receive {
		m.received += int64(n)
		b.received += int64(n)
		m.lastReceive = now
	} else {
		m.sent += int64(n)
		b.sent += int64(n)
	}
}
func (m *Meter) Sent(n int)     { m.traffic(n, false) }
func (m *Meter) Received(n int) { m.traffic(n, true) }
func (m *Meter) Progress()      { m.mu.Lock(); m.lastProgress = time.Now(); m.mu.Unlock() }

// Watch waits for a change by design, so its latency is not a network sample.
// Only successful requests update latency. It includes host processing and
// queueing and must never be labelled network RTT.
func (m *Meter) Begin(method string) func(error) {
	if method == "watch" {
		return func(error) {}
	}
	started := time.Now()
	m.mu.Lock()
	m.pending++
	m.mu.Unlock()
	return func(err error) {
		m.mu.Lock()
		defer m.mu.Unlock()
		m.pending--
		if err == nil {
			m.response = time.Since(started)
			if method == "network" {
				m.probe = m.response
			}
			m.lastResponse = time.Now()
		}
	}
}
func (m *Meter) Snapshot() map[string]any {
	m.mu.Lock()
	defer m.mu.Unlock()
	now := time.Now()
	var sent, received int64
	for _, b := range m.buckets {
		if now.Unix()-b.second >= 0 && now.Unix()-b.second < 5 {
			sent += b.sent
			received += b.received
		}
	}
	age := func(at time.Time) int64 {
		if at.IsZero() {
			return -1
		}
		return max(0, now.Sub(at).Milliseconds())
	}
	span := 5.0
	if !m.firstTraffic.IsZero() {
		span = min(span, max(.001, now.Sub(m.firstTraffic).Seconds()))
	}
	result := map[string]any{"sentBytes": m.sent, "receivedBytes": m.received, "uploadBps": float64(sent) / span, "downloadBps": float64(received) / span, "pendingRequests": m.pending, "lastReceiveMs": age(m.lastReceive), "lastProgressMs": age(m.lastProgress), "lastResponseMs": age(m.lastResponse), "connectedMs": age(m.started)}
	if !m.lastResponse.IsZero() {
		result["responseMs"] = m.response.Milliseconds()
	}
	return result
}

// ProbeDuration measures a completed, lightweight end-to-end network request.
func (m *Meter) ProbeDuration() time.Duration { m.mu.Lock(); defer m.mu.Unlock(); return m.probe }
