// Package servercmd runs the standalone, account-free ReignHub service.
package servercmd

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"os"
	"os/signal"
	"slices"
	"strings"
	"syscall"
	"time"

	"github.com/monkeyWie/reignhub/internal/connectivity"
	"github.com/monkeyWie/reignhub/internal/deviceservice"
	"github.com/monkeyWie/reignhub/internal/relay"
	"github.com/monkeyWie/reignhub/internal/webui"
)

// Version is set by release builds.
var Version = "dev"

// Runtime owns all listeners, sockets and the SQLite database.
type Runtime struct {
	native    *relay.NativeListener
	HTTPAddr  net.Addr
	UDPAddr   net.Addr
	TCPAddr   net.Addr
	http      *http.Server
	listener  net.Listener
	directory *deviceservice.Server
	udp       *connectivity.STUNServer
	tcp       *connectivity.TCPSTUNServer
	errors    chan error
}

func Start(c Config) (_ *Runtime, err error) {
	r := &Runtime{errors: make(chan error, 1)}
	defer func() {
		if err != nil {
			_ = r.Close()
		}
	}()
	if c.Control || c.WebSocket {
		r.listener, err = net.Listen("tcp", c.Listen)
		if err != nil {
			return nil, fmt.Errorf("HTTP listen: %w", err)
		}
		r.HTTPAddr = r.listener.Addr()
	}
	if c.STUNListen != "" {
		r.udp, err = connectivity.ListenSTUN(c.STUNListen)
		if err != nil {
			return nil, fmt.Errorf("UDP STUN listen: %w", err)
		}
		r.UDPAddr = r.udp.Addr()
	}
	if c.TCPSTUNListen != "" {
		r.tcp, err = connectivity.ListenTCPSTUN(c.TCPSTUNListen)
		if err != nil {
			return nil, fmt.Errorf("TCP STUN listen: %w", err)
		}
		r.TCPAddr = r.tcp.Addr()
	}
	if c.Control || c.WebSocket || c.NativeListen != "" {
		r.directory, err = deviceservice.New(c.State, c.Origins)
		if err != nil {
			return nil, err
		}
		r.directory.SetRevokedRelayDevices(c.RevokedDevices)
		r.directory.ConfigureNative(c.Nodes, c.WebSocket)
		r.directory.ConfiguredSTUN = c.STUNServers
		if c.NativeListen != "" {
			r.native, err = r.directory.ListenNative(c.NativeListen, c.NativePublic)
			if err != nil {
				return nil, fmt.Errorf("native relay listen: %w", err)
			}
		}
		if c.STUNPublic != "" {
			r.directory.Network.SelfSTUN = []string{"stun:" + c.STUNPublic}
		}
		if c.TCPSTUNPublic != "" {
			r.directory.Network.TCPSTUN = []string{c.TCPSTUNPublic}
		}
		if r.UDPAddr != nil {
			_, r.directory.STUNPort, _ = net.SplitHostPort(r.UDPAddr.String())
		}
		if r.TCPAddr != nil {
			_, r.directory.TCPSTUNPort, _ = net.SplitHostPort(r.TCPAddr.String())
		}
	}
	if c.Control || c.WebSocket {
		web := webui.Handler()
		if c.Control && c.Web != "" {
			web, err = webui.Directory(c.Web)
			if err != nil {
				return nil, err
			}
		}
		r.http = &http.Server{Handler: handler(c, r.directory, web), ReadHeaderTimeout: 10 * time.Second, ReadTimeout: 20 * time.Second, IdleTimeout: 75 * time.Second, MaxHeaderBytes: 32 << 10}
		// ServeTLS validates the certificate before reporting successful startup.
		if c.TLSCert != "" {
			if err = configureTLS(r.http, c.TLSCert, c.TLSKey); err != nil {
				return nil, err
			}
		}
		go func() {
			var err error
			if c.TLSCert != "" {
				err = r.http.ServeTLS(r.listener, "", "")
			} else {
				err = r.http.Serve(r.listener)
			}
			if err != nil && !errors.Is(err, http.ErrServerClosed) && !errors.Is(err, net.ErrClosed) {
				r.errors <- err
			}
		}()
	}
	return r, nil
}

