// Package webapi adapts the unchanged Agent Hub protocol to browser storage.
package webapi

import (
	"context"
	"crypto/ecdh"
	"crypto/rand"

	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"github.com/monkeyWie/reignhub/clients/nsgo/browserclient"
	"github.com/monkeyWie/reignhub/internal/connectivity"
	"github.com/monkeyWie/reignhub/internal/device"
	"github.com/monkeyWie/reignhub/internal/relay"
	"github.com/monkeyWie/reignhub/pkg/composerdraft"
	"github.com/monkeyWie/reignhub/pkg/persistence"
	"strings"
	"sync"
	"time"
)

type Storage interface {
	persistence.HistoryDAO
	persistence.DirectoryDAO
	persistence.ReadDAO
	composerdraft.DAO
	Download(context.Context, string, string, Object) (any, error)
	LoadIdentity(context.Context, string) (Object, error)
	SaveIdentity(context.Context, string, Object) error
	Environments() ([]Object, error)
	SaveEnvironment(context.Context, Object) error
	Route(context.Context, string) (string, error)
	Get(string) (any, error)
	Put(string, any) error
	Delete(string) error
	Keys() ([]string, error)
}
type API struct {
	store                      Storage
	mu, edit, identity, assets sync.Mutex
	clients                    map[string]*browserclient.Client
	busy                       map[string]context.CancelFunc
	generation                 map[string]int
	closed                     bool
}

