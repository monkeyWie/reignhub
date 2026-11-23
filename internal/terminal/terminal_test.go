package terminal

import (
	"context"
	"errors"
	"io"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

type fixtureEngine struct {
	mu     sync.Mutex
	done   chan struct{}
	once   sync.Once
	text   string
	dirty  bool
	inputs int
	size   Size
}

func newFixtureEngine() *fixtureEngine {
	return &fixtureEngine{done: make(chan struct{}), text: "fixture prompt", dirty: true}
}
func (e *fixtureEngine) Resize(s Size) { e.mu.Lock(); e.size = s; e.dirty = true; e.mu.Unlock() }
func (e *fixtureEngine) Frame(reset bool) []byte {
	e.mu.Lock()
	defer e.mu.Unlock()
	if !reset && !e.dirty {
		return nil
	}
	e.dirty = false
	return []byte(e.text)
}
func (e *fixtureEngine) Send(b []byte) {
	e.mu.Lock()
	e.inputs++
	e.text += string(b)
	e.dirty = true
	e.mu.Unlock()
}
func (e *fixtureEngine) Done() <-chan struct{} { return e.done }
func (e *fixtureEngine) ExitCode() int         { return 0 }
func (e *fixtureEngine) Close() error          { e.once.Do(func() { close(e.done) }); return nil }
func fixture(t *testing.T) (*Manager, *fixtureEngine, Scope, Params, *atomic.Int32) {
	t.Helper()
	e := newFixtureEngine()
	count := &atomic.Int32{}
	m := New(context.Background(), func(context.Context, string, Size, string) (Engine, error) { count.Add(1); return e, nil })
	t.Cleanup(func() { _ = m.Close() })
	return m, e, Scope{"controller-a", "agent", "conversation"}, Params{ID: strings.Repeat("ab", 32), Size: Size{Cols: 80, Rows: 24}}, count
}
func openFixture(t *testing.T, m *Manager, s Scope, p Params) Snapshot {
	t.Helper()
	out, err := m.Open(context.Background(), s, p, func(context.Context) (string, error) { return "fixture", nil })
	if err != nil {
		t.Fatal(err)
	}
	return out
}
func TestTerminalOwnershipReplayAndRecovery(t *testing.T) {
	m, e, s, p, count := fixture(t)
	first := openFixture(t, m, s, p)
	p.Attachment, p.After = first.Attachment, first.Sequence
	p.Data, p.InputSeq = []byte(" input"), 1
	if err := m.Input(context.Background(), s, p); err != nil {
		t.Fatal(err)
	}
	if err := m.Input(context.Background(), s, p); err == nil {
		t.Fatal("duplicate input executed")
	}
	updated, err := m.Read(context.Background(), s, p)
	if err != nil {
		t.Fatal(err)
	}
	replayed, err := m.Read(context.Background(), s, p)
	if err != nil || string(replayed.Data) != string(updated.Data) || replayed.Sequence != updated.Sequence {
		t.Fatal("lost response consumed output", replayed, err)
	}
	other := s
	other.Owner = "controller-b"
	if _, err := m.Attach(context.Background(), other, p); !errors.Is(err, ErrMissing) {
		t.Fatal("ownership escaped", err)
	}
	other = s
	other.Session = "another-session"
	if err := m.Terminate(context.Background(), other, p); !errors.Is(err, ErrMissing) {
		t.Fatal("another session terminated terminal")
	}
	if err := m.Detach(context.Background(), s, p); err != nil {
		t.Fatal(err)
	}
	select {
	case <-e.Done():
		t.Fatal("detach killed process")
	default:
	}
	second := openFixture(t, m, s, p)
	if count.Load() != 1 || second.Attachment == first.Attachment || !strings.Contains(string(second.Data), " input") {
		t.Fatal("reattach spawned or lost screen")
	}
	if err := m.Input(context.Background(), s, p); !errors.Is(err, ErrDetached) {
		t.Fatal("old attachment wrote", err)
	}
	p.Attachment, p.After = second.Attachment, second.Sequence
	p.Size = Size{Cols: 120, Rows: 35, CellWidth: 8, CellHeight: 16}
	if err := m.Resize(context.Background(), s, p); err != nil {
		t.Fatal(err)
	}
	out, err := m.Read(context.Background(), s, p)
	if err != nil || out.Size != p.Size {
		t.Fatal("resize not propagated", out, err)
	}
	if err := m.Terminate(context.Background(), s, p); err != nil {
		t.Fatal(err)
	}
	select {
	case <-e.Done():
	default:
		t.Fatal("explicit end did not close process")
	}
	if _, err := m.Open(context.Background(), s, p, nil); !errors.Is(err, ErrMissing) || count.Load() != 1 {
		t.Fatal("delayed open recreated a terminated process", err)
	}
}
func TestTerminalCancellationDoesNotStopShell(t *testing.T) {
	m, e, s, p, _ := fixture(t)
	initial := openFixture(t, m, s, p)
	p.Attachment, p.After = initial.Attachment, initial.Sequence
	ctx, cancel := context.WithTimeout(context.Background(), 40*time.Millisecond)
	defer cancel()
	_, err := m.Read(ctx, s, p)
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatal(err)
	}
	select {
	case <-e.Done():
		t.Fatal("cancel killed shell")
	default:
	}
	e.Send([]byte("after cancellation"))
	out, err := m.Read(context.Background(), s, p)
	if err != nil || !strings.Contains(string(out.Data), "after cancellation") {
		t.Fatal("read failed to recover", err)
	}
}

