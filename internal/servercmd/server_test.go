package servercmd

import (
	"bytes"
	"context"
	"crypto/tls"
	"errors"
	"io"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/coder/websocket"
	"github.com/monkeyWie/reignhub/internal/connectivity"
	"github.com/monkeyWie/reignhub/internal/device"
	"github.com/monkeyWie/reignhub/internal/deviceservice"
	"github.com/monkeyWie/reignhub/internal/nativepath"
	"github.com/pion/stun/v3"
)

func testConfig(t *testing.T) Config {
	t.Helper()
	c, err := allServices().resolve()
	if err != nil {
		t.Fatal(err)
	}
	c.NativeListen = "127.0.0.1:0"
	c.Listen = "127.0.0.1:0"
	c.STUNListen = "127.0.0.1:0"
	c.TCPSTUNListen = "127.0.0.1:0"
	c.State = filepath.Join(t.TempDir(), "state", "server.db")
	return c
}
func TestConfigFileLifecycle(t *testing.T) {
	t.Chdir(t.TempDir())
	cmd, err := parseCommand(nil, io.Discard)
	if err != nil || cmd.file != "config.json" || cmd.action != serve {
		t.Fatalf("default command: %+v %v", cmd, err)
	}
	if _, err := loadConfig(cmd.file); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("expected missing config", err)
	}
	if err := writeDefaultConfig(cmd.file); err != nil {
		t.Fatal(err)
	}
	c, err := loadConfig(cmd.file)
	cwd, _ := os.Getwd()
	if err != nil || !c.Control || c.State != filepath.Join(cwd, "data", "reignhub.db") {
		t.Fatal(c, err)
	}
	if _, err := os.Stat("data"); !os.IsNotExist(err) {
		t.Fatal("init created state")
	}
	file := filepath.Join(t.TempDir(), "custom.json")
	custom := []byte(`{"services":{"control":{"webDir":"console","tls":{"certFile":"cert","keyFile":"key"}},"stun":{"udp":{"listen":"[::1]:40001"}}}}`)
	if err := os.WriteFile(file, custom, 0600); err != nil {
		t.Fatal(err)
	}
	if err := writeDefaultConfig(file); err == nil {
		t.Fatal("overwrote existing config")
	}
	got, _ := os.ReadFile(file)
	if !bytes.Equal(custom, got) {
		t.Fatal("changed existing config")
	}
	c, err = loadConfig(file)
	if err != nil || !c.Control || c.TCPSTUNListen != ":26331" || c.STUNListen != "[::1]:40001" || c.Web != filepath.Join(filepath.Dir(file), "console") || c.TLSKey != filepath.Join(filepath.Dir(file), "key") {
		t.Fatal(c, err)
	}
	for _, value := range []string{`{"typo":true}`, `null`, `{}` + " {}", `{"relay":true,"signal":false}`, `{"services":{"relay":{"controlUrl":"http://remote.example"}}}`, `{}` + strings.Repeat(" ", 1<<20), `{"services":{"control":{"enable":false},"stun":{"enable":false},"relay":{"enable":false}}}`, `{"services":{"stun":{"enable":"false"}}}`, `{"services":{"relay":{"enable":null}}}`, `{"services":null}`, `{"services":{"control":{"stunServers":null}}}`, `{"services":{"control":{"stunServers":["bad"]}}}`, `{"services":{"control":{"signal":false}}}`} {
		if err := os.WriteFile(file, []byte(value), 0600); err != nil {
			t.Fatal(err)
		}
		if _, err := loadConfig(file); err == nil {
			t.Fatal("accepted invalid config")
		}
	}
	for _, args := range [][]string{{"--only", "stun"}, {"--state", "file"}, {"--init", "--print-relay-credential"}, {"--listen", ":8080"}, {"extra"}} {
		if _, err := parseCommand(args, io.Discard); err == nil {
			t.Fatal("accepted", args)
		}
	}
}
func checkProbes(t *testing.T, r *Runtime) {
	t.Helper()
	c, err := net.Dial("tcp", r.TCPAddr.String())
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	mapped, err := connectivity.TCPBinding(ctx, c)
	if err != nil || mapped.String() != c.LocalAddr().String() {
		t.Fatal(mapped, err)
	}
	udp, err := net.Dial("udp", r.UDPAddr.String())
	if err != nil {
		t.Fatal(err)
	}
	defer udp.Close()
	_ = udp.SetDeadline(time.Now().Add(2 * time.Second))
	request := stun.MustBuild(stun.TransactionID, stun.BindingRequest, stun.Fingerprint)
	if _, err = udp.Write(request.Raw); err != nil {
		t.Fatal(err)
	}
	raw := make([]byte, 1500)
	n, err := udp.Read(raw)
	if err != nil {
		t.Fatal(err)
	}
	response := stun.New()
	response.Raw = raw[:n]
	if err = response.Decode(); err != nil || response.TransactionID != request.TransactionID {
		t.Fatal("invalid UDP binding", err)
	}
	var addr stun.XORMappedAddress
	if err = addr.GetFrom(response); err != nil || net.JoinHostPort(addr.IP.String(), fmtPort(addr.Port)) != udp.LocalAddr().String() {
		t.Fatal(addr, err)
	}
}
func fmtPort(port int) string { return strconv.Itoa(port) }
func TestProbeOnlyDoesNotCreateDatabase(t *testing.T) {
	c := testConfig(t)
	c.Control, c.WebSocket, c.NativeListen = false, false, ""
	r, err := Start(c)
	if err != nil {
		t.Fatal(err)
	}
	defer r.Close()
	if r.HTTPAddr != nil {
		t.Fatal("probe-only opened HTTP")
	}
	if _, err = os.Stat(c.State); !os.IsNotExist(err) {
		t.Fatal("probe-only touched database", err)
	}
	checkProbes(t, r)
}
func TestAllFeaturesRestartAndGracefulSocketShutdown(t *testing.T) {
	c := testConfig(t)
	web := t.TempDir()
	c.Web = web
	for name, content := range map[string]string{"index.html": "<title>ReignHub</title>", "nsgo.json": "{}", "app.0123456789.part-000.bin": "part"} {
		if err := os.WriteFile(filepath.Join(web, name), []byte(content), 0600); err != nil {
			t.Fatal(err)
		}
	}
	r, err := Start(c)
	if err != nil {
		t.Fatal(err)
	}
	defer r.Close()
	base := "http://" + r.HTTPAddr.String()
	for _, path := range []string{"/healthz", "/chat/", "/assets/chat/nsgo.json", "/api/account"} {
		res, err := http.Get(base + path)
		if err != nil {
			t.Fatal(err)
		}
		_, _ = io.Copy(io.Discard, res.Body)
		res.Body.Close()
		if res.StatusCode != 200 {
			t.Fatal(path, res.StatusCode)
		}
	}
	checkProbes(t, r)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	identity, _ := device.NewIdentity("fixture")
	identity, err = device.Register(ctx, base, identity, true)
	if err != nil {
		t.Fatal(err)
	}
	ws, _, err := websocket.Dial(ctx, "ws://"+r.HTTPAddr.String()+"/p2p/signal?id="+identity.ID+"&role=host", &websocket.DialOptions{HTTPHeader: http.Header{"Authorization": {"Bearer " + identity.Token}}})
	if err != nil {
		t.Fatal(err)
	}
	defer ws.CloseNow()
	if _, _, err = ws.Read(ctx); err != nil {
		t.Fatal(err)
	}
	key := r.directory.Network.GrantPublicKey
	sid := r.directory.ServiceID
	done := make(chan struct{})
	go func() { _ = r.Close(); close(done) }()
	select {
	case <-done:
	case <-ctx.Done():
		t.Fatal("shutdown blocked on open WebSocket")
	}
	if _, _, err = ws.Read(ctx); err == nil {
		t.Fatal("shutdown left WebSocket open")
	}
	next, err := Start(c)
	if err != nil {
		t.Fatal(err)
	}
	defer next.Close()
	if next.directory.Network.GrantPublicKey != key || next.directory.ServiceID != sid {
		t.Fatal("restart rotated service identity")
	}
	if _, ok := next.directory.Authenticate(identity.Token); !ok {
		t.Fatal("restart lost registration")
	}
}
func TestBindFailureRollsBackOtherListeners(t *testing.T) {
	occupied, err := net.ListenPacket("udp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer occupied.Close()
	reserve, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	c := testConfig(t)
	c.Listen = reserve.Addr().String()
	_ = reserve.Close()
	c.STUNListen = occupied.LocalAddr().String()
	if _, err = Start(c); err == nil {
		t.Fatal("ignored UDP bind failure")
	}
	listener, err := net.Listen("tcp", c.Listen)
	if err != nil {
		t.Fatal("HTTP leaked after startup failure", err)
	}
	listener.Close()
}
func readTestConfig(t *testing.T, services string) Config {
	t.Helper()
	file := filepath.Join(t.TempDir(), "config.json")
	if err := os.WriteFile(file, []byte(`{"services":`+services+`}`), 0600); err != nil {
		t.Fatal(err)
	}
	c, err := loadConfig(file)
	if err != nil {
		t.Fatal(err)
	}
	return c
}

func TestConfiguredListenersAndRoutes(t *testing.T) {
	const loop = "127.0.0.1:0"
	for _, tc := range []struct {
		name, services                       string
		control, udp, tcp, native, websocket bool
	}{
		{"defaults", `{}`, true, true, true, true, true},
		{"explicit defaults", `{"control":{"enable":true},"stun":{},"relay":{}}`, true, true, true, true, true},
		{"control only", `{"stun":{"enable":false},"relay":{"enable":false}}`, true, false, false, false, false},
		{"dual STUN", `{"control":{"enable":false},"relay":{"enable":false}}`, false, true, true, false, false},
		{"UDP only", `{"control":{"enable":false},"stun":{"tcp":{"enable":false}},"relay":{"enable":false}}`, false, true, false, false, false},
		{"TCP only", `{"control":{"enable":false},"stun":{"udp":{"enable":false}},"relay":{"enable":false}}`, false, false, true, false, false},
		{"local native", `{"stun":{"enable":false},"relay":{"websocket":false}}`, true, false, false, true, false},
		{"local WebSocket", `{"stun":{"enable":false},"relay":{"native":{"enable":false}}}`, true, false, false, false, true},
		{"node", `{"control":{"enable":false}}`, false, true, true, true, true},
		{"WSS only", `{"control":{"enable":false},"stun":{"enable":false},"relay":{"native":{"enable":false}}}`, false, false, false, false, true},
		{"disabled group wins", `{"stun":{"enable":false,"udp":{"enable":true}},"relay":{"enable":false,"websocket":true}}`, true, false, false, false, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			c := readTestConfig(t, tc.services)
			for _, addr := range []*string{&c.Listen, &c.NativeListen, &c.STUNListen, &c.TCPSTUNListen} {
				if *addr != "" {
					*addr = loop
				}
			}
			r, err := Start(c)
			if err != nil {
				t.Fatal(err)
			}
			defer r.Close()
			if (r.HTTPAddr != nil) != (tc.control || tc.websocket) || (r.UDPAddr != nil) != tc.udp || (r.TCPAddr != nil) != tc.tcp || (r.native != nil) != tc.native {
				t.Fatal("unexpected listeners", r.HTTPAddr, r.UDPAddr, r.TCPAddr)
			}
			_, err = os.Stat(c.State)
			if (tc.control || tc.native || tc.websocket) != (err == nil) {
				t.Fatal("unexpected database", err)
			}
			if r.HTTPAddr == nil {
				return
			}
			for path, available := range map[string]bool{"/healthz": true, "/chat/": tc.control, "/api/account": tc.control, "/p2p/config": tc.control, "/p2p/signal": tc.control, "/p2p/relay": tc.control && tc.websocket, "/p2p/node": tc.websocket} {
				res, err := http.Get("http://" + r.HTTPAddr.String() + path)
				if err != nil {
					t.Fatal(err)
				}
				res.Body.Close()
				if (res.StatusCode != http.StatusNotFound) != available {
					t.Errorf("%s: %d", path, res.StatusCode)
				}
			}
		})
	}
}

