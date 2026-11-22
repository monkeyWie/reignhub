package remote

import (
	"bufio"
	"context"
	"crypto/rand"
	"encoding/json"
	"github.com/monkeyWie/reignhub/internal/transport"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/coder/websocket"
	"github.com/monkeyWie/reignhub/internal/control"
	"github.com/monkeyWie/reignhub/internal/provider"
	"github.com/monkeyWie/reignhub/internal/relay"
)

func TestRelayBusyReadRespondsAndCancellationReleasesHost(t *testing.T) {
	token := strings.Repeat("r", 32)
	server := httptest.NewServer(relay.New(token))
	defer server.Close()
	pair := Pairing{Relay: "ws" + strings.TrimPrefix(server.URL, "http"), Host: relay.ID(), Key: strings.Repeat("ab", 32), RelayToken: token}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	p := &waitingProvider{testProvider: &testProvider{}, started: make(chan context.Context, 8), release: make(chan struct{})}
	monitor := NewHostStatus(pair.Relay)
	go serveHostObserved(ctx, pair, Options{RelayOnly: true}, &control.Dispatcher{Providers: map[string]provider.Provider{"test": p}}, monitor)
	waitFor(t, func() bool { return monitor.Snapshot().State == "connected" })
	c, err := Connect(ctx, pair, Options{RelayOnly: true})
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	var stops []context.CancelFunc
	done := make(chan error, 8)
	for range 8 {
		readCtx, stop := context.WithCancel(ctx)
		stops = append(stops, stop)
		defer stop()
		go func() {
			done <- c.Call(readCtx, control.Request{Method: "transcript", Provider: "test", Session: "slow"}, nil)
		}()
	}
	var hostContexts []context.Context
	for range 8 {
		select {
		case hostCtx := <-p.started:
			hostContexts = append(hostContexts, hostCtx)
		case <-ctx.Done():
			t.Fatal("eight reads not dispatched")
		}
	}
	short, stop := context.WithTimeout(ctx, time.Second)
	defer stop()
	err = c.Call(short, control.Request{Method: "providers"}, nil)
	if err == nil || !strings.Contains(err.Error(), "繁忙") {
		t.Fatalf("full host must respond immediately with busy, got %v", err)
	}
	if err := c.Call(short, control.Request{Method: "network"}, nil); err != nil {
		t.Fatalf("liveness probe blocked behind Agent reads: %v", err)
	}
	for _, stop := range stops {
		stop()
	}
	for _, hostCtx := range hostContexts {
		select {
		case <-hostCtx.Done():
		case <-time.After(time.Second):
			t.Fatal("client read cancellation did not reach host")
		}
	}
	for range 8 {
		select {
		case err := <-done:
			if err == nil {
				t.Fatal("cancelled read succeeded")
			}
		case <-ctx.Done():
			t.Fatal("read leaked")
		}
	}
	// Wait until goroutine cleanup returns its slot. The same connection stays live.
	waitFor(t, func() bool {
		short, stop := context.WithTimeout(ctx, time.Second)
		defer stop()
		return c.Call(short, control.Request{Method: "network"}, nil) == nil
	})
	if c.Path() != "Relay · WebSocket" {
		t.Fatal("cancelling reads disconnected relay", c.Path())
	}
}

func TestWatchesForDistinctSessionsCoexistAndWritesCannotBeCancelled(t *testing.T) {
	var reads hostReads
	add := func(id, method, session string) (context.Context, *hostRead) {
		ctx, stop := context.WithCancel(context.Background())
		t.Cleanup(stop)
		v, ok := reads.add(control.Request{ID: id, Method: method, Provider: "test", Session: session}, stop)
		if !ok {
			t.Fatal("registration failed")
		}
		return ctx, v
	}
	first, old := add("a", "watch", "one")
	second, _ := add("b", "watch", "two")
	replacement, _ := add("c", "watch", "one")
	if first.Err() == nil || second.Err() != nil || replacement.Err() != nil {
		t.Fatal("watch replacement cancelled unrelated session")
	}
	reads.remove("a", old)
	reads.cancel("c")
	if replacement.Err() == nil || second.Err() != nil {
		t.Fatal("old cleanup lost the replacement or another session")
	}
	write, _ := add("write", "send", "one")
	reads.cancel("write")
	if write.Err() != nil {
		t.Fatal("read cancellation cancelled a write")
	}
}