func TestTerminalCloseBeforeDelayedOpen(t *testing.T) {
	m, _, s, p, count := fixture(t)
	if err := m.Terminate(context.Background(), s, p); err != nil {
		t.Fatal(err)
	}
	if _, err := m.Open(context.Background(), s, p, nil); !errors.Is(err, ErrMissing) || count.Load() != 0 {
		t.Fatal("delayed open started after explicit close", err)
	}
}

type asynchronousCloseEngine struct {
	*fixtureEngine
	started chan struct{}
	calls   atomic.Int32
}

func (e *asynchronousCloseEngine) Close() error {
	if e.calls.Add(1) == 1 {
		close(e.started)
	}
	return nil
}

func TestTerminalCloseWaitsForExitAndRetainsScopedConfirmation(t *testing.T) {
	engine := &asynchronousCloseEngine{fixtureEngine: newFixtureEngine(), started: make(chan struct{})}
	m := New(context.Background(), func(context.Context, string, Size, string) (Engine, error) { return engine, nil })
	t.Cleanup(func() { _ = m.Close() })
	t.Cleanup(func() { _ = engine.fixtureEngine.Close() })
	scope := Scope{"owner", "agent", "conversation"}
	p := Params{ID: strings.Repeat("ab", 32), Size: Size{Cols: 80, Rows: 24}}
	snapshot := openFixture(t, m, scope, p)
	if err := m.CloseStatus(context.Background(), scope, p); !errors.Is(err, ErrCloseUnconfirmed) {
		t.Fatal("confirmed a close that never arrived", err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	request, disconnect := context.WithCancel(ctx)
	defer disconnect()
	done := make(chan error, 1)
	go func() { done <- m.Terminate(request, scope, p) }()
	select {
	case <-engine.started:
	case <-ctx.Done():
		t.Fatal("termination never reached the engine")
	}
	p.Attachment, p.After = snapshot.Attachment, snapshot.Sequence
	if _, err := m.Read(ctx, scope, p); !errors.Is(err, ErrClosing) {
		t.Fatal("in-progress close should be distinct from a missing terminal", err)
	}
	probe, stop := context.WithTimeout(ctx, 20*time.Millisecond)
	defer stop()
	if err := m.CloseStatus(probe, scope, p); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatal("confirmed before the process exited", err)
	}
	disconnect()
	if err := <-done; !errors.Is(err, context.Canceled) {
		t.Fatal("close request could not stop waiting", err)
	}
	_ = engine.fixtureEngine.Close()
	if err := m.CloseStatus(ctx, scope, p); err != nil || engine.calls.Load() != 1 {
		t.Fatal("exit was not confirmed without a second close", err)
	}
	other := Scope{"another-owner", scope.Provider, scope.Session}
	if err := m.CloseStatus(ctx, other, p); !errors.Is(err, ErrMissing) {
		t.Fatal("close acknowledgement leaked to another owner", err)
	}
}
func TestTerminalCreationLimitsAndRetryAfterFailure(t *testing.T) {
	m, _, s, p, _ := fixture(t)
	_, err := m.Open(context.Background(), s, p, func(context.Context) (string, error) { return "", errors.New("missing directory") })
	if err == nil {
		t.Fatal("accepted invalid cwd")
	}
	openFixture(t, m, s, p)
	if _, err := m.Open(context.Background(), s, Params{ID: p.ID, Size: Size{Cols: 9999, Rows: 5}}, nil); err == nil {
		t.Fatal("accepted oversized grid")
	}
	m.mu.Lock()
	for i := 1; i < MaxTerminals; i++ {
		m.entries[strings.Repeat("x", i)] = &entry{scope: s, ready: closedChannel(), engine: newFixtureEngine()}
	}
	m.mu.Unlock()
	p.ID = strings.Repeat("cd", 32)
	if _, err := m.Open(context.Background(), s, p, nil); err == nil {
		t.Fatal("unbounded shell count")
	}
}
func closedChannel() chan struct{} { c := make(chan struct{}); close(c); return c }

func TestTerminalConnDoesNotReplayUncertainInput(t *testing.T) {
	id := strings.Repeat("aa", 32)
	calls := 0
	call := func(ctx context.Context, method string, p Params) (Snapshot, error) {
		switch method {
		case "terminal.open":
			return Snapshot{ID: id, Attachment: "fixture", Size: Size{Cols: 80, Rows: 24}, Sequence: 1, Data: []byte("ready")}, nil
		case "terminal.input":
			calls++
			return Snapshot{}, errors.New("acknowledgement lost after execution")
		case "terminal.read":
			<-ctx.Done()
			return Snapshot{}, ctx.Err()
		default:
			return Snapshot{}, nil
		}
	}
	c, err := OpenConn(context.Background(), context.Background(), call, id, "")
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	failure := make(chan error, 1)
	c.Bind(func(Size) {}, func(err error, _ bool) { failure <- err })
	b := make([]byte, 5)
	if _, err := io.ReadFull(c, b); err != nil || string(b) != "ready" {
		t.Fatal(err, string(b))
	}
	if _, err := c.Write([]byte("run\r")); err == nil {
		t.Fatal("ambiguous input reported success")
	}
	if _, err := c.Write([]byte("run\r")); err == nil || calls != 1 {
		t.Fatal("input retried after uncertainty", calls)
	}
	select {
	case err := <-failure:
		if !errors.Is(err, ErrInputUnconfirmed) {
			t.Fatal(err)
		}
	case <-time.After(time.Second):
		t.Fatal("missing uncertain-write feedback")
	}
}

func TestTerminalConnReadExitResizeAndDetach(t *testing.T) {
	m, e, s, p, _ := fixture(t)
	call := func(ctx context.Context, method string, p Params) (Snapshot, error) {
		switch method {
		case "terminal.open":
			return m.Open(ctx, s, p, func(context.Context) (string, error) { return "fixture", nil })
		case "terminal.attach":
			return m.Attach(ctx, s, p)
		case "terminal.read":
			return m.Read(ctx, s, p)
		case "terminal.input":
			return Snapshot{}, m.Input(ctx, s, p)
		case "terminal.resize":
			return Snapshot{}, m.Resize(ctx, s, p)
		case "terminal.detach":
			return Snapshot{}, m.Detach(ctx, s, p)
		}
		return Snapshot{}, errors.New("unexpected operation")
	}
	c, err := OpenConn(context.Background(), context.Background(), call, p.ID, "")
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	ended := make(chan struct{}, 1)
	c.Bind(func(Size) {}, func(err error, exit bool) {
		if exit {
			ended <- struct{}{}
		}
	})
	buf := make([]byte, len("fixture prompt"))
	if _, err := io.ReadFull(c, buf); err != nil {
		t.Fatal(err)
	}
	if _, err := c.Write([]byte("one")); err != nil {
		t.Fatal(err)
	}
	buf = make([]byte, len("fixture promptone"))
	if _, err := io.ReadFull(c, buf); err != nil || string(buf) != "fixture promptone" {
		t.Fatal(err, string(buf))
	}
	_ = e.Close()
	done := make(chan error, 1)
	go func() { _, err := c.Read(make([]byte, 32)); done <- err }()
	select {
	case <-ended:
	case <-time.After(time.Second):
		t.Fatal("missing exit status")
	}
	select {
	case <-done:
		t.Fatal("exited screen discarded")
	default:
	}
	_ = c.Close()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("read did not cancel")
	}
}

