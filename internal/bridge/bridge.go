// Package bridge is the in-process JSON request boundary for native controllers.
package bridge

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"

	"github.com/monkeyWie/reignhub/internal/app"
	"github.com/monkeyWie/reignhub/internal/clienthub"
	"github.com/monkeyWie/reignhub/internal/config"
	"github.com/monkeyWie/reignhub/internal/control"
	"github.com/monkeyWie/reignhub/internal/terminal"
)

var mu sync.Mutex
var hostMu sync.Mutex
var lifecycle sync.RWMutex
var bridgeContext, cancelBridge = context.WithCancel(context.Background())
var hubs = map[string]*clienthub.Hub{}
var hosts = map[string]*app.Embedded{}
var shuttingDown bool
var terminalFactory terminal.Factory

// SetTerminalFactory supplies the desktop engine before the first host starts.
func SetTerminalFactory(factory terminal.Factory) {
	hostMu.Lock()
	defer hostMu.Unlock()
	terminalFactory = factory
}

func embeddedHost(dir, uiPath string) (*app.Embedded, error) {
	hostMu.Lock()
	defer hostMu.Unlock()
	if shuttingDown {
		return nil, fmt.Errorf("RAS 已退出")
	}
	if h := hosts[dir]; h != nil {
		return h, nil
	}
	c, err := config.Load(dir)
	if err != nil {
		return nil, err
	}
	if uiPath != "" && runtime.GOOS == "darwin" {
		c.UIPath = filepath.Clean(uiPath)
		if err := c.Save(); err != nil {
			return nil, err
		}
	}
	// The lifetime belongs to the application, never to a short-lived UI call.
	h, err := app.StartEmbedded(context.Background(), c, terminalFactory)
	if err != nil {
		return nil, err
	}
	hosts[dir] = h
	return h, nil
}

// Shutdown is called once by the native application when it actually quits.
// Hiding a window does not unload the Go runtime or stop its connections.
func Shutdown() {
	cancelBridge()
	lifecycle.Lock()
	defer lifecycle.Unlock()
	hostMu.Lock()
	shuttingDown = true
	for _, h := range hosts {
		_ = h.Close()
	}
	hosts = map[string]*app.Embedded{}
	hostMu.Unlock()
	mu.Lock()
	defer mu.Unlock()
	for _, h := range hubs {
		_ = h.Close()
	}
	hubs = map[string]*clienthub.Hub{}
}

func Call(raw string) string {
	return CallContext(context.Background(), raw)
}

