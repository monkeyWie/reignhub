// Package host owns local device authorization and Agent lifecycle, outside the UI.
package host

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"os"
	"reflect"
	"sort"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/monkeyWie/reignhub/internal/agents"
	"github.com/monkeyWie/reignhub/internal/config"
	"github.com/monkeyWie/reignhub/internal/connectivity"
	"github.com/monkeyWie/reignhub/internal/control"
	"github.com/monkeyWie/reignhub/internal/device"
	"github.com/monkeyWie/reignhub/internal/deviceservice"
	"github.com/monkeyWie/reignhub/internal/nativepath"
	"github.com/monkeyWie/reignhub/internal/provider"
	"github.com/monkeyWie/reignhub/internal/relay"
	"github.com/monkeyWie/reignhub/internal/remote"
	"github.com/monkeyWie/reignhub/pkg/appdb"
	"golang.org/x/crypto/bcrypt"
)

type Grant struct {
	Pairing      string            `json:"pairing,omitempty"`
	Revoked      bool              `json:"revoked,omitempty"`
	Info         device.ClientInfo `json:"info,omitempty"`
	Client       string            `json:"client"`
	Name         string            `json:"name"`
	Public       string            `json:"public"`
	Key          string            `json:"key"`
	Expires      int64             `json:"expires,omitempty"`
	AuthorizedAt int64             `json:"authorizedAt,omitempty"`
}
type saved struct {
	Alternates   []*relay.Enrollment `json:"alternates,omitempty"`
	Policy       connectivity.Policy `json:"policy,omitempty"`
	Node         *relay.Enrollment   `json:"node,omitempty"`
	Port         string              `json:"port,omitempty"`
	Identity     device.Identity     `json:"identity"`
	PasswordHash string              `json:"passwordHash,omitempty"`
	Grants       map[string]Grant    `json:"grants"`
}
type Intent struct {
	Info     device.ClientInfo `json:"info,omitempty"`
	Password string            `json:"password"`
}
type Manager struct {
	accountRevision uint64
	accountKeys     map[string]accountPeer
	mu              sync.Mutex
	activation      sync.Mutex
	configMu        sync.Mutex
	cfg             config.Config
	state           saved
	pending         map[string]deviceservice.Pair
	intents         map[string]Intent
	status          string
	network         *remote.HostStatus
	d               *control.Dispatcher
	ice             func(config.Config) remote.Options
	path            string
	attempts        map[string][]time.Time
	agentAttempts   map[string]context.CancelFunc
	agentWake       map[string]chan struct{}
}

func New(c config.Config, d *control.Dispatcher, ice func(config.Config) remote.Options) *Manager {
	m := &Manager{cfg: c, d: d, ice: ice, pending: map[string]deviceservice.Pair{}, intents: map[string]Intent{}, attempts: map[string][]time.Time{}, agentAttempts: map[string]context.CancelFunc{}, agentWake: map[string]chan struct{}{}, status: "尚未配置设备服务"}
	d.ProviderEnabled = m.agentEnabled
	d.RemoteTerminalAllowed = func() bool { m.mu.Lock(); defer m.mu.Unlock(); return m.cfg.RemoteTerminalEnabled }
	for id := range d.Providers {
		m.agentWake[id] = make(chan struct{}, 1)
	}
	return m
}
func (m *Manager) save() error { return saveIdentity(m.path, m.state) }
func saveIdentity(path string, s saved) error {
	db, err := appdb.Open(path)
	if err != nil {
		return err
	}
	defer db.Close()
	s.Grants = nil
	return db.SaveIdentity(s.Identity.Service, "host", s)
}
func (m *Manager) Network() control.NetworkStatus {
	m.mu.Lock()
	n := m.network
	m.mu.Unlock()
	out := n.Snapshot()
	return out
}