func handler(c Config, directory *deviceservice.Server, web http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("X-Content-Type-Options", "nosniff")
		if r.URL.Path == "/p2p/node" {
			if !c.WebSocket {
				http.NotFound(w, r)
				return
			}
			directory.ServeNode(w, r)
			return
		}
		if !c.Control && r.URL.Path != "/healthz" {
			http.NotFound(w, r)
			return
		}
		switch {
		case r.URL.Path == "/healthz":
			if r.Method != "GET" && r.Method != "HEAD" {
				w.WriteHeader(405)
				return
			}
			w.Header().Set("Cache-Control", "no-store")
			_, _ = io.WriteString(w, "ok")
		case r.URL.Path == "/api/account":
			w.Header().Set("Content-Type", "application/json")
			w.Header().Set("Cache-Control", "no-store")
			_, _ = io.WriteString(w, `{"deployment":"self-hosted","account":null,"providers":[],"plans":[]}`)
		case strings.HasPrefix(r.URL.Path, "/p2p/"):
			if r.URL.Path == "/p2p/relay" && !c.WebSocket {
				http.NotFound(w, r)
				return
			}
			directory.ServeHTTP(w, r)
		case r.URL.Path == "/" || r.URL.Path == "/chat" || strings.HasPrefix(r.URL.Path, "/chat/") || strings.HasPrefix(r.URL.Path, "/assets/chat/"):
			web.ServeHTTP(w, r)
		default:
			http.NotFound(w, r)
		}
	})
}

func (r *Runtime) Close() error {
	// Stop accepting new upgrades, then close hijacked sockets before closing SQLite.
	if r.listener != nil {
		_ = r.listener.Close()
	}
	if r.http != nil {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		err := r.http.Shutdown(ctx)
		cancel()
		if err != nil {
			_ = r.http.Close()
		}
	}
	if r.native != nil {
		r.native.Close()
	}
	if r.directory != nil {
		_ = r.directory.Close()
	}
	if r.udp != nil {
		_ = r.udp.Close()
	}
	if r.tcp != nil {
		_ = r.tcp.Close()
	}
	return nil
}

func Run() {
	cmd, err := parseCommand(os.Args[1:], os.Stderr)
	if errors.Is(err, flag.ErrHelp) {
		return
	}
	if err != nil {
		log.Print(err)
		os.Exit(2)
	}
	switch cmd.action {
	case printVersion:
		fmt.Println("reignhub-server " + Version)
		return
	case initConfig:
		if err := writeDefaultConfig(cmd.file); err != nil {
			log.Print(err)
			os.Exit(1)
		}
		fmt.Println("Created", cmd.file)
		return
	}
	c, err := loadConfig(cmd.file)
	if err != nil {
		log.Print(err)
		os.Exit(2)
	}
	if cmd.action == printRelayCredential {
		credential, err := deviceservice.ReadRelayCredential(c.State)
		if err != nil {
			log.Print(err)
			os.Exit(1)
		}
		fmt.Println(credential)
		return
	}
	r, err := Start(c)
	if err != nil {
		log.Print(err)
		os.Exit(1)
	}
	defer r.Close()
	var nativeAddr net.Addr
	if r.native != nil {
		nativeAddr = r.native.Addr()
	}
	log.Printf("ReignHub server %s; HTTP=%v UDP STUN=%v TCP STUN=%v native relay=%v", Version, r.HTTPAddr, r.UDPAddr, r.TCPAddr, nativeAddr)
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	if r.native != nil || c.WebSocket {
		go r.watchRevocations(ctx, cmd.file, c.RevokedDevices)
	}
	select {
	case <-ctx.Done():
	case err := <-r.errors:
		log.Print(err)
	}
}

// Listener changes take effect on restart. Revocations also affect live legs,
// so reread the small local config and apply only that field.
func (r *Runtime) watchRevocations(ctx context.Context, path string, previous []string) {
	tick := time.NewTicker(2 * time.Second)
	defer tick.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-tick.C:
		}
		next, err := loadConfig(path)
		if err != nil || slices.Equal(previous, next.RevokedDevices) {
			continue
		}
		previous = next.RevokedDevices
		r.directory.SetRevokedRelayDevices(previous)
	}
}
