// Package clienthub is the shared native client used by desktop and mobile bridges.
package clienthub

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/monkeyWie/reignhub/internal/assets"
	"github.com/monkeyWie/reignhub/internal/connectivity"
	"github.com/monkeyWie/reignhub/internal/control"
	"github.com/monkeyWie/reignhub/internal/device"
	"github.com/monkeyWie/reignhub/internal/deviceservice"
	"github.com/monkeyWie/reignhub/internal/nativepath"
	"github.com/monkeyWie/reignhub/internal/relay"
	"github.com/monkeyWie/reignhub/internal/remote"
	"github.com/monkeyWie/reignhub/internal/store"
	"github.com/monkeyWie/reignhub/pkg/appdb"
	"github.com/monkeyWie/reignhub/pkg/persistence"
)

type networkAddresses struct {
	Network   string                `json:"network"`
	Addresses []nativepath.Endpoint `json:"addresses"`
}
type Saved struct {
	AccountBinding    string                `json:"accountBinding,omitempty"`
	Networks          []networkAddresses    `json:"networks,omitempty"`
	Direct            []nativepath.Endpoint `json:"direct,omitempty"`
	Routes            relay.Routes          `json:"routes,omitempty"`
	Platform          string                `json:"platform,omitempty"`
	ID                string                `json:"id"`
	Service           string                `json:"service"`
	ConnectionID      string                `json:"connectionId"`
	Name              string                `json:"name"`
	Host              string                `json:"host"`
	Public            string                `json:"public"`
	Key               string                `json:"key"`
	Expires           int64                 `json:"expires"`
	RelayOnly         bool                  `json:"relayOnly,omitempty"`
	HasConnected      bool                  `json:"hasConnected,omitempty"`
	ReconnectDisabled bool                  `json:"reconnectDisabled,omitempty"`
}
type Hub struct {
	Assets      *assets.Cache
	mu          sync.Mutex
	identityMu  sync.Mutex
	DB          *store.DB
	connections map[string]*remote.Client
	busy        map[string]bool
	generation  map[string]uint64
	local       control.Caller
	localName   string
}

// SetLocal attaches an application-owned runtime. Closing a controller or
// disconnecting a remote peer must not stop local Agent execution.
func (h *Hub) SetLocal(c control.Caller, name string) {
	h.mu.Lock()
	h.local, h.localName = c, name
	h.mu.Unlock()
}

func (h *Hub) caller(id string) control.Caller {
	h.mu.Lock()
	defer h.mu.Unlock()
	if id == control.LocalTarget {
		return h.local
	}
	if c := h.connections[id]; c != nil {
		return c
	}
	return nil
}

func New(dir string) (*Hub, error) {
	db, e := store.Open(appdb.Path(dir))
	if e != nil {
		return nil, e
	}
	return &Hub{DB: db, Assets: &assets.Cache{Database: appdb.Path(dir), Root: filepath.Join(dir, "asset-cache")}, connections: map[string]*remote.Client{}, busy: map[string]bool{}, generation: map[string]uint64{}}, nil
}

