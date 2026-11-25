package codex

import (
	"bytes"
	"context"
	"crypto/subtle"
	"crypto/tls"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"net/netip"
	"os"
	"os/exec"
	"regexp"
	"strings"
	"sync"
	"time"

	"github.com/monkeyWie/reignhub/internal/nativepath"
	"github.com/monkeyWie/reignhub/internal/rpc"
	"github.com/monkeyWie/reignhub/pkg/appdb"
)

// Only the local settings database and the private startup pipe carry these
// credentials. The desktop keeps a separate loopback connection to the worker.
type sharedEndpoint struct {
	Control, Address, HubPath, Token, Pin string
	PreferIPC                             bool `json:",omitempty"`
}

type sharedConnection struct {
	sharedEndpoint
	http *http.Client
}

func loopbackAddress(address string) bool {
	a, err := netip.ParseAddrPort(address)
	return err == nil && a.Addr() == netip.MustParseAddr("127.0.0.1") && a.Port() != 0
}

func (e sharedEndpoint) connection() (*sharedConnection, error) {
	if !loopbackAddress(e.Control) || !strings.HasPrefix(e.Address, "ws://") || !loopbackAddress(strings.TrimPrefix(e.Address, "ws://")) || !launchPath.MatchString(e.HubPath) || e.Token == "" || e.Pin == "" {
		return nil, errors.New("共享 Codex 连接记录无效")
	}
	client := &http.Client{Transport: &http.Transport{TLSClientConfig: nativepath.PinnedClientTLS(e.Pin, "http/1.1")}, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	return &sharedConnection{e, client}, nil
}

func (c *sharedConnection) dial(ctx context.Context, observe rpc.Observer) (*rpc.Client, error) {
	return rpc.DialWithClient(ctx, "wss://"+c.Control+c.HubPath, c.Token, observe, c.http)
}

var launchPath = regexp.MustCompile(`^/[a-zA-Z0-9_-]{24,128}$`)

type desktopState struct {
	Path  string
	Ready bool
}

func (c *sharedConnection) desktopState(ctx context.Context, method string, path string) (desktopState, error) {
	ctx, cancel := context.WithTimeout(ctx, time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, method, "https://"+c.Control+"/desktop", strings.NewReader(path))
	if err != nil {
		return desktopState{}, err
	}
	req.Header.Set("Authorization", "Bearer "+c.Token)
	res, err := c.http.Do(req)
	if err != nil {
		return desktopState{}, err
	}
	defer res.Body.Close()
	var state desktopState
	if res.StatusCode != http.StatusOK {
		return state, errors.New("无法确认共享 Codex 桌面连接")
	}
	err = json.NewDecoder(io.LimitReader(res.Body, 512)).Decode(&state)
	return state, err
}

func (c *sharedConnection) connected(ctx context.Context) bool {
	d, err := c.desktopState(ctx, http.MethodGet, "")
	return err == nil && d.Ready
}

const sharedSetting = "codex/sharedDesktop"

// A persisted endpoint is a candidate, never proof of desktop ownership.
func savedConnection(raw []byte) *sharedConnection {
	var endpoint sharedEndpoint
	if json.Unmarshal(raw, &endpoint) != nil {
		return nil
	}
	c, _ := endpoint.connection()
	return c
}

// The desktop can be between sockets when Hub starts. Give its registered
// launch a short window to finish initialization on the authenticated bridge.
func (c *sharedConnection) waitDesktop(ctx context.Context) bool {
	wait, cancel := context.WithTimeout(ctx, 2500*time.Millisecond)
	defer cancel()
	tick := time.NewTicker(100 * time.Millisecond)
	defer tick.Stop()
	for {
		state, err := c.desktopState(wait, http.MethodGet, "")
		if err == nil {
			if state.Ready {
				return true
			}
			if state.Path == "" {
				return false
			}
		}
		select {
		case <-wait.Done():
			return false
		case <-tick.C:
		}
	}
}

func readSharedEndpoint(ctx context.Context, dir string) ([]byte, error) {
	db, err := appdb.Open(appdb.Path(dir))
	if err != nil {
		return nil, err
	}
	defer db.Close()
	var raw []byte
	err = db.QueryRowContext(ctx, "SELECT v FROM settings WHERE k=?", sharedSetting).Scan(&raw)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	return raw, err
}

// Compare and swap so two simultaneous launches cannot both publish a desktop endpoint.
func saveSharedEndpoint(ctx context.Context, dir string, previous []byte, e sharedEndpoint) error {
	db, err := appdb.Open(appdb.Path(dir))
	if err != nil {
		return err
	}
	defer db.Close()
	raw, err := json.Marshal(e)
	if err != nil {
		return err
	}
	var result sql.Result
	if previous == nil {
		result, err = db.ExecContext(ctx, "INSERT INTO settings(k,v) VALUES(?,?) ON CONFLICT(k) DO NOTHING", sharedSetting, raw)
	} else {
		result, err = db.ExecContext(ctx, "UPDATE settings SET v=? WHERE k=? AND v=?", raw, sharedSetting, previous)
	}
	if err != nil {
		return err
	}
	n, err := result.RowsAffected()
	if err != nil {
		return err
	}
	if n != 1 {
		return errors.New("共享 Codex 连接已变化，请重试")
	}
	return nil
}

func startSharedWorker(ctx context.Context, cli, dir string) (*sharedConnection, error) {
	exe, release, err := sharedWorkerExecutable(dir)
	if err != nil {
		return nil, err
	}
	cmd := exec.Command(exe, sharedWorkerFlag)
	configureWorkerProcess(cmd)
	input, _ := json.Marshal(cli)
	cmd.Stdin = bytes.NewReader(input)
	out, err := cmd.StdoutPipe()
	if err != nil {
		release()
		return nil, err
	}
	if err = cmd.Start(); err != nil {
		out.Close()
		release()
		return nil, err
	}
	// The worker has its own startup and idle deadlines and reaps its child.
	// Cancel this caller's pipe read without killing the worker mid-cleanup.
	done := make(chan error, 1)
	var startup sharedWorkerStartup
	go func() { done <- json.NewDecoder(io.LimitReader(out, 4096)).Decode(&startup) }()
	select {
	case err = <-done:
	case <-ctx.Done():
		_ = out.Close()
		<-done
		err = ctx.Err()
	}
	_ = out.Close()
	go func() { _ = cmd.Wait(); release() }()
	if err != nil {
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		return nil, fmt.Errorf("共享 Codex 后台进程未返回启动结果，请重试: %w", err)
	}
	if startup.Failure != "" {
		return nil, startup.Failure
	}
	return startup.Endpoint.connection()
}

type sharedWorkerStartup struct {
	Endpoint sharedEndpoint
	Failure  sharedStartupFailure
}

// Report only known failure stages. Raw CLI output and launch errors can
// contain local paths, credentials or the private WebSocket URL.
type sharedStartupFailure string

const (
	sharedStartupRequest    sharedStartupFailure = "request"
	sharedStartupCLI        sharedStartupFailure = "cli"
	sharedStartupPermission sharedStartupFailure = "permission"
	sharedStartupExited     sharedStartupFailure = "exited"
	sharedStartupTimeout    sharedStartupFailure = "timeout"
	sharedStartupInitialize sharedStartupFailure = "initialize"
	sharedStartupListener   sharedStartupFailure = "listener"
)

func (f sharedStartupFailure) Error() string {
	switch f {
	case sharedStartupCLI:
		return "无法启动 Codex 后端，请检查 Codex 安装后重试"
	case sharedStartupPermission:
		return "系统拒绝启动 Codex 后端，请确认 Codex 与 ReignHub 使用同一用户运行"
	case sharedStartupExited:
		return "Codex 后端启动后提前退出，请检查 Codex 版本后重试"
	case sharedStartupTimeout:
		return "等待 Codex 后端启动超时，请重试"
	case sharedStartupInitialize:
		return "Codex 后端未完成 WebSocket 初始化，请检查 Codex 版本后重试"
	case sharedStartupListener:
		return "无法建立 Codex 本机连接，请重试"
	default:
		return "共享 Codex 后台进程启动失败，请重试"
	}
}

const sharedWorkerFlag = "--reignhub-codex-bridge"

// RunSharedWorker is dispatched before native window or service initialization.
func RunSharedWorker() bool {
	if len(os.Args) != 2 || os.Args[1] != sharedWorkerFlag {
		return false
	}
	runSharedWorker(os.Stdin, os.Stdout)
	return true
}

func runSharedWorker(input io.Reader, output io.Writer) {
	var cli string
	failure := sharedStartupRequest
	if json.NewDecoder(io.LimitReader(input, 8192)).Decode(&cli) == nil && cli != "" {
		err := serveSharedWorker(cli, output)
		if err == nil {
			return
		}
		switch {
		case errors.Is(err, context.DeadlineExceeded):
			failure = sharedStartupTimeout
		case errors.Is(err, os.ErrPermission) && errors.Is(err, sharedStartupCLI):
			failure = sharedStartupPermission
		default:
			_ = errors.As(err, &failure)
		}
	}
	_ = json.NewEncoder(output).Encode(sharedWorkerStartup{Failure: failure})
}

func serveSharedWorker(cli string, output io.Writer) error {
	ctx, cancel := context.WithTimeout(context.Background(), 12*time.Second)
	defer cancel()
	s, err := startSharedServer(ctx, cli, nil)
	if err != nil {
		return err
	}
	defer s.close()
	_ = s.client.Close() // Only actual Hub and desktop connections keep the backend alive.
	key := nativepath.Key([]byte(s.token), "codex-local-bridge")
	cert, err := nativepath.ServerTLS(key)
	if err != nil {
		return sharedStartupListener
	}
	cert.NextProtos = []string{"http/1.1"}
	listener, err := net.Listen("tcp4", "127.0.0.1:0")
	if err != nil {
		return sharedStartupListener
	}
	mux := http.NewServeMux()
	mux.Handle(s.hubPath, s)
	var launches sync.Mutex
	var previous *sharedDesktop
	mux.HandleFunc("/desktop", func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Origin") != "" || subtle.ConstantTimeCompare([]byte(r.Header.Get("Authorization")), []byte("Bearer "+s.token)) != 1 {
			http.NotFound(w, r)
			return
		}
		launches.Lock()
		defer launches.Unlock()
		d := s.desktop.Load()
		switch r.Method {
		case http.MethodGet:
		case http.MethodPut, http.MethodDelete:
			path, err := io.ReadAll(io.LimitReader(r.Body, 160))
			if err != nil || !launchPath.Match(path) {
				http.Error(w, "invalid launch", 400)
				return
			}
			if r.Method == http.MethodPut {
				previous = d
				s.desktop.Store(&sharedDesktop{path: string(path)})
			} else if d != nil && d.path == string(path) {
				s.desktop.Store(previous)
				previous = nil
			}
		default:
			http.NotFound(w, r)
			return
		}
		state := desktopState{}
		if d != nil {
			state = desktopState{d.path, d.ready.Load() > 0}
		}
		_ = json.NewEncoder(w).Encode(state)
	})
	h := &http.Server{Handler: mux, ReadHeaderTimeout: 3 * time.Second, ReadTimeout: 4 * time.Second, IdleTimeout: 10 * time.Second, MaxHeaderBytes: 8 << 10, ErrorLog: log.New(io.Discard, "", 0)}
	defer h.Close()
	go func() { _ = h.Serve(tls.NewListener(listener, cert)); s.close() }()
	e := sharedEndpoint{Control: listener.Addr().String(), Address: s.address, HubPath: s.hubPath, Token: s.token, Pin: nativepath.Pin(key)}
	if err = json.NewEncoder(output).Encode(sharedWorkerStartup{Endpoint: e}); err != nil {
		return err
	}
	ticker := time.NewTicker(time.Second)
	defer ticker.Stop()
	last := time.Now()
	for {
		select {
		case <-s.ctx.Done():
			return nil
		case now := <-ticker.C:
			if len(s.slots) > 0 {
				last = now
			} else if now.Sub(last) > time.Minute {
				return nil
			}
		}
	}
}