// CallContext lets in-process clients cancel foreground reads when the app is
// suspended or the user switches sessions, without stopping the shared host.
func CallContext(parent context.Context, raw string) string {
	lifecycle.RLock()
	defer lifecycle.RUnlock()
	if bridgeContext.Err() != nil {
		return answer(nil, fmt.Errorf("RAS 已退出"))
	}
	var in struct {
		Method, DataDir, HostDir, UIPath, Target string
		Params                                   json.RawMessage
		Request                                  control.Request
	}
	if e := json.Unmarshal([]byte(raw), &in); e != nil {
		return answer(nil, e)
	}
	if in.DataDir == "" {
		return answer(nil, fmt.Errorf("missing private data directory"))
	}
	mu.Lock()
	h := hubs[in.DataDir]
	if h == nil {
		var e error
		h, e = clienthub.New(in.DataDir)
		if e != nil {
			mu.Unlock()
			return answer(nil, e)
		}
		hubs[in.DataDir] = h
	}
	mu.Unlock()
	// Business reads and commands follow the caller/application lifetime. Slow
	// agents and large snapshots must not inherit an arbitrary total deadline.
	// Connection setup and HTTP operations enforce their own stage budgets.
	ctx, cancel := context.WithCancel(parent)
	defer cancel()
	stop := context.AfterFunc(bridgeContext, cancel)
	defer stop()
	if err := ctx.Err(); err != nil {
		return answer(nil, err)
	}
	var p struct {
		Service, Code, Public, Password, Name, ID, Provider, Email, OTP string
		RelayOnly                                                       bool
	}
	if len(in.Params) > 0 {
		if e := json.Unmarshal(in.Params, &p); e != nil {
			return answer(nil, e)
		}
	}
	var out any
	var e error
	switch in.Method {
	case "client.assetCache.get", "client.assetCache.put", "client.assetCache.clear":
		var params struct{ Key, Data string }
		if err := json.Unmarshal(in.Params, &params); err != nil {
			return answer(nil, err)
		}
		switch in.Method {
		case "client.assetCache.get":
			var b []byte
			b, e = h.Assets.Get(in.Target, params.Key)
			if e == nil && b != nil {
				out = map[string]any{"data": base64.StdEncoding.EncodeToString(b)}
			}
		case "client.assetCache.put":
			if len(params.Data) > 70<<20 {
				return answer(nil, fmt.Errorf("附件超过缓存上限"))
			}
			var b []byte
			b, e = base64.StdEncoding.DecodeString(params.Data)
			if e == nil {
				e = h.Assets.Put(in.Target, params.Key, b)
			}
		case "client.assetCache.clear":
			e = h.Assets.Clear(in.Target)
		}
	case "client.account.send", "client.account.verify", "client.account.status", "client.account.devices":
		out, e = h.Account(ctx, p.Service, p.Name, strings.TrimPrefix(in.Method, "client.account."), p.Email, p.OTP)
	case "client.list":
		out, e = h.List()
	case "client.presence":
		out, e = h.Presence(ctx)
	case "client.connect":
		_, e = h.Connect(ctx, p.Service, p.Code, p.Public, p.Password, p.Name, p.RelayOnly)
		if e == nil {
			out, e = h.List()
		}
	case "client.reconnect":
		_, e = h.Reconnect(ctx, p.ID)
		if e == nil {
			out, e = h.List()
		}
	case "client.disconnect":
		e = h.Disconnect(p.ID)
	case "client.forget":
		e = h.Forget(p.ID)
	case "client.sync":
		out, e = h.Sync(ctx, in.Target, p.Provider)
	case "client.cached":
		out, e = h.Cached(in.Target, in.Request)
	case "client.draft.get", "client.draft.put":
		var params map[string]any
		e = json.Unmarshal(in.Params, &params)
		if e == nil {
			out, e = h.Draft(ctx, in.Method, in.Target, params)
		}
	case "client.snapshot":
		var snapshot struct {
			Provider, Session string
			Transcript        json.RawMessage
		}
		e = json.Unmarshal(in.Params, &snapshot)
		if e == nil {
			e = h.SaveSnapshot(in.Target, snapshot.Provider, snapshot.Session, snapshot.Transcript)
		}
	case "client.call":
		out, e = h.Call(ctx, in.Target, in.Request)
	default:
		if runtime.GOOS == "android" || runtime.GOOS == "ios" {
			e = fmt.Errorf("移动端只提供远控功能")
			break
		}
		if !strings.HasPrefix(in.Method, "host.") && in.Method != "providers" {
			e = fmt.Errorf("unknown bridge method")
			break
		}
		dir := in.HostDir
		if dir == "" {
			dir = config.DefaultDir()
		}
		embedded, err := embeddedHost(dir, in.UIPath)
		if err != nil {
			return answer(nil, err)
		}
		// The controller sees the local runtime as an ordinary environment.
		// Attaching it needs neither a service identity nor a network connection.
		name := "本机"
		if c, err := config.Load(dir); err == nil && c.DeviceName != "" {
			name = c.DeviceName
		}
		h.SetLocal(embedded, name)
		if in.Method == "host.bootstrap" {
			out = map[string]any{"ready": true, "pid": os.Getpid(), "transport": "embedded"}
		} else {
			r := in.Request
			r.Method = in.Method
			r.Params = in.Params
			if in.Method == "host.account.verify" {
				var info struct {
					ServiceURL string `json:"serviceUrl"`
				}
				if err := embedded.Call(ctx, control.Request{Method: "host.info"}, &info); err != nil {
					return answer(nil, err)
				}
				token, err := h.ControllerCredential(ctx, info.ServiceURL, p.Name)
				if err != nil {
					return answer(nil, err)
				}
				r.Params, _ = json.Marshal(map[string]string{"controllerToken": token, "serviceUrl": info.ServiceURL, "email": p.Email, "otp": p.OTP})
			}
			e = embedded.Call(ctx, r, &out)
		}
	}
	return answer(out, e)
}
func answer(v any, e error) string {
	out := map[string]any{"result": v}
	if e != nil {
		out = map[string]any{"error": e.Error(), "code": control.ErrorCode(e)}
	}
	b, _ := json.Marshal(out)
	return string(b)
}