// Close is called after the application's outstanding requests are canceled.
func (h *Hub) Close() error {
	h.mu.Lock()
	connections := h.connections
	h.connections = map[string]*remote.Client{}
	h.mu.Unlock()
	for _, c := range connections {
		_ = c.Close()
	}
	return h.DB.Close()
}
func (h *Hub) identity(ctx context.Context, service, name string) (device.Identity, error) {
	h.identityMu.Lock()
	defer h.identityMu.Unlock()
	var i device.Identity
	if info := device.ClientInfoFrom(ctx); info.Name != "" {
		name = info.Name
	}

	if e := h.DB.LoadIdentity(service, "controller", &i); e == nil {
		if name != "" && name != i.Name {
			i.Name = name
		}
		info := device.CurrentClientInfo(ctx, i.Info)
		info.Name = i.Name
		if info != i.Info {
			i.Info = info
			if e := h.DB.SaveIdentity(service, "controller", i); e != nil {
				return i, e
			}
		}
		return i, nil
	} else if !errors.Is(e, sql.ErrNoRows) {
		return i, e
	}
	i, e := device.NewIdentity(name)
	if e != nil {
		return i, e
	}
	i.Info = device.CurrentClientInfo(ctx, device.ClientInfo{Name: name})
	i, e = device.Register(ctx, service, i, false)
	if e != nil {
		return i, e
	}
	return i, h.DB.SaveIdentity(service, "controller", i)
}
func (h *Hub) List() ([]map[string]any, error) {
	keys, e := h.DB.Environments()
	if e != nil {
		return nil, e
	}
	r := []map[string]any{}
	h.mu.Lock()
	local, name := h.local, h.localName
	h.mu.Unlock()
	if local != nil {
		r = append(r, map[string]any{"id": control.LocalTarget, "name": name, "local": true, "platform": runtime.GOOS, "connected": true, "path": "In-process"})
	}
	for _, k := range keys {
		var s Saved
		if e = h.DB.LoadEnvironment(k, &s); e != nil {
			return nil, e
		}
		path := "离线"
		h.mu.Lock()
		c := h.connections[s.ID]
		h.mu.Unlock()
		if c != nil {
			path = c.Path()
		}
		connected := c != nil && path != "Disconnected"
		r = append(r, map[string]any{"id": s.ID, "service": s.Service, "connectionId": s.ConnectionID, "name": s.Name, "platform": s.Platform, "host": s.Host, "public": s.Public, "path": path, "connected": connected, "network": func() any {
			if c != nil {
				return c.Metrics()
			}
			return nil
		}(), "independentRoutes": s.Routes.Grant != "" && (s.Routes.Node != nil || !s.RelayOnly && (len(s.Direct) > 0 || len(s.Networks) > 0)), "autoReconnect": s.HasConnected && !s.ReconnectDisabled && s.Key != "" && (s.Expires == 0 || s.Expires > time.Now().Unix())})
	}
	return r, nil
}

// Presence reads saved hosts without initiating any peer connection.
func (h *Hub) Presence(ctx context.Context) ([]device.DeviceStatus, error) {
	keys, err := h.DB.Environments()
	if err != nil {
		return nil, err
	}
	targets := []device.PresenceTarget{}
	for _, key := range keys {
		var saved Saved
		if err = h.DB.LoadEnvironment(key, &saved); err != nil {
			return nil, err
		}
		var identity device.Identity
		_ = h.DB.LoadIdentity(saved.Service, "controller", &identity)
		targets = append(targets, device.PresenceTarget{ID: saved.ID, Service: saved.Service, Host: saved.Host, Token: identity.Token})
	}
	return device.Presence(ctx, targets), ctx.Err()
}

func (h *Hub) Connect(ctx context.Context, service, code, public, password, name string, relayOnly bool) (Saved, error) {
	origin, err := device.ServiceURL(service)
	if err != nil {
		return Saved{}, err
	}
	keys, err := h.DB.Environments()
	if err != nil {
		return Saved{}, err
	}
	for _, id := range keys {
		var saved Saved
		if err := h.DB.LoadEnvironment(id, &saved); err != nil {
			return Saved{}, err
		}
		if saved.Service == origin && saved.ConnectionID == code && saved.AccountBinding == "" && saved.Key != "" && (saved.Expires == 0 || saved.Expires > time.Now().Unix()) {
			return h.connect(ctx, origin, code, public, password, name, relayOnly, false, &saved)
		}
	}
	return h.connect(ctx, origin, code, public, password, name, relayOnly, false, nil)
}