// Each integration owns its retry loop and setup budget. Neither network
// registration nor a slow Agent can hold up another integration.
func (m *Manager) autoConnect(ctx context.Context) {
	var workers sync.WaitGroup
	for id, p := range m.d.Providers {
		connector, ok := p.(provider.AutoConnector)
		if !ok {
			continue
		}
		workers.Add(1)
		go func() {
			defer workers.Done()
			for ctx.Err() == nil {
				attempt, cancel := context.WithTimeout(ctx, 30*time.Second)
				m.mu.Lock()
				enabled := m.cfg.AgentIsEnabled(id)
				if enabled {
					m.agentAttempts[id] = cancel
				}
				m.mu.Unlock()
				if enabled {
					_ = connector.AutoConnect(attempt)
				}
				cancel()
				m.mu.Lock()
				delete(m.agentAttempts, id)
				m.mu.Unlock()
				timer := time.NewTimer(5 * time.Second)
				select {
				case <-ctx.Done():
					timer.Stop()
					return
				case <-m.agentWake[id]:
					timer.Stop()
				case <-timer.C:
				}
			}
		}()
	}
	workers.Wait()
}

func (m *Manager) agentEnabled(id string) bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.cfg.AgentIsEnabled(id)
}

// Disabling blocks new access and attachment; it does not terminate a user's
// App or interrupt tasks already accepted by an owned runtime.
func (m *Manager) setAgentEnabled(id string, enabled bool) error {
	if m.d.Providers[id] == nil {
		return fmt.Errorf("未知 Agent")
	}
	m.configMu.Lock()
	defer m.configMu.Unlock()
	m.mu.Lock()
	previous := m.cfg.AgentEnabled
	next := make(map[string]bool, len(previous)+1)
	for key, value := range previous {
		next[key] = value
	}
	next[id] = enabled
	config := m.cfg
	config.AgentEnabled = next
	m.mu.Unlock()
	if err := config.Save(); err != nil {
		return err
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	m.cfg.AgentEnabled = next
	if !enabled {
		if cancel := m.agentAttempts[id]; cancel != nil {
			cancel()
		}
	}
	select {
	case m.agentWake[id] <- struct{}{}:
	default:
	}
	return nil
}
func (m *Manager) Run(ctx context.Context) {
	done := make(chan struct{})
	go func() { defer close(done); m.autoConnect(ctx) }()
	defer func() { <-done }()
	var cancel context.CancelFunc
	var background sync.WaitGroup
	defer background.Wait()
	defer func() {
		if cancel != nil {
			cancel()
		}
	}()
	current := ""
	tick := time.NewTicker(2 * time.Second)
	defer tick.Stop()
	for {
		m.mu.Lock()
		c := m.cfg
		m.mu.Unlock()
		signature := c.ServiceURL
		if c.ServiceURL != "" && signature != current {
			if cancel != nil {
				cancel()
				background.Wait()
				cancel = nil
			}
			current = ""
			m.mu.Lock()
			m.network = nil
			m.state = saved{Grants: map[string]Grant{}}
			m.path = ""
			m.status = "正在登记设备"
			m.mu.Unlock()
			e := m.register(ctx, c)
			var policy connectivity.Policy
			if e == nil {
				m.mu.Lock()
				i := m.state.Identity
				m.status = "正在获取服务授权配置"
				policy = m.state.Policy
				m.mu.Unlock()
				if policy.GrantPublicKey == "" {
					policy, e = device.HostNetwork(ctx, i.Service)
					if e == nil {
						m.mu.Lock()
						m.state.Policy = policy
						e = m.save()
						m.mu.Unlock()
					}
				}
			}
			if e != nil {
				n := remote.NewHostStatus(c.ServiceURL)
				n.ReportError(e)
				m.mu.Lock()
				m.status = e.Error()
				m.network = n
				m.mu.Unlock()
			} else {
				current = signature
				m.mu.Lock()
				i := m.state.Identity
				n := remote.NewHostStatus(i.Service)
				m.network = n
				m.status = "在线"
				m.mu.Unlock()
				life, stop := context.WithCancel(ctx)
				cancel = stop
				opts := m.ice(c)
				opts.GrantRequired = true
				opts.GrantPublicKey = policy.GrantPublicKey
				opts.ServiceID = policy.ServiceID
				opts.ICEServers = policy.Servers()
				opts.TCPSTUN = policy.TCPSTUN
				opts.DirectAddresses = func() []nativepath.Endpoint { return m.directAddresses(i) }
				opts.ResolveKey = m.key
				opts.AccountRemoved = m.removeAccountPeer
				opts.OnClientInfo = m.updateClientInfo
				m.startRoutes(life, i, opts, n, &background)
				background.Go(func() {
					remote.RunHostObserved(life, remote.Pairing{Relay: "ws" + strings.TrimPrefix(i.Service, "http"), Host: i.ID, Key: device.Random(), RelayToken: i.Token}, opts, m.d, n)
				})
			}
		}
		if current != "" {
			m.poll(ctx)
		}
		select {
		case <-ctx.Done():
			return
		case <-tick.C:
		}
	}
}
func (m *Manager) register(ctx context.Context, c config.Config) error {
	service, e := device.ServiceURL(c.ServiceURL)
	if e != nil {
		return e
	}
	path := appdb.Path(c.DataDir)
	s := saved{Grants: map[string]Grant{}}
	db, e := appdb.Open(path)
	if e != nil {
		return e
	}
	defer db.Close()
	e = db.LoadIdentity(service, "host", &s)
	if e == nil {
		rows, err := db.QueryContext(ctx, "SELECT client,value FROM host_grants WHERE service=?", service)
		if err != nil {
			return err
		}
		s.Grants = map[string]Grant{}
		for rows.Next() {
			var id string
			var b []byte
			var g Grant
			if err = rows.Scan(&id, &b); err == nil {
				err = json.Unmarshal(b, &g)
			}
			if err != nil {
				rows.Close()
				return err
			}
			s.Grants[id] = g
		}
		err = errors.Join(rows.Err(), rows.Close())
		if err != nil {
			return err
		}
	}
	if e != nil && !errors.Is(e, sql.ErrNoRows) {
		return e
	}
	if s.Identity.Token == "" {
		name := c.DeviceName
		if name == "" {
			name, _ = os.Hostname()
		}
		s.Identity, e = device.NewIdentity(name)
		if e != nil {
			return e
		}
		s.Identity, e = device.Register(ctx, service, s.Identity, true)
		if e != nil {
			return e
		}
		if e = saveIdentity(path, s); e != nil {
			return e
		}
	}
	if s.Grants == nil {
		s.Grants = map[string]Grant{}
	}
	m.mu.Lock()
	m.state = s
	m.accountKeys = nil
	m.path = path
	m.pending = map[string]deviceservice.Pair{}
	m.intents = map[string]Intent{}
	m.mu.Unlock()
	return nil
}
func (m *Manager) key(id string) (string, bool) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if a, ok := m.accountKeys[id]; ok {
		return a.key, true
	}
	g, ok := m.state.Grants[id]
	return g.Key, ok && !g.Revoked && (g.Expires == 0 || g.Expires > time.Now().Unix())
}
func (m *Manager) poll(ctx context.Context) {
	m.mu.Lock()
	rev := m.accountRevision
	i := m.state.Identity
	m.mu.Unlock()
	if i.ID == "" {
		return
	}
	var presence struct{ AccountPeers []accountPeer }
	if e := device.Post(ctx, i.Service, i.Token, "/p2p/devices/presence", map[string]any{"name": i.Name, "accountAccess": true}, &presence); e != nil {
		m.mu.Lock()
		m.status = e.Error()
		m.mu.Unlock()
		return
	}
	m.syncAccountPeers(ctx, i, rev, presence.AccountPeers)
	m.mu.Lock()
	var revoked []Grant
	for _, g := range m.state.Grants {
		if g.Revoked && g.Pairing != "" {
			revoked = append(revoked, g)
		}
	}
	m.mu.Unlock()
	for _, g := range revoked {
		if m.notifyRevocation(ctx, i, g) != nil {
			break
		}
	}
	var pairs []deviceservice.Pair
	if device.Post(ctx, i.Service, i.Token, "/p2p/pairings/pending", map[string]any{}, &pairs) != nil {
		return
	}
	active := map[string]bool{}
	for _, p := range pairs {
		active[p.ID] = true
		m.mu.Lock()
		_, seen := m.pending[p.ID]
		m.mu.Unlock()
		if seen {
			continue
		}
		var in Intent
		if e := device.Open(i.Private, p.Public, "reignhub/pair/request/"+p.ID, p.Envelope, &in); e != nil {
			continue
		}
		m.mu.Lock()
		m.pending[p.ID] = p
		m.intents[p.ID] = in
		password := m.state.PasswordHash
		attempts := m.attempts[p.Client]
		now := time.Now()
		recent := []time.Time{}
		for _, t := range attempts {
			if now.Sub(t) < time.Minute {
				recent = append(recent, t)
			}
		}
		m.attempts[p.Client] = recent
		m.mu.Unlock()
		if in.Password == "" {
			m.mu.Lock()
			uiPath := m.cfg.UIPath
			m.mu.Unlock()
			// Bring the local authorization window forward, including when it was closed.
			showAuthorizationWindow(ctx, uiPath)
		}
		if in.Password != "" {
			ok := len(recent) < 5 && password != "" && bcrypt.CompareHashAndPassword([]byte(password), []byte(in.Password)) == nil
			m.mu.Lock()
			m.attempts[p.Client] = append(recent, now)
			m.mu.Unlock()
			_ = m.respond(ctx, p.ID, ok)
		}
	}
	m.mu.Lock()
	m.status = "在线"
	for id := range m.pending {
		if !active[id] {
			delete(m.pending, id)
			delete(m.intents, id)
		}
	}
	m.mu.Unlock()
}
func (m *Manager) respond(ctx context.Context, id string, allow bool) error {
	respondedAt := time.Now()
	m.mu.Lock()
	p, ok := m.pending[id]
	intent := m.intents[id]
	i := m.state.Identity
	m.mu.Unlock()
	if !ok || p.Expires < respondedAt.Unix() {
		return fmt.Errorf("授权请求已过期")
	}
	var reply string
	if allow {
		info := intent.Info.Normalize()
		if info.Name == "" {
			info.Name = p.Name
		}
		g := Grant{Pairing: p.ID, Client: p.Client, Name: info.Name, Info: info, Public: p.Public, Key: device.Random(), AuthorizedAt: respondedAt.UnixMilli()}
		var e error
		direct := m.directAddresses(i)
		reply, e = device.Seal(i.Private, p.Public, "reignhub/pair/reply/"+p.ID, map[string]any{"key": g.Key, "host": i.ID, "public": i.Public, "direct": direct})
		if e != nil {
			return e
		}
		m.mu.Lock()
		old, had := m.state.Grants[g.Client]
		if had && !old.Revoked && old.Pairing == g.Pairing && old.AuthorizedAt > 0 {
			g.AuthorizedAt = old.AuthorizedAt
		}
		m.state.Grants[g.Client] = g
		e = m.saveGrant(g.Client, &g)
		if e != nil {
			if had {
				m.state.Grants[g.Client] = old
			} else {
				delete(m.state.Grants, g.Client)
			}
		}
		m.mu.Unlock()
		if e != nil {
			return e
		}
	}
	if e := device.Post(ctx, i.Service, i.Token, "/p2p/pairings/respond", map[string]any{"id": id, "allow": allow, "reply": reply}, nil); e != nil {
		return e
	}
	m.mu.Lock()
	delete(m.pending, id)
	delete(m.intents, id)
	m.mu.Unlock()
	return nil
}
func (m *Manager) Local(ctx context.Context, r control.Request) (any, error) {
	var in struct {
		ServiceURL, Name, Password, ID, Client, Provider, Email, OTP string
		Allow                                                        bool
		ClearPassword                                                bool
		Enabled                                                      *bool
	}
	if len(r.Params) > 0 {
		if e := json.Unmarshal(r.Params, &in); e != nil {
			return nil, e
		}
	}
	switch r.Method {
	case "host.terminal.enabled":
		if in.Enabled == nil {
			return nil, fmt.Errorf("缺少远程终端开关状态")
		}
		m.configMu.Lock()
		defer m.configMu.Unlock()
		m.mu.Lock()
		cfg := m.cfg
		m.mu.Unlock()
		cfg.RemoteTerminalEnabled = *in.Enabled
		if err := cfg.Save(); err != nil {
			return nil, err
		}
		m.mu.Lock()
		m.cfg.RemoteTerminalEnabled = *in.Enabled
		m.mu.Unlock()
		return nil, nil
	case "host.agent.enabled":
		if in.Enabled == nil {
			return nil, fmt.Errorf("缺少 Agent 开关状态")
		}
		return nil, m.setAgentEnabled(in.Provider, *in.Enabled)
	case "host.agents":
		m.mu.Lock()
		defer m.mu.Unlock()
		return agents.Catalog(m.cfg.Agents), nil
	case "host.agent.save":
		var edit struct {
			ID    string       `json:"id"`
			Agent config.Agent `json:"agent"`
		}
		if err := json.Unmarshal(r.Params, &edit); err != nil {
			return nil, err
		}
		if err := agents.Validate(edit.ID, edit.Agent); err != nil {
			return nil, err
		}
		m.configMu.Lock()
		defer m.configMu.Unlock()
		m.mu.Lock()
		defer m.mu.Unlock()
		if m.cfg.Agents == nil {
			m.cfg.Agents = map[string]config.Agent{}
		}
		m.cfg.Agents[edit.ID] = edit.Agent
		return map[string]bool{"restartRequired": true}, m.cfg.Save()
	case "host.account.start", "host.account.poll", "host.account.password", "host.account.send", "host.account.verify", "host.account.status", "host.account.devices":
		m.mu.Lock()
		i := m.state.Identity
		m.mu.Unlock()
		var params device.AccountRequest
		if len(r.Params) > 0 {
			if err := json.Unmarshal(r.Params, &params); err != nil {
				return nil, err
			}
		}
		if params.ControllerToken != "" && i.Service != in.ServiceURL {
			return nil, fmt.Errorf("设备服务地址已变更，请重新登录")
		}
		return device.AccountAction(ctx, i.Service, i.Token, strings.TrimPrefix(r.Method, "host.account."), params)
	case "host.info":
		m.mu.Lock()
		defer m.mu.Unlock()
		i := m.state.Identity
		pending := []map[string]any{}
		for _, p := range m.pending {
			info := m.intents[p.ID].Info.Normalize()
			if info.Name == "" {
				info.Name = p.Name
			}
			pending = append(pending, map[string]any{"id": p.ID, "name": info.Name, "platform": info.Platform, "kind": info.Kind, "model": info.Model, "fingerprint": device.Fingerprint(p.Public), "expires": p.Expires})
		}
		grants := []map[string]any{}
		for _, g := range m.state.Grants {
			if g.Revoked {
				continue
			}
			grants = append(grants, map[string]any{"client": g.Client, "name": g.Name, "platform": g.Info.Platform, "kind": g.Info.Kind, "model": g.Info.Model, "fingerprint": device.Fingerprint(g.Public), "expires": g.Expires, "authorizedAt": g.AuthorizedAt})
		}
		sort.Slice(pending, func(a, b int) bool { return pending[a]["id"].(string) < pending[b]["id"].(string) })
		sort.Slice(grants, func(a, b int) bool {
			left, right := grants[a]["authorizedAt"].(int64), grants[b]["authorizedAt"].(int64)
			if left != right {
				return left > right
			}
			return grants[a]["client"].(string) < grants[b]["client"].(string)
		})
		status := m.status
		network := m.network.Snapshot()
		if m.network != nil {
			switch network.State {
			case "connected":
				status = "在线 · 可连接"
			case "connecting":
				status = "正在连接服务"
			case "error":
				status = "连接异常 · 正在重试"
			default:
				status = "离线"
			}
		}
		return map[string]any{"deviceId": i.ID, "connectionId": i.ConnectionID, "public": i.Public, "name": i.Name, "serviceUrl": m.cfg.ServiceURL, "status": status, "network": network, "passwordEnabled": m.state.PasswordHash != "", "remoteTerminalEnabled": m.cfg.RemoteTerminalEnabled, "pending": pending, "grants": grants, "qr": map[string]any{"version": 2, "service": i.Service, "connectionId": i.ConnectionID, "public": i.Public}}, nil
	case "host.configure":
		url, e := device.ServiceURL(in.ServiceURL)
		if e != nil {
			return nil, e
		}
		m.configMu.Lock()
		defer m.configMu.Unlock()
		m.mu.Lock()
		defer m.mu.Unlock()
		m.cfg.ServiceURL = url
		m.cfg.DeviceName = in.Name
		if m.path != "" && m.state.Identity.Service == url && in.Name != "" {
			m.state.Identity.Name = in.Name
			if e := m.save(); e != nil {
				return nil, e
			}
		}
		return nil, m.cfg.Save()
	case "host.password":
		m.mu.Lock()
		defer m.mu.Unlock()
		if m.path == "" {
			return nil, fmt.Errorf("请先连接设备服务")
		}
		if in.ClearPassword {
			m.state.PasswordHash = ""
		} else {
			if len(in.Password) < 8 || len(in.Password) > 72 {
				return nil, fmt.Errorf("密码应为 8–72 字节")
			}
			h, e := bcrypt.GenerateFromPassword([]byte(in.Password), bcrypt.DefaultCost)
			if e != nil {
				return nil, e
			}
			m.state.PasswordHash = string(h)
		}
		return nil, m.save()
	case "host.respond":
		return nil, m.respond(ctx, in.ID, in.Allow)
	case "host.revoke":
		m.mu.Lock()
		i := m.state.Identity
		network := m.network
		prior, ok := m.state.Grants[in.Client]
		if !ok {
			m.mu.Unlock()
			return nil, nil
		}
		revoked := Grant{Client: in.Client, Pairing: prior.Pairing, Revoked: true}
		e := m.saveGrant(in.Client, &revoked)
		if e == nil {
			m.state.Grants[in.Client] = revoked
		}
		m.mu.Unlock()
		if e != nil {
			return nil, e
		}
		network.DisconnectDevice(in.Client)
		// The local deletion is authoritative. Retry the generation-bound notification
		// while polling if the control is currently unreachable.
		_ = m.notifyRevocation(ctx, i, revoked)
		return nil, nil
	case "host.enable", "host.activate":
		p := m.d.Providers[in.Provider]
		if p == nil {
			return nil, fmt.Errorf("未知 Agent")
		}
		if r.Method == "host.activate" && in.Provider == "codex-app" {
			if !m.agentEnabled(in.Provider) {
				return nil, fmt.Errorf("此 Agent 已在本机关闭")
			}
			return nil, p.ConnectDesktop(ctx, r.Expected, r.ConfirmRestart)
		}
		m.activation.Lock()
		defer m.activation.Unlock()
		if e := p.Start(ctx); e != nil {
			return nil, e
		}
		if err := m.setAgentEnabled(in.Provider, true); err != nil {
			return nil, err
		}
		if r.Method == "host.activate" {
			return nil, p.ConnectDesktop(ctx, r.Expected, r.ConfirmRestart)
		}
		return nil, nil
	default:
		return nil, fmt.Errorf("unknown host method")
	}
}