func New(s Storage) *API {
	return &API{store: s, clients: map[string]*browserclient.Client{}, busy: map[string]context.CancelFunc{}, generation: map[string]int{}}
}
func (*API) CanHost() bool { return false }
func (a *API) Close() {
	a.mu.Lock()
	a.closed = true
	cs := a.clients
	a.clients = map[string]*browserclient.Client{}
	for id, stop := range a.busy {
		stop()
		a.generation[id]++
	}
	a.mu.Unlock()
	for _, c := range cs {
		c.Close()
	}
}
func (a *API) saved() ([]Object, error) { return a.store.Environments() }
func (a *API) list() (any, error) {
	v, e := a.saved()
	if e != nil {
		return nil, e
	}
	out := []Object{}
	a.mu.Lock()
	defer a.mu.Unlock()
	for _, d := range v {
		r := clone(d)
		delete(r, "key")
		delete(r, "routes")
		c := a.clients[str(d, "id")]
		r["path"] = "离线"
		r["connected"] = false
		if c != nil {
			r["path"] = c.Path()
			r["connected"] = c.Connected()
			r["network"] = c.Metrics()
		}
		r["autoReconnect"] = d["reconnectDisabled"] != true && str(d, "key") != "" && (num(d, "expires") == 0 || num(d, "expires") > time.Now().Unix())
		out = append(out, r)
	}
	return out, nil
}
func (a *API) loadIdentity(ctx context.Context, service, name string) (Object, error) {
	a.identity.Lock()
	defer a.identity.Unlock()
	if info := device.ClientInfoFrom(ctx); info.Name != "" {
		name = info.Name
	}
	v, e := a.store.LoadIdentity(ctx, service)
	if e != nil {
		return nil, e
	}
	if v != nil {
		i := clone(obj(v))
		if name != "" && name != str(i, "name") {
			if _, e = post(ctx, service, str(i, "token"), "/p2p/devices/presence", Object{"name": name}); e != nil {
				return nil, e
			}
			i["name"] = name
		}
		var previous device.ClientInfo
		raw, _ := json.Marshal(i["info"])
		_ = json.Unmarshal(raw, &previous)
		info := device.CurrentClientInfo(ctx, previous)
		info.Name = str(i, "name")
		if info != previous {
			i["info"] = info
			if e = a.store.SaveIdentity(ctx, service, i); e != nil {
				return nil, e
			}
		}
		return i, nil
	}
	k, e := ecdh.X25519().GenerateKey(rand.Reader)
	if e != nil {
		return nil, e
	}
	pub := hex.EncodeToString(k.PublicKey().Bytes())
	r, e := post(ctx, service, "", "/p2p/devices/register", Object{"name": name, "public": pub, "isHost": false})
	if e != nil {
		return nil, e
	}
	r["info"] = device.CurrentClientInfo(ctx, device.ClientInfo{Name: name})
	r["private"] = hex.EncodeToString(k.Bytes())
	r["public"] = pub
	e = a.store.SaveIdentity(ctx, service, r)
	return r, e
}
func (a *API) current(id string, g int) bool {
	a.mu.Lock()
	defer a.mu.Unlock()
	return !a.closed && a.generation[id] == g
}
func (a *API) connect(ctx context.Context, p Object) (any, error) {
	service := strings.TrimRight(str(p, "service"), "/")
	name := str(p, "name")
	if name == "" && p["reconnectOnly"] != true {
		name = "Web 浏览器"
	}
	connectivity.Report(ctx, "service", "正在连接服务并验证本机身份", connectivity.StageTimeout)
	var i Object
	var e error
	if p["reconnectOnly"] == true {
		i, e = a.store.LoadIdentity(ctx, service)
	}
	if e == nil && i == nil {
		i, e = a.loadIdentity(ctx, service, name)
	}
	if e != nil {
		return nil, e
	}
	token := str(i, "token")
	connectivity.Note(ctx, "service", "service", "本机身份已验证", service, "success")
	var r Object
	cached := obj(p["cached"])
	if len(cached) > 0 {
		r = Object{"online": true, "device": Object{"id": cached["host"], "public": cached["public"], "name": cached["name"], "connectionId": cached["connectionId"]}}
	} else {
		connectivity.Report(ctx, "lookup", "正在查找被控设备", connectivity.StageTimeout)
		r, e = post(ctx, service, token, "/p2p/devices/resolve", Object{"connectionId": p["code"]})
		if e != nil {
			return nil, e
		}
	}
	d := obj(r["device"])
	if r["online"] == false {
		return nil, errors.New("被控设备离线，请确认电脑上的 ReignHub 正在运行并已连接服务")
	}
	connectivity.Note(ctx, "lookup", "lookup", "已找到被控设备", str(d, "name"), "success")
	id, e := a.store.Route(ctx, service+"|"+str(d, "id"))
	if e != nil {
		return nil, e
	}
	if str(d, "id") == "" || str(d, "public") == "" {
		return nil, errors.New("无效设备信息")
	}
	a.mu.Lock()
	if a.closed || a.busy[id] != nil {
		a.mu.Unlock()
		return nil, errors.New("正在连接此设备或客户端已关闭")
	}
	ctx, stop := context.WithCancel(ctx)
	defer stop()
	a.busy[id] = stop
	a.generation[id]++
	g := a.generation[id]
	a.mu.Unlock()
	defer func() { a.mu.Lock(); delete(a.busy, id); a.mu.Unlock() }()
	if str(p, "public") != "" && p["public"] != d["public"] {
		return nil, errors.New("二维码公钥不匹配")
	}
	saved, e := a.saved()
	if e != nil {
		return nil, e
	}
	s := Object{}
	for _, v := range saved {
		if v["id"] == id {
			s = clone(v)
			break
		}
	}
	expired := num(s, "expires") > 0 && num(s, "expires") <= time.Now().Unix()
	if p["reconnectOnly"] == true && (s["reconnectDisabled"] == true || str(s, "key") == "" || expired) {
		return nil, errors.New("自动重连已停止或授权已过期，请手动连接")
	}
	if str(s, "public") != "" && s["public"] != d["public"] {
		return nil, errors.New("设备身份已变化，需要重新配对")
	}
	if str(s, "key") == "" || expired {
		connectivity.Report(ctx, "authorize", "等待被控设备授权", connectivity.StageTimeout)
		request := randomHex(16)
		envelope, e := seal(i, str(d, "public"), "reignhub/pair/request/"+request, Object{"password": p["password"], "remember": p["remember"] != false, "info": i["info"]})
		if e != nil {
			return nil, e
		}
		if _, e = post(ctx, service, token, "/p2p/pairings/request", Object{"id": request, "host": d["id"], "envelope": envelope}); e != nil {
			return nil, e
		}
		approvalCtx, stopApproval := context.WithTimeout(ctx, connectivity.ApprovalTimeout)
		defer stopApproval()
		var result Object
		for approvalCtx.Err() == nil {
			if !a.current(id, g) {
				return nil, errors.New("连接已取消")
			}
			result, e = post(approvalCtx, service, token, "/p2p/pairings/result", Object{"id": request})
			if e != nil {
				return nil, e
			}
			if result["state"] == "denied" {
				return nil, errors.New("主机拒绝授权或密码错误")
			}
			if result["state"] == "approved" {
				break
			}
			select {
			case <-approvalCtx.Done():
				return nil, fmt.Errorf("等待主机确认超时或取消: %w", approvalCtx.Err())
			case <-time.After(time.Second):
			}
		}
		if result["state"] != "approved" {
			return nil, errors.New("等待主机确认超时")
		}
		reply, e := openEnvelope(i, str(d, "public"), "reignhub/pair/reply/"+request, str(result, "reply"))
		if e != nil {
			return nil, e
		}
		if reply["host"] != d["id"] || reply["public"] != d["public"] {
			return nil, errors.New("无效主机授权")
		}
		s = Object{"id": id, "service": service, "connectionId": d["connectionId"], "name": d["name"], "host": d["id"], "public": d["public"], "key": reply["key"], "expires": reply["expires"]}
	}
	connectivity.Note(ctx, "authorize", "authorize", "设备授权已就绪", "", "success")
	deviceID, hostID := str(i, "id"), str(d, "id")
	var routes relay.Routes
	raw, _ := json.Marshal(s["routes"])
	_ = json.Unmarshal(raw, &routes)
	// A fresh enrollment can arrive on either side of the first verified RPC.
	// Serialize it with publication, disconnect and forgetting this environment.
	published := false
	fetch := func(refresh context.Context) (relay.Routes, error) {
		result, err := post(refresh, service, token, "/p2p/routes", Object{"host": hostID})
		var fresh relay.Routes
		if err != nil {
			return fresh, err
		}
		encoded, err := json.Marshal(result)
		if err == nil {
			err = json.Unmarshal(encoded, &fresh)
		}
		if err != nil {
			return fresh, err
		}
		a.edit.Lock()
		defer a.edit.Unlock()
		if !a.current(id, g) || refresh.Err() != nil {
			return fresh, context.Canceled
		}
		s["routes"] = result
		s["independentRoutes"] = fresh.Node != nil && fresh.Node.WSS != "" && fresh.Node.Device == deviceID && fresh.Node.Host == hostID && fresh.Node.Role == "client"
		if published {
			err = a.store.SaveEnvironment(refresh, s)
		}
		return fresh, err
	}
	pairing := browserclient.Pairing{Relay: strings.Replace(service, "http", "ws", 1), Host: hostID, Key: str(s, "key"), Token: token, RelayOnly: p["relayOnly"] == true}
	if !pairing.RelayOnly {
		entry, _ := a.store.Get("network:" + service)
		policy := obj(obj(entry)["policy"])
		pairing.ICE = browserclient.STUN(policy)
		if num(obj(entry), "at") < time.Now().Add(-10*time.Minute).Unix() {
			ready := make(chan struct{})
			var refreshed []string
			// Start discovery alongside dialing. A cold ICE attempt waits briefly for
			// local STUN settings; independent relay connections never wait for HTTP.
			go func() {
				defer close(ready)
				next, err := post(ctx, service, "", "/p2p/config", Object{})
				if err == nil {
					refreshed = browserclient.STUN(next)
					_ = a.store.Put("network:"+service, Object{"policy": next, "at": time.Now().Unix()})
				}
			}()
			fallback := pairing.ICE
			pairing.ICEServers = func(setup context.Context) []string {
				timer := time.NewTimer(time.Second)
				defer timer.Stop()
				select {
				case <-ready:
					if refreshed != nil {
						return refreshed
					}
				case <-timer.C:
				case <-setup.Done():
				}
				return fallback
			}
		}
	}
	c, e := browserclient.ConnectRoutes(ctx, pairing, deviceID, routes, fetch)
	if e != nil {
		return nil, e
	}
	a.edit.Lock()
	if !a.current(id, g) {
		a.edit.Unlock()
		c.Close()
		return nil, errors.New("连接已取消")
	}
	if platform := device.NormalizePlatform(c.HostPlatform()); platform != "" {
		s["platform"] = platform
	}
	s["name"], s["connectionId"] = d["name"], d["connectionId"]
	s["relayOnly"] = p["relayOnly"] == true
	s["reconnectDisabled"] = false
	e = a.store.SaveEnvironment(ctx, s)
	published = e == nil
	if e != nil {
		a.edit.Unlock()
		c.Close()
		return nil, e
	}
	a.mu.Lock()
	old := a.clients[id]
	a.clients[id] = c
	a.mu.Unlock()
	a.edit.Unlock()
	if old != nil {
		old.Close()
	}
	if info := device.ClientInfoFrom(ctx); info.Name != "" {
		publishClientInfo(c, info)
	} else {
		publishClientInfo(c, i["info"])
	}
	return a.list()
}
func (a *API) client(id string) *browserclient.Client {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.clients[id]
}
func (a *API) Call(ctx context.Context, method string, p Object, target string, command Object) (any, error) {
	if strings.HasPrefix(method, "client.account.") {
		paths := map[string]string{"start": "/api/account/device-login/start", "poll": "/api/account/device-login/poll", "status": "/api/account/device-status", "devices": "/api/account/device-list"}
		path, ok := paths[strings.TrimPrefix(method, "client.account.")]
		if !ok {
			return nil, errors.New("无效账号操作")
		}
		service := strings.TrimRight(str(p, "service"), "/")
		i, err := a.loadIdentity(ctx, service, str(p, "name"))
		if err != nil {
			return nil, err
		}
		return post(ctx, service, str(i, "token"), path, Object{"id": p["id"]})
	}
	switch method {
	case "client.list":
		return a.list()
	case "client.cached":
		return a.cached(target, command)
	case "client.draft.get", "client.draft.put":
		return composerdraft.Dispatch(ctx, a.store, method, target, p)
	case "client.snapshot":
		if target == "" || str(p, "provider") == "" || str(p, "session") == "" || p["transcript"] == nil {
			return nil, errors.New("invalid local transcript snapshot")
		}
		return nil, a.ApplyHistory(ctx, persistence.Session{Env: target, Provider: str(p, "provider"), ID: str(p, "session")}, persistence.Diff("", nil, obj(p["transcript"])))
	case "client.presence":
		saved, err := a.saved()
		if err != nil {
			return nil, err
		}
		targets := []device.PresenceTarget{}
		for _, s := range saved {
			identity, _ := a.store.LoadIdentity(ctx, str(s, "service"))
			targets = append(targets, device.PresenceTarget{ID: str(s, "id"), Service: str(s, "service"), Host: str(s, "host"), Token: str(obj(identity), "token")})
		}
		out := []Object{}
		for _, status := range device.Presence(ctx, targets) {
			item := Object{"id": status.ID, "host": status.Host, "name": status.Name, "checkedAt": status.CheckedAt, "online": nil}
			if status.Online != nil {
				item["online"] = *status.Online
			}
			out = append(out, item)
		}
		return out, ctx.Err()
	case "client.connect":
		return a.connect(ctx, p)
	case "client.reconnect":
		v, e := a.saved()
		if e != nil {
			return nil, e
		}
		for _, s := range v {
			if s["id"] == p["id"] {
				service := s["service"]
				if str(p, "service") != "" {
					service = p["service"]
				}
				if s["reconnectDisabled"] == true || str(s, "key") == "" || (num(s, "expires") > 0 && num(s, "expires") <= time.Now().Unix()) {
					return nil, errors.New("自动重连已停止或授权已过期，请手动连接")
				}
				if old := a.client(str(s, "id")); old != nil {
					connectivity.Report(ctx, "resume", "正在恢复原有连接", connectivity.StageTimeout)
					probe, stop := context.WithTimeout(ctx, connectivity.ResumeTimeout)
					err := old.Resume(probe)
					stop()
					if err == nil {
						info := device.ClientInfoFrom(ctx)
						if info.Name != "" {
							publishClientInfo(old, info)
						}
						return a.list()
					}
					if ctx.Err() != nil {
						return nil, ctx.Err()
					}
					old.Close()
				}
				params := Object{"service": service, "code": s["connectionId"], "public": s["public"], "relayOnly": s["relayOnly"], "reconnectOnly": true, "cached": s}
				connectivity.Report(ctx, "resume", "正在复用设备配对与连接配置", connectivity.StageTimeout)
				result, err := a.connect(ctx, params)
				if err == nil || ctx.Err() != nil {
					return result, err
				}
				connectivity.Note(ctx, "resume-fallback", "retry", "快速恢复未成功，重新查找设备", err.Error(), "warning")
				delete(params, "cached")
				return a.connect(ctx, params)
			}
		}
		return nil, errors.New("设备配对不存在")
	case "client.disconnect", "client.forget":
		id := str(p, "id")
		a.edit.Lock()
		a.mu.Lock()
		a.generation[id]++
		if stop := a.busy[id]; stop != nil {
			stop()
		}
		c := a.clients[id]
		delete(a.clients, id)
		a.mu.Unlock()
		if c != nil {
			c.Close()
		}
		var e error
		if method == "client.forget" {
			e = a.store.ForgetHistory(ctx, id)
		} else {
			var v []Object
			v, e = a.saved()
			if e == nil {
				for _, d := range v {
					if d["id"] == id {
						d["reconnectDisabled"] = true
						e = a.store.SaveEnvironment(ctx, d)
						break
					}
				}
			}
		}

		a.edit.Unlock()
		if e != nil {
			return nil, e
		}
		if method == "client.forget" {

			_, e = a.Call(ctx, "client.assetCache.clear", nil, id, nil)
			if e != nil {
				return nil, e
			}

		}
		return nil, nil
	case "client.call":
		kind := persistence.ReadKind(str(command, "method"))
		scope := persistence.Session{Env: target, Provider: str(command, "provider"), ID: str(command, "session")}
		read := false
		switch str(command, "method") {
		case "providers", "sessions", "models", "transcript", "approvals":
			read = true
		}
		c := a.client(target)
		if c == nil || !c.Connected() {
			if read && command["fresh"] != true {
				if str(command, "method") == "transcript" && str(command, "cursor") == "" {
					v, e := a.cached(target, command)
					if e != nil || v != nil {
						return v, e
					}
				}
				if str(command, "method") == "sessions" && str(command, "cursor") == "" {
					rows, e := a.store.ListSessions(ctx, persistence.DirectoryKey{Env: target, Provider: str(command, "provider")})
					if e != nil {
						return nil, e
					}
					if len(rows) > 0 {
						return Object{"sessions": rows}, nil
					}
				}
				v, e := a.store.LoadList(ctx, scope, kind)
				if e != nil || v != nil {
					return v, e
				}
			}
			return nil, errors.New("设备离线，请重新连接")
		}
		v, e := c.Call(ctx, command)
		if e == nil && str(command, "method") == "sessions" {
			e = a.store.WriteDirectory(ctx, persistence.DirectoryKey{Env: target, Provider: str(command, "provider")}, persistence.DirectoryHead{}, persistence.DirectoryBatch{Preview: true, Upserts: arr(obj(v)["sessions"])})
		} else if e == nil && kind.Table() != "" {
			e = a.store.SaveList(ctx, scope, kind, arr(v))
		}
		return v, e
	case "client.sync":
		k := persistence.DirectoryKey{Env: target, Provider: str(p, "provider")}
		old, e := a.store.DirectoryPosition(ctx, k)
		if e != nil {
			return nil, e
		}
		c := a.client(target)
		if c == nil || !c.Connected() {
			return nil, errors.New("设备离线：保留本地会话，连接后重试同步")
		}
		v, e := c.Call(ctx, Object{"method": "sync", "provider": k.Provider, "epoch": old.Epoch, "after": old.Cursor})
		if e != nil {
			return nil, e
		}
		d := obj(v)
		batch := persistence.DirectoryBatch{Reset: d["reset"] == true, Head: persistence.DirectoryHead{Epoch: str(d, "epoch"), Cursor: num(d, "cursor")}, Upserts: arr(d["items"])}
		for _, change := range arr(d["changes"]) {
			if change["deleted"] == true {
				batch.Changes = append(batch.Changes, persistence.DirectoryChange{ID: str(change, "id")})
			} else {
				batch.Changes = append(batch.Changes, persistence.DirectoryChange{ID: str(change, "id"), Value: obj(change["value"])})
			}
		}
		if e = a.store.WriteDirectory(ctx, k, old, batch.Coalesced()); e != nil {
			return nil, e
		}
		return a.store.ListSessions(ctx, k)
	case "client.assetCache.get", "client.assetCache.put", "client.assetCache.clear":
		return a.store.Download(ctx, method, target, p)
	default:
		return nil, errors.New("浏览器仅提供远控功能")
	}
}
func publishClientInfo(c *browserclient.Client, info any) {
	if info == nil {
		return
	}
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_, _ = c.Call(ctx, Object{"method": "peer.info", "params": info})
	}()
}

func (a *API) ApplyHistory(ctx context.Context, k persistence.Session, p persistence.HistoryPatch) error {
	return a.store.ApplyHistory(ctx, k, p)
}
func (a *API) ReadPage(ctx context.Context, k persistence.Session, q persistence.PageQuery) (persistence.HistoryPage, error) {
	return a.store.ReadPage(ctx, k, q)
}

func (a *API) Navigation(ctx context.Context, k persistence.Session) ([]persistence.Landmark, error) {
	if dao, ok := a.store.(persistence.NavigationDAO); ok {
		return dao.Navigation(ctx, k)
	}
	return nil, nil
}