func TestTerminalConcurrentReadFailureDoesNotReplayPendingInput(t *testing.T) {
	id := strings.Repeat("ff", 32)
	writing := make(chan struct{})
	var writes atomic.Int32
	call := func(ctx context.Context, method string, p Params) (Snapshot, error) {
		switch method {
		case "terminal.open":
			return Snapshot{ID: id, Attachment: "fixture", Sequence: 1, Size: Size{Cols: 80, Rows: 24}}, nil
		case "terminal.input":
			writes.Add(1)
			close(writing)
			<-ctx.Done()
			return Snapshot{}, ctx.Err()
		case "terminal.read":
			<-writing
			return Snapshot{}, errors.New("connection lost while a write may have executed")
		}
		return Snapshot{}, nil
	}
	c, err := OpenConn(context.Background(), context.Background(), call, id, "")
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	failure := make(chan error, 2)
	c.Bind(func(Size) {}, func(err error, _ bool) { failure <- err })
	writeDone := make(chan error, 1)
	go func() { _, err := c.Write([]byte("command\r")); writeDone <- err }()
	if _, err := c.Read(make([]byte, 32)); err == nil {
		t.Fatal("read reported success")
	}
	select {
	case err := <-failure:
		if !errors.Is(err, ErrInputUnconfirmed) {
			t.Fatal("lost uncertain input status", err)
		}
	case <-time.After(time.Second):
		t.Fatal("missing feedback")
	}
	select {
	case err := <-writeDone:
		if err == nil {
			t.Fatal("write reported success")
		}
	case <-time.After(time.Second):
		t.Fatal("write did not cancel")
	}
	if _, err := c.Write([]byte("command\r")); err == nil || writes.Load() != 1 {
		t.Fatal("uncertain input replayed")
	}
}

type delayedCloseEngine struct {
	*fixtureEngine
	closed chan struct{}
}

func (e *delayedCloseEngine) Close() error { close(e.closed); return nil }
func TestTerminalShutdownWaitsForAllProcesses(t *testing.T) {
	var engines []*delayedCloseEngine
	m := New(context.Background(), func(context.Context, string, Size, string) (Engine, error) {
		e := &delayedCloseEngine{newFixtureEngine(), make(chan struct{})}
		engines = append(engines, e)
		return e, nil
	})
	for _, id := range []string{strings.Repeat("11", 32), strings.Repeat("22", 32)} {
		openFixture(t, m, Scope{"owner", "agent", "session"}, Params{ID: id, Size: Size{Cols: 80, Rows: 24}})
	}
	done := make(chan error, 1)
	go func() { done <- m.Close() }()
	for _, e := range engines {
		select {
		case <-e.closed:
		case <-time.After(time.Second):
			t.Fatal("shutdown waited on one process before closing another")
		}
	}
	select {
	case <-done:
		t.Fatal("shutdown returned before processes exited")
	default:
	}
	for _, e := range engines {
		_ = e.fixtureEngine.Close()
	}
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(time.Second):
		t.Fatal("shutdown did not finish")
	}
}
