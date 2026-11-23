package codex

import (
	"bufio"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"net/netip"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/coder/websocket"
	"github.com/monkeyWie/reignhub/internal/rpc"
	"github.com/monkeyWie/reignhub/internal/system"
)

type sharedServer struct {
	ctx      context.Context
	cancel   context.CancelFunc
	client   *rpc.Client
	address  string
	hubPath  string
	upstream string
	token    string
	desktop  atomic.Pointer[sharedDesktop]
	slots    chan struct{}
}

type sharedDesktop struct {
	path  string
	ready atomic.Int32
}

var sharedListenURL = regexp.MustCompile("ws://127[.]0[.]0[.]1:[0-9]{1,5}")

func startSharedServer(ctx context.Context, cli string, observe rpc.Observer) (*sharedServer, error) {
	life, cancel := context.WithCancel(context.Background())
	s := &sharedServer{ctx: life, cancel: cancel, token: rand.Text(), hubPath: "/" + rand.Text(), slots: make(chan struct{}, 4)}
	established := false
	defer func() {
		if !established {
			s.close()
		}
	}()
	digest := sha256.Sum256([]byte(s.token))
	cmd := exec.CommandContext(life, cli, "-c", "features.code_mode_host=true", "app-server", "--listen", "ws://127.0.0.1:0",
		"--ws-auth", "capability-token", "--ws-token-sha256", hex.EncodeToString(digest[:]))
	cmd.Dir = filepath.Dir(cli)
	system.HideConsole(cmd)
	stderr, err := cmd.StderrPipe()
	if err != nil {
		return nil, err
	}
	if err := cmd.Start(); err != nil {
		_ = stderr.Close()
		return nil, fmt.Errorf("启动共享 Codex 后端失败: %w", err)
	}
	endpoint := make(chan string, 1)
	go func() {
		scanner := bufio.NewScanner(stderr)
		for scanner.Scan() {
			if address := sharedListenURL.FindString(scanner.Text()); address != "" {
				port, err := netip.ParseAddrPort(strings.TrimPrefix(address, "ws://"))
				if err == nil && port.Port() != 0 {
					select {
					case endpoint <- address:
					default:
					}
					break
				}
			}
		}
		_, _ = io.Copy(io.Discard, stderr)
		_ = cmd.Wait()
		cancel()
	}()
	select {
	case s.upstream = <-endpoint:
	case <-ctx.Done():
		return nil, ctx.Err()
	case <-life.Done():
		return nil, errors.New("共享 Codex 后端提前退出，请检查 App 版本是否支持本机 WebSocket 和鉴权")
	}
	listener, err := (&net.ListenConfig{}).Listen(ctx, "tcp4", "127.0.0.1:0")
	if err != nil {
		return nil, err
	}
	s.address = "ws://" + listener.Addr().String()
	server := &http.Server{
		Handler: s, ReadHeaderTimeout: 3 * time.Second, MaxHeaderBytes: 8 << 10,
		ErrorLog:    log.New(io.Discard, "", 0),
		BaseContext: func(net.Listener) context.Context { return life },
	}
	context.AfterFunc(life, func() { _ = server.Close() })
	go func() { _ = server.Serve(listener); cancel() }()
	s.client, err = rpc.DialAuthenticated(ctx, s.address+s.hubPath, s.token, observe)
	if err != nil {
		return nil, errors.New("共享 Codex 后端未完成初始化")
	}
	context.AfterFunc(life, func() { _ = s.client.Close() })
	established = true
	return s, nil
}

func (s *sharedServer) close() { s.cancel() }
func (s *sharedServer) connected() bool {
	desktop := s.desktop.Load()
	return s.ctx.Err() == nil && desktop != nil && desktop.ready.Load() > 0
}

// The App's local WebSocket transport has no bearer header override. Its
// launch URL is a secret, while the actual app-server checks its own token.
// Every App connection gets an independent upstream connection.
func (s *sharedServer) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if r.URL.RawQuery != "" || r.Header.Get("Origin") != "" {
		http.NotFound(w, r)
		return
	}
	path := []byte(r.URL.EscapedPath())
	desktop := s.desktop.Load()
	app := desktop != nil && subtle.ConstantTimeCompare(path, []byte(desktop.path)) == 1
	hub := subtle.ConstantTimeCompare(path, []byte(s.hubPath)) == 1 &&
		subtle.ConstantTimeCompare([]byte(r.Header.Get("Authorization")), []byte("Bearer "+s.token)) == 1
	if !app && !hub {
		http.NotFound(w, r)
		return
	}
	select {
	case s.slots <- struct{}{}:
		defer func() { <-s.slots }()
	default:
		w.WriteHeader(http.StatusServiceUnavailable)
		return
	}
	setup, cancel := context.WithTimeout(r.Context(), 4*time.Second)
	up, resp, err := websocket.Dial(setup, s.upstream, &websocket.DialOptions{
		HTTPHeader: http.Header{"Authorization": {"Bearer " + s.token}},
	})
	cancel()
	if err != nil {
		if resp != nil {
			_ = resp.Body.Close()
		}
		w.WriteHeader(http.StatusBadGateway)
		return
	}
	defer up.CloseNow()
	down, err := websocket.Accept(w, r, nil)
	if err != nil {
		return
	}
	defer down.CloseNow()
	ctx, stop := context.WithCancel(r.Context())
	defer stop()
	up.SetReadLimit(32 << 20)
	down.SetReadLimit(32 << 20)

	var mu sync.Mutex
	var initializeID string
	ready := false
	var timeout *time.Timer
	if app {
		timeout = time.AfterFunc(10*time.Second, stop)
		defer timeout.Stop()
	}
	type initMessage struct {
		ID     json.RawMessage
		Method string
		Result json.RawMessage
		Error  *rpc.Error
	}
	finished := make(chan struct{})
	go func() {
		defer close(finished)
		defer stop()
		for {
			kind, data, err := down.Read(ctx)
			if err != nil {
				return
			}
			var message initMessage
			mu.Lock()
			if app && !ready && json.Unmarshal(data, &message) == nil && message.Method == "initialize" {
				initializeID = string(message.ID)
			}
			mu.Unlock()
			if err := up.Write(ctx, kind, data); err != nil {
				return
			}
		}
	}()
	for {
		kind, data, err := up.Read(ctx)
		if err != nil || down.Write(ctx, kind, data) != nil {
			break
		}
		mu.Lock()
		if app && !ready && initializeID != "" {
			var message initMessage
			// Codex Desktop can start sending requests immediately after
			// initialize succeeds, without an initialized notification.
			if json.Unmarshal(data, &message) == nil && message.Method == "" && string(message.ID) == initializeID && len(message.Result) != 0 && message.Error == nil && timeout.Stop() {
				ready = true
				desktop.ready.Add(1)
			}
		}
		mu.Unlock()
	}
	stop()
	<-finished
	if ready {
		desktop.ready.Add(-1)
	}
}