// Reconnect only reuses an existing valid grant. It never requests new approval
// or replays a user command after a network failure.
func (h *Hub) Reconnect(ctx context.Context, id string) (Saved, error) {
	var s Saved
	if err := h.DB.LoadEnvironment(id, &s); err != nil {
		return s, err
	}
	if s.ReconnectDisabled || s.Key == "" || (s.Expires > 0 && s.Expires <= time.Now().Unix()) {
		return s, fmt.Errorf("自动重连已停止或授权已过期，请手动连接")
	}
	h.mu.Lock()
	old := h.connections[id]
	h.mu.Unlock()
	if old != nil {
		connectivity.Report(ctx, "resume", "正在恢复原有连接", connectivity.StageTimeout)
		probe, stop := context.WithTimeout(ctx, connectivity.ResumeTimeout)
		err := old.Resume(probe)
		stop()
		if err == nil {
			var identity device.Identity
			_ = h.DB.LoadIdentity(s.Service, "controller", &identity)
			publishClientInfo(old, device.CurrentClientInfo(ctx, identity.Info))
			return s, nil
		}
		if ctx.Err() != nil {
			return s, ctx.Err()
		}
		_ = old.Close()
	}
	connectivity.Report(ctx, "resume", "正在复用设备配对与连接配置", connectivity.StageTimeout)
	result, err := h.connect(ctx, s.Service, s.ConnectionID, s.Public, "", "", s.RelayOnly, true, &s)
	if err == nil || ctx.Err() != nil {
		return result, err
	}
	connectivity.Note(ctx, "resume-fallback", "retry", "快速恢复未成功，重新查找设备", err.Error(), "warning")
	return h.connect(ctx, s.Service, s.ConnectionID, s.Public, "", "", s.RelayOnly, true, nil)
}