func (m *Manager) updateClientInfo(id string, info device.ClientInfo) error {
	info = info.Normalize()
	if info.Name == "" {
		return fmt.Errorf("缺少设备名称")
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	old, ok := m.state.Grants[id]
	if !ok || old.Revoked || (old.Expires != 0 && old.Expires <= time.Now().Unix()) {
		return fmt.Errorf("设备未获授权")
	}
	if old.Info == info && old.Name == info.Name {
		return nil
	}
	updated := old
	updated.Info, updated.Name = info, info.Name
	m.state.Grants[id] = updated
	if err := m.saveGrant(id, &updated); err != nil {
		m.state.Grants[id] = old
		return err
	}
	return nil
}

func (m *Manager) saveGrant(id string, g *Grant) error {
	db, err := appdb.Open(m.path)
	if err != nil {
		return err
	}
	defer db.Close()
	if g == nil {
		_, err = db.Exec("DELETE FROM host_grants WHERE service=? AND client=?", m.state.Identity.Service, id)
		return err
	}
	data, err := json.Marshal(g)
	if err != nil {
		return err
	}
	_, err = db.Exec("INSERT INTO host_grants VALUES(?,?,?) ON CONFLICT(service,client) DO UPDATE SET value=excluded.value", m.state.Identity.Service, id, data)
	return err
}

func (m *Manager) startRoutes(ctx context.Context, i device.Identity, opts remote.Options, status *remote.HostStatus, background *sync.WaitGroup) {
	m.mu.Lock()
	port := m.state.Port
	cachedRoutes := relay.Routes{Node: m.state.Node, Alternates: m.state.Alternates}
	opts.Credentials = &relay.RouteCredentials{}
	opts.Credentials.Store(&cachedRoutes)
	m.mu.Unlock()
	background.Go(func() {
		// Choose a stable service port outside the dynamic client port range.
		// A restarted host can otherwise lose its saved port to an outbound dial.
		seed := sha256.Sum256([]byte(i.ID))
		for attempt := 0; ctx.Err() == nil; attempt++ {
			selected := port
			if selected == "" {
				selected = strconv.Itoa(20000 + (int(binary.BigEndian.Uint16(seed[:2]))+attempt)%10000)
			}
			listener, err := remote.ListenPaired(ctx, net.JoinHostPort("", selected), nativepath.Key([]byte(i.Private), "host-listener"), i.ID, opts, m.d, status)
			if err == nil {
				m.mu.Lock()
				m.state.Port = selected
				err = m.save()
				m.mu.Unlock()
				if err == nil {
					<-ctx.Done()
					listener.Close()
					return
				}
				listener.Close()
			}
			status.ReportError(fmt.Errorf("原生直连监听失败: %w", err))
			if port == "" && attempt < 8 && errors.Is(err, syscall.EADDRINUSE) {
				continue
			}
			select {
			case <-ctx.Done():
				return
			case <-time.After(time.Second):
			}
		}
	})
	background.Go(func() {
		type registration struct {
			node *relay.Enrollment
			stop context.CancelFunc
		}
		running := map[string]registration{}
		defer func() {
			for _, r := range running {
				r.stop()
			}
		}()
		reconcile := func(routes relay.Routes) {
			for pin, r := range running {
				found := false
				for _, n := range routes.Nodes() {
					if r.node.SameNode(n) {
						found = true
						break
					}
				}
				if !found {
					r.stop()
					delete(running, pin)
				}
			}
			for _, n := range routes.Nodes() {
				if _, ok := running[n.Pin]; ok {
					continue
				}
				life, stop := context.WithCancel(ctx)
				running[n.Pin] = registration{n, stop}
				background.Go(func() { remote.RegisterPaired(life, n, i.ID, opts, m.d, status) })
			}
		}
		reconcile(cachedRoutes)
		for ctx.Err() == nil {
			var routes relay.Routes
			e := device.Post(ctx, i.Service, i.Token, "/p2p/routes", map[string]string{"host": i.ID}, &routes)
			if e == nil && ctx.Err() == nil {
				opts.Credentials.Store(&routes)
				reconcile(routes)
				if !reflect.DeepEqual(cachedRoutes.Node, routes.Node) || !reflect.DeepEqual(cachedRoutes.Alternates, routes.Alternates) {
					m.mu.Lock()
					m.state.Node, m.state.Alternates = routes.Node, routes.Alternates
					e = m.save()
					m.mu.Unlock()
					if e == nil {
						cachedRoutes = routes
					} else {
						status.ReportError(e)
					}
				}
			}
			delay := time.Minute
			if e != nil {
				delay = 5 * time.Second
			}
			select {
			case <-ctx.Done():
				return
			case <-time.After(delay):
			}
		}
	})
}

func (m *Manager) directAddresses(i device.Identity) []nativepath.Endpoint {
	m.mu.Lock()
	port := m.state.Port
	owner := m.state.Identity.ID
	m.mu.Unlock()
	if port == "" || owner != i.ID {
		return nil
	}
	return remote.DirectAddresses(port, nativepath.Pin(nativepath.Key([]byte(i.Private), "host-listener")))
}

func (m *Manager) notifyRevocation(ctx context.Context, i device.Identity, g Grant) error {
	if err := device.Post(ctx, i.Service, i.Token, "/p2p/pairings/revoke", map[string]string{"client": g.Client, "pairing": g.Pairing}, nil); err != nil {
		return err
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	current := m.state.Grants[g.Client]
	if m.state.Identity.ID != i.ID || !current.Revoked || current.Pairing != g.Pairing {
		return nil
	}
	if err := m.saveGrant(g.Client, nil); err != nil {
		return err
	}
	delete(m.state.Grants, g.Client)
	return nil
}