func TestCancelledResponseDropsFragmentsAndAcceptsLiveMessage(t *testing.T) {
	secret := strings.Repeat("ab", 32)
	sender, _ := newCryptor(secret)
	receiver, _ := newCryptor(secret)
	pending := map[string]bool{"history": true, "live": true}
	var result string
	w := &wire{crypto: receiver, peer: "peer", chunks: map[string]*assembly{}, wantsResponse: func(id string) bool { return pending[id] }, onData: func(b []byte) { result = string(b) }}
	receive := func(request, id string, index, total int, data string) {
		t.Helper()
		raw, _ := json.Marshal(chunk{Encoding: transport.Identity, RawSize: len(data), Request: request, ID: id, Index: index, Total: total, Data: []byte(data)})
		sealed, err := sender.seal(w.aad("chunk", false), raw)
		if err != nil {
			t.Fatal(err)
		}
		w.onChunk(sealed)
	}
	receive("history", "large", 0, 3, "old")
	delete(pending, "history")
	w.discardResponse("history")
	receive("history", "large", 1, 3, "discard")
	if len(w.chunks) != 0 || w.chunkBytes != 0 {
		t.Fatal("cancelled response retained fragments")
	}
	receive("live", "small", 0, 1, "new user message")
	if result != "new user message" {
		t.Fatal("live response did not arrive", result)
	}
	receive("", "unscoped", 0, 1, "invalid response")
	if result != "new user message" {
		t.Fatal("unscoped fragment was accepted")
	}
}

// Slow each actual socket write, so a large response cannot merely disappear
// into a local OS buffer before the competing live message is sent.
type pacedConn struct {
	net.Conn
	started chan struct{}
}

func (c *pacedConn) Write(b []byte) (int, error) {
	select {
	case c.started <- struct{}{}:
	default:
	}
	time.Sleep(time.Duration(len(b)) * time.Second / (32 << 10))
	return c.Conn.Write(b)
}

type pacedResponse struct {
	http.ResponseWriter
	started chan struct{}
}

func (w pacedResponse) Hijack() (net.Conn, *bufio.ReadWriter, error) {
	c, rw, err := w.ResponseWriter.(http.Hijacker).Hijack()
	if err != nil {
		return nil, nil, err
	}
	p := &pacedConn{Conn: c, started: w.started}
	return p, bufio.NewReadWriter(rw.Reader, bufio.NewWriter(p)), nil
}
func TestSmallRelayResponsePassesLargeHistory(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	accepted := make(chan *websocket.Conn, 1)
	started := make(chan struct{}, 1)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		ws, err := websocket.Accept(pacedResponse{w, started}, r, nil)
		if err != nil {
			return
		}
		accepted <- ws
		<-ctx.Done()
		ws.CloseNow()
	}))
	defer server.Close()
	ws, _, err := websocket.Dial(ctx, "ws"+strings.TrimPrefix(server.URL, "http"), nil)
	if err != nil {
		t.Fatal(err)
	}
	defer ws.CloseNow()
	ws.SetReadLimit(1 << 20)
	hostWS := <-accepted
	select {
	case <-started: // Discard the HTTP upgrade write.
	default:
	}
	secret := strings.Repeat("ab", 32)
	crypto, _ := newCryptor(secret)
	receiver, _ := newCryptor(secret)
	w := &wire{ctx: ctx, crypto: crypto, host: true, peer: "peer", relayLink: &relayLink{}}
	w.relayLink.socket.Store(hostWS)
	history := make([]byte, 2<<20)
	if _, err := rand.Read(history); err != nil {
		t.Fatal(err)
	}
	totalFrames := (len(history) + transport.ChunkSize - 1) / transport.ChunkSize
	largeDone := make(chan error, 1)
	go func() { largeDone <- w.sendContext(ctx, "history", history) }()
	<-started
	begin := time.Now()
	smallDone := make(chan error, 1)
	go func() { smallDone <- w.sendContext(ctx, "live", []byte("live update")) }()
	parts := 0
	sawSmall := false
	for !sawSmall {
		_, raw, err := ws.Read(ctx)
		if err != nil {
			t.Fatal(err)
		}
		var f relay.Frame
		if err := json.Unmarshal(raw, &f); err != nil {
			t.Fatal(err)
		}
		plain, err := receiver.open(w.aad("chunk", true), f.Body)
		if err != nil {
			t.Fatal(err)
		}
		var c chunk
		_ = json.Unmarshal(plain, &c)
		parts++
		if string(c.Data) == "live update" {
			sawSmall = true
		}
		if parts >= totalFrames {
			t.Fatal("small response queued behind the whole history")
		}
	}
	if err := <-smallDone; err != nil {
		t.Fatal(err)
	}
	if time.Since(begin) > time.Second {
		t.Fatalf("live message delayed %s on simulated 32 KiB/s relay", time.Since(begin))
	}
	t.Logf("32 KiB/s relay: live message arrived in %s after %d frames, before the %d-frame history finished", time.Since(begin), parts, totalFrames)
	// Stop the unfinished history without closing a shared socket mid-frame.
	cancel()
	select {
	case <-largeDone:
	case <-time.After(time.Second):
		t.Fatal("large sender did not stop")
	}
}
