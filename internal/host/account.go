package host

import (
	"context"

	"github.com/monkeyWie/reignhub/internal/device"
)

type accountPeer struct {
	Client  string `json:"client"`
	Public  string `json:"public"`
	Binding string `json:"binding"`
	Ready   bool   `json:"ready"`
	key     string
}

// The authenticated presence reply is authoritative. Never persist account keys
// as manual pairings or restore them before the first successful reconciliation.
func (m *Manager) syncAccountPeers(ctx context.Context, i device.Identity, revision uint64, peers []accountPeer) {
	m.mu.Lock()
	if revision != m.accountRevision || i.ID != m.state.Identity.ID {
		m.mu.Unlock()
		return
	}
	old := m.accountKeys
	next := make(map[string]accountPeer, len(peers))
	var ack []string
	for _, p := range peers {
		if prev, ok := old[p.Client]; ok && prev.Binding == p.Binding && prev.Public == p.Public {
			p.key = prev.key
		} else {
			var err error
			p.key, err = device.AccountKey(i.Private, p.Public, p.Binding)
			if err != nil {
				continue
			}
		}
		next[p.Client] = p
		if !p.Ready {
			ack = append(ack, p.Binding)
		}
	}
	m.accountKeys = next
	network := m.network
	m.mu.Unlock()
	for id, prev := range old {
		if next[id].key != prev.key {
			network.DisconnectDevice(id)
		}
	}
	for len(ack) > 0 {
		n := min(len(ack), 64)
		if device.Post(ctx, i.Service, i.Token, "/api/account/access/ack", map[string]any{"bindings": ack[:n]}, nil) != nil {
			return
		}
		ack = ack[n:]
	}
}

func (m *Manager) removeAccountPeer(client, binding string) {
	m.mu.Lock()
	m.accountRevision++
	if a, ok := m.accountKeys[client]; !ok || a.Binding != binding {
		m.mu.Unlock()
		return
	}
	delete(m.accountKeys, client)
	network := m.network
	m.mu.Unlock()
	network.DisconnectDevice(client)
}
