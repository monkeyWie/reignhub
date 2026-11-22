package remote

import (
	"sort"
	"sync"

	"github.com/monkeyWie/reignhub/internal/control"
)

// HostStatus reports authenticated live links, not a persistent device registry.
type HostStatus struct {
	mu                   sync.Mutex
	state, relay, detail string
	peers                map[string]*wire
}

func NewHostStatus(url string) *HostStatus {
	return &HostStatus{state: "connecting", relay: url, peers: make(map[string]*wire)}
}
func (s *HostStatus) ReportError(err error) { s.set("error", err.Error()) }
func (s *HostStatus) set(state, detail string) {
	if s == nil {
		return
	}
	s.mu.Lock()
	s.state, s.detail = state, detail
	if state != "connected" {
		for id, w := range s.peers {
			if w.ctx.Err() != nil {
				delete(s.peers, id)
			}
		}
	}
	s.mu.Unlock()
}
func (s *HostStatus) remove(id string, w *wire) {
	if s == nil {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.peers[id] == w {
		delete(s.peers, id)
	}
}
func (s *HostStatus) Snapshot() control.NetworkStatus {
	if s == nil {
		return control.NetworkStatus{State: "disabled", Peers: []control.PeerStatus{}}
	}
	s.mu.Lock()
	out := control.NetworkStatus{State: s.state, Relay: s.relay, Detail: s.detail, Peers: []control.PeerStatus{}}
	peers := make(map[string]*wire, len(s.peers))
	for id, w := range s.peers {
		peers[id] = w
	}
	s.mu.Unlock()
	selected := map[[2]string]*wire{}
	for id, w := range peers {
		w.mu.Lock()
		authenticated := w.crypto != nil && w.ctx.Err() == nil
		logical := w.logicalID
		w.mu.Unlock()
		if !authenticated {
			continue
		}
		if logical == "" {
			out.Peers = append(out.Peers, control.PeerStatus{ID: id, Path: w.path()})
			continue
		}
		group := [2]string{w.device, logical}
		if selected[group] == nil || selected[group].selection.Load() < w.selection.Load() {
			selected[group] = w
		}
	}
	for _, w := range selected {
		out.Peers = append(out.Peers, control.PeerStatus{ID: w.peer, Path: w.path()})
	}
	sort.Slice(out.Peers, func(i, j int) bool { return out.Peers[i].ID < out.Peers[j].ID })
	return out
}

// DisconnectAll immediately closes data channels; the relay link stays available.
func (s *HostStatus) DisconnectAll() {
	if s == nil {
		return
	}
	s.mu.Lock()
	peers := make([]*wire, 0, len(s.peers))
	for _, w := range s.peers {
		peers = append(peers, w)
	}
	s.mu.Unlock()
	for _, w := range peers {
		w.close()
	}
}

// DisconnectDevice leaves other paired controllers connected.
func (s *HostStatus) DisconnectDevice(device string) {
	if s == nil {
		return
	}
	s.mu.Lock()
	var peers []*wire
	for _, w := range s.peers {
		if w.device == device {
			peers = append(peers, w)
		}
	}
	s.mu.Unlock()
	for _, w := range peers {
		w.close()
	}
}

func (s *HostStatus) reserve(id string, w *wire) bool {
	if s == nil {
		return true
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if len(s.peers) >= 64 || s.peers[id] != nil {
		return false
	}
	s.peers[id] = w
	return true
}

// Selections are encrypted on the candidate itself. Replies to outstanding
// requests always retain their original peer, including after a newer selection.
func (s *HostStatus) selectPath(w *wire, generation uint64) bool {
	if generation == 0 || w.logicalID == "" {
		return false
	}
	if s == nil {
		w.selection.Store(generation)
		return true
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.peers[w.peer] != w {
		return false
	}
	for _, other := range s.peers {
		other.mu.Lock()
		same := other.device == w.device && other.logicalID == w.logicalID
		other.mu.Unlock()
		if same && other.selection.Load() >= generation {
			return false
		}
	}
	w.selection.Store(generation)
	return true
}