func TestControlSTUNServers(t *testing.T) {
	for _, tc := range []struct {
		control  string
		udp, tcp []string
	}{
		{`{}`, []string{}, nil},
		{`{"stunServers":[]}`, []string{}, nil},
		{`{"stunServers":["udp:stun.cloudflare.com:3478","probe.example:26331","tcp:[::1]:3479","probe.example:26331"]}`, []string{"stun:probe.example:26331"}, []string{"probe.example:26331", "[::1]:3479"}},
	} {
		c := readTestConfig(t, `{"stun":{"enable":false},"relay":{"enable":false},"control":`+tc.control+`}`)
		c.Listen = "127.0.0.1:0"
		r, err := Start(c)
		if err != nil {
			t.Fatal(err)
		}
		policy := device.Network(t.Context(), "http://"+r.HTTPAddr.String())
		r.Close()
		if policy.ServiceID == "" || !slices.Equal(policy.PublicSTUN, []string{"stun:stun.cloudflare.com:3478", "stun:stun.l.google.com:19302", "stun:stun.chat.bilibili.com:3478"}) || !slices.Equal(policy.SelfSTUN, tc.udp) || !slices.Equal(policy.TCPSTUN, tc.tcp) {
			t.Fatal(tc.control, policy)
		}
	}
}

func TestPrintRelayCredentialMatchesRunningAndStoppedRelay(t *testing.T) {
	c := testConfig(t)
	if _, err := deviceservice.ReadRelayCredential(c.State); err == nil {
		t.Fatal("missing state returned a credential")
	}
	if _, err := os.Stat(filepath.Dir(c.State)); !os.IsNotExist(err) {
		t.Fatal("credential lookup created state", err)
	}

	r, err := Start(c)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { r.Close() })
	credential, err := deviceservice.ReadRelayCredential(c.State)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	parts := strings.Split(credential, ".")
	if len(parts) != 3 || parts[0] != "rh1" {
		t.Fatal("invalid node credential format")
	}
	dialer := tls.Dialer{Config: nativepath.ClientTLS(parts[1])}
	conn, err := dialer.DialContext(ctx, "tcp", r.native.Addr().String())
	if err != nil {
		t.Fatal("printed pin did not authenticate the running relay", err)
	}
	conn.Close()
	r.Close()

	offline, err := deviceservice.ReadRelayCredential(c.State)
	if err != nil || offline != credential {
		t.Fatal("could not read the same node credential while stopped", err)
	}
}