func (h *Hub) connect(ctx context.Context, service, code, public, password, name string, relayOnly, reconnectOnly bool, cached *Saved) (Saved, error) {
	ctx, cancelAttempt := context.WithCancel(ctx)
	defer cancelAttempt()
	service, e := device.ServiceURL(service)
	if e != nil {
		return Saved{}, e
	}
	connectivity.Report(ctx, "service", "正在连接服务并验证本机身份", connectivity.StageTimeout)
	i, e := h.identity(ctx, service, name)
	if e != nil {
		return Saved{}, e
	}
	var r struct {
		Device         deviceservice.Device
		AccountBinding string
		Online         bool
	}
	if cached != nil {
		r.Device = deviceservice.Device{ID: cached.Host, ConnectionID: cached.ConnectionID, Public: cached.Public, Name: cached.Name}
		r.Online = true // The new signal lease and encrypted handshake verify liveness and authorization.
	} else {
		connectivity.Report(ctx, "resolve", "正在查找设备并检查在线状态", connectivity.StageTimeout)
		if e = device.Post(ctx, service, i.Token, "/p2p/devices/resolve", map[string]any{"connectionId": code, "accountAccess": true}, &r); e != nil {
			return Saved{}, e
		}
	}
	if !r.Online {
		return Saved{}, fmt.Errorf("被控设备离线，请在电脑上打开 ReignHub，并确认两端使用相同的服务地址")
	}
	d := r.Device
	if public != "" && public != d.Public {
		return Saved{}, fmt.Errorf("二维码公钥不匹配")
	}
	id, e := h.DB.Route(ctx, service+"|"+d.ID)
	if e != nil {
		return Saved{}, e
	}
	h.mu.Lock()
	if h.busy[id] {
		h.mu.Unlock()
		return Saved{}, fmt.Errorf("正在连接该设备")
	}
	h.busy[id] = true
	generation := h.generation[id]
	h.mu.Unlock()
	defer func() { h.mu.Lock(); delete(h.busy, id); h.mu.Unlock() }()
	var s Saved
	_ = h.DB.LoadEnvironment(id, &s)
	if reconnectOnly && (s.ReconnectDisabled || s.Key == "" || (s.Expires > 0 && s.Expires <= time.Now().Unix())) {
		return s, fmt.Errorf("自动重连已停止或授权已过期，请手动连接")
	}
	if s.Public != "" && s.Public != d.Public {
		return Saved{}, fmt.Errorf("设备身份已变化，需要删除旧配对再确认")
	}
	if cached == nil && r.AccountBinding != "" {
		key, err := device.AccountKey(i.Private, d.Public, r.AccountBinding)
		if err != nil {
			return s, err
		}
		if err = device.WaitAccountHost(ctx, service, i.Token, r.AccountBinding); err != nil {
			return s, err
		}
		if s.Key != key {
			s = Saved{ID: id, Service: service, ConnectionID: d.ConnectionID, Name: d.Name, Host: d.ID, Public: d.Public, Key: key, AccountBinding: r.AccountBinding}
			if err = h.DB.SaveEnvironment(id, s); err != nil {
				return s, err
			}
		}
	} else if cached == nil && s.AccountBinding != "" {
		if reconnectOnly {
			return s, fmt.Errorf("账号设备关联已移除")
		}
		s.Key = ""
	}
	if s.Key == "" || (s.Expires > 0 && s.Expires <= time.Now().Unix()) {
		connectivity.Report(ctx, "request", "正在向被控电脑请求授权", connectivity.StageTimeout)
		reqID := device.Random()[:32]
		envelope, e := device.Seal(i.Private, d.Public, "reignhub/pair/request/"+reqID, map[string]any{"password": password, "info": i.Info})
		if e != nil {
			return s, e
		}
		if e = device.Post(ctx, service, i.Token, "/p2p/pairings/request", map[string]any{"host": d.ID, "id": reqID, "envelope": envelope}, nil); e != nil {
			return s, e
		}
		message := "请在被控电脑的 ReignHub 中允许此次连接"
		if password != "" {
			message = "正在等待被控电脑验证访问密码"
		}
		connectivity.Report(ctx, "approval", message, connectivity.ApprovalTimeout)
		approvalCtx, stopApproval := context.WithTimeout(ctx, connectivity.ApprovalTimeout)
		defer stopApproval()
		tick := time.NewTicker(time.Second)
		defer tick.Stop()
		for {
			var result deviceservice.Pair
			if e = device.Post(approvalCtx, service, i.Token, "/p2p/pairings/result", map[string]string{"id": reqID}, &result); e != nil {
				return s, e
			}
			if result.State == "denied" {
				return s, fmt.Errorf("主机拒绝授权或密码错误")
			}
			if result.State == "approved" {
				var reply struct {
					Key, Host, Public string
					Expires           int64
					Direct            []nativepath.Endpoint
				}
				if e = device.Open(i.Private, d.Public, "reignhub/pair/reply/"+reqID, result.Reply, &reply); e != nil {
					return s, e
				}
				if reply.Host != d.ID || reply.Public != d.Public {
					return s, fmt.Errorf("无效主机授权")
				}
				s = Saved{ID: id, Service: service, ConnectionID: d.ConnectionID, Name: d.Name, Host: d.ID, Public: d.Public, Key: reply.Key, Expires: reply.Expires, Direct: reply.Direct}
				if e = h.DB.SaveEnvironment(id, s); e != nil {
					return s, e
				}
				break
			}
			select {
			case <-ctx.Done():
				return s, ctx.Err()
			case <-approvalCtx.Done():
				return s, fmt.Errorf("等待主机授权超时，请在被控电脑上允许连接后重试")
			case <-tick.C:
			}
		}
	}
	connectCtx, cancel := context.WithCancel(ctx)
	defer cancel()
	opts := remote.Options{RelayOnly: relayOnly}
	if !relayOnly {
		connectivity.Report(ctx, "network", "授权已通过，正在获取直连配置", connectivity.StageTimeout)
		var cache struct {
			Policy connectivity.Policy
			At     time.Time
		}
		_ = h.DB.Get("network:"+service, &cache)
		policy := cache.Policy
		if cached == nil || (cache.At.IsZero() && s.Routes.Grant == "") {
			policy = device.Network(connectCtx, service)
			cache.Policy, cache.At = policy, time.Now()
			if err := h.DB.Put("network:"+service, cache); err != nil {
				return s, err
			}
		}
		opts.PublicSTUN, opts.SelfSTUN = policy.PublicSTUN, policy.SelfSTUN
		opts.ICEServers = policy.Servers()
		opts.TCPSTUN = policy.TCPSTUN
	}
	pairKey := s.Key
	network := remote.NetworkKey()
	direct := s.Direct
	for _, hints := range s.Networks {
		if hints.Network == network {
			direct = uniqueEndpoints(append(append([]nativepath.Endpoint{}, hints.Addresses...), direct...))
			break
		}
	}
	opts.RememberAddresses = func(endpoints []nativepath.Endpoint) {
		if len(endpoints) == 0 {
			return
		}
		h.mu.Lock()
		defer h.mu.Unlock()
		var current Saved
		if h.generation[id] != generation || h.DB.LoadEnvironment(id, &current) != nil || current.Key != pairKey {
			return
		}
		networks := []networkAddresses{{Network: network, Addresses: uniqueEndpoints(endpoints)}}
		for _, prior := range current.Networks {
			if prior.Network != network && len(networks) < 3 {
				networks = append(networks, prior)
			}
		}
		current.Networks = networks
		_ = h.DB.SaveEnvironment(id, current)
	}
	opts.FetchRoutes = func(refresh context.Context) (relay.Routes, error) {
		var routes relay.Routes
		err := device.Post(refresh, service, i.Token, "/p2p/routes", map[string]string{"host": d.ID}, &routes)
		if err != nil {
			return routes, err
		}
		h.mu.Lock()
		defer h.mu.Unlock()
		if refresh.Err() != nil || h.generation[id] != generation {
			return relay.Routes{}, context.Canceled
		}
		var current Saved
		if err = h.DB.LoadEnvironment(id, &current); err != nil {
			return relay.Routes{}, err
		}
		if current.Key != pairKey {
			return relay.Routes{}, context.Canceled
		}
		current.Routes = routes
		return routes, h.DB.SaveEnvironment(id, current)
	}
	c, e := remote.ConnectRoutes(connectCtx, remote.Pairing{Relay: "ws" + strings.TrimPrefix(service, "http"), Host: d.ID, Key: s.Key, RelayToken: i.Token}, opts, i.ID, s.Routes, direct)
	if e != nil {
		return s, e
	}
	if ctx.Err() != nil {
		_ = c.Close()
		return s, ctx.Err()
	}
	h.mu.Lock()
	if h.generation[id] != generation {
		h.mu.Unlock()
		_ = c.Close()
		return s, fmt.Errorf("连接已取消")
	}
	if platform := device.NormalizePlatform(c.HostPlatform()); platform != "" {
		s.Platform = platform
	}
	var stored Saved
	if h.DB.LoadEnvironment(id, &stored) == nil && stored.Key == s.Key {
		s.Routes = stored.Routes
		s.Networks = stored.Networks
	}
	s.Name, s.ConnectionID = d.Name, d.ConnectionID
	s.RelayOnly, s.ReconnectDisabled, s.HasConnected = relayOnly, false, true
	if err := h.DB.SaveEnvironment(id, s); err != nil {
		h.mu.Unlock()
		_ = c.Close()
		return s, err
	}
	old := h.connections[id]
	h.connections[id] = c
	h.mu.Unlock()
	if old != nil {
		old.Close()
	}
	publishClientInfo(c, i.Info)
	connectivity.Note(ctx, "connected", "connected", "连接成功", c.Path(), "success")
	return s, nil
}
func (h *Hub) Disconnect(id string) error {
	if id == control.LocalTarget {
		return fmt.Errorf("本机环境不能断开；请在 Agent 设置中管理本机接入")
	}
	h.mu.Lock()
	h.generation[id]++
	c := h.connections[id]
	delete(h.connections, id)
	defer h.mu.Unlock()
	if c != nil {
		defer c.Close()
	}
	var s Saved
	if h.DB.LoadEnvironment(id, &s) == nil {
		s.ReconnectDisabled = true
		if err := h.DB.SaveEnvironment(id, s); err != nil {
			return err
		}
	}
	return nil
}
func (h *Hub) Forget(id string) error {
	if id == control.LocalTarget {
		return fmt.Errorf("本机环境不能移除")
	}
	if err := h.DB.ForgetHistory(context.Background(), id); err != nil {
		return err
	}
	if err := h.Assets.Clear(id); err != nil {
		return err
	}
	h.Disconnect(id)

	return nil
}
func (h *Hub) Call(ctx context.Context, id string, r control.Request) (any, error) {
	scope := persistence.Session{Env: id, Provider: r.Provider, ID: r.Session}
	kind := persistence.ReadKind(r.Method)
	c := h.caller(id)
	if c == nil || c.Path() == "Disconnected" {
		if !r.Fresh && control.ReadOnly(r.Method) {
			if r.Method == "transcript" && r.Cursor == "" {
				page, e := h.DB.ReadPage(ctx, persistence.Session{Env: id, Provider: r.Provider, ID: r.Session}, persistence.PageQuery{})
				v := page.View()
				if e != nil {
					return nil, e
				}
				if v != nil {
					return v, nil
				}
			}
			if r.Method == "sessions" && r.Cursor == "" {
				rows, e := h.DB.ListSessions(ctx, persistence.DirectoryKey{Env: id, Provider: r.Provider})
				if e != nil {
					return nil, e
				}
				if len(rows) > 0 {
					return map[string]any{"sessions": rows}, nil
				}
			}
			v, e := h.DB.LoadList(ctx, scope, kind)
			if e != nil || v != nil {
				return v, e
			}
		}
		return nil, fmt.Errorf("设备离线：可以查看缓存，写操作需重新连接")
	}
	var out any
	if e := c.Call(ctx, r, &out); e != nil {
		return nil, e
	}
	if r.Method == "sessions" {
		k := persistence.DirectoryKey{Env: id, Provider: r.Provider}
		page, _ := out.(map[string]any)
		if err := h.DB.WriteDirectory(ctx, k, persistence.DirectoryHead{}, persistence.DirectoryBatch{Preview: true, Upserts: persistence.Objects(page["sessions"])}); err != nil {
			return nil, err
		}
	} else if kind.Table() != "" {
		if e := h.DB.SaveList(ctx, scope, kind, persistence.Objects(out)); e != nil {
			return nil, e
		}
	}
	return out, nil
}
func (h *Hub) applyDirectory(ctx context.Context, k persistence.DirectoryKey, old persistence.DirectoryHead, d store.Delta) ([]map[string]any, error) {
	batch := persistence.DirectoryBatch{Head: persistence.DirectoryHead{Epoch: d.Epoch, Cursor: d.Cursor}, Reset: d.Reset}
	for _, v := range d.Items {
		var row persistence.Object
		if err := json.Unmarshal(v, &row); err != nil {
			return nil, err
		}
		batch.Upserts = append(batch.Upserts, row)
	}
	for _, c := range d.Changes {
		if c.Deleted {
			batch.Changes = append(batch.Changes, persistence.DirectoryChange{ID: c.ID})
			continue
		}
		var row persistence.Object
		if err := json.Unmarshal(c.Value, &row); err != nil {
			return nil, err
		}
		batch.Changes = append(batch.Changes, persistence.DirectoryChange{ID: c.ID, Value: row})
	}
	if err := h.DB.WriteDirectory(ctx, k, old, batch.Coalesced()); err != nil {
		return nil, err
	}
	return h.DB.ListSessions(ctx, k)
}
func (h *Hub) Sync(ctx context.Context, id, p string) (any, error) {
	k := persistence.DirectoryKey{Env: id, Provider: p}
	head, err := h.DB.DirectoryPosition(ctx, k)
	if err != nil {
		return nil, err
	}
	c := h.caller(id)
	if c == nil || c.Path() == "Disconnected" {
		return nil, fmt.Errorf("设备离线：保留本地会话，连接后重试同步")
	}
	var d store.Delta
	if err = c.Call(ctx, control.Request{Method: "sync", Provider: p, Epoch: head.Epoch, After: head.Cursor}, &d); err != nil {
		return nil, err
	}
	return h.applyDirectory(ctx, k, head, d)
}

// Display metadata must not delay a successful connection or data sync.
func publishClientInfo(c *remote.Client, info device.ClientInfo) {
	if info.Name == "" {
		return
	}
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		params, _ := json.Marshal(info)
		_ = c.Call(ctx, control.Request{Method: "peer.info", Params: params}, nil)
	}()
}

func uniqueEndpoints(in []nativepath.Endpoint) []nativepath.Endpoint {
	out := make([]nativepath.Endpoint, 0, 8)
	for _, e := range in {
		if e.Address == "" || e.Pin == "" || slices.Contains(out, e) {
			continue
		}
		out = append(out, e)
		if len(out) == 8 {
			break
		}
	}
	return out
}
