//go:build darwin || linux || freebsd || openbsd || netbsd || dragonfly

package codex

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"

	"github.com/monkeyWie/reignhub/internal/codexipc"
	"github.com/monkeyWie/reignhub/internal/config"
	"github.com/monkeyWie/reignhub/internal/events"
	"github.com/monkeyWie/reignhub/internal/rpc"
)

// The harmless stdio fixture takes a real lock, then exits on stdin EOF. This
// verifies OS-level release after both orderly close and abrupt parent death.
func init() {
	runtimeFixtureSetup = func() error {
		path := os.Getenv("REIGN_TEST_WRITER_LOCK")
		if path == "" {
			return nil
		}
		f, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR, 0600)
		if err != nil {
			return err
		}
		if err = syscall.Flock(int(f.Fd()), syscall.LOCK_EX); err != nil {
			return err
		}
		// Keep the descriptor reachable for this short-lived fixture process.
		runtimeFixtureHeldFile = f
		return nil
	}
}

var runtimeFixtureHeldFile *os.File

func TestIPCLockedButUndiscoverableThreadIsNeverResumed(t *testing.T) {
	p := New(config.Config{}, "", events.New())
	backendFixture(t, p, func(m rpc.Message) (any, *rpc.Error) {
		t.Errorf("locked session reached private executor: %s", m.Method)
		return nil, nil
	})
	f := attachIPCFixture(t, p, func(m codexipc.Message) (codexipc.Message, bool) {
		if m.Method == "thread-owner-discovery" {
			return codexipc.Message{ResultType: "error", Error: "no-client-found"}, true
		}
		return codexipc.Message{}, false
	})
	dir := filepath.Join(f.b.home, "thread-writer-locks")
	if err := os.MkdirAll(dir, 0700); err != nil {
		t.Fatal(err)
	}
	file, err := os.Create(filepath.Join(dir, "held.lock"))
	if err != nil {
		t.Fatal(err)
	}
	defer file.Close()
	if err = syscall.Flock(int(file.Fd()), syscall.LOCK_EX); err != nil {
		t.Fatal(err)
	}
	ctx, stop := context.WithTimeout(context.Background(), 3*time.Second)
	defer stop()
	if err = p.Send(ctx, "held", "continue"); err == nil || !strings.Contains(err.Error(), "不会强行接管") {
		t.Fatalf("unsafe route: %v", err)
	}
}
func TestIPCOwnerTimeoutThenWriterRelease(t *testing.T) {
	p := New(config.Config{}, "", events.New())
	var mu sync.Mutex
	var calls []string
	backendFixture(t, p, func(m rpc.Message) (any, *rpc.Error) {
		mu.Lock()
		calls = append(calls, m.Method)
		mu.Unlock()
		return map[string]any{}, nil
	})
	f := attachIPCFixture(t, p, func(m codexipc.Message) (codexipc.Message, bool) {
		if m.Method == "thread-owner-discovery" {
			return codexipc.Message{ResultType: "error", Error: "request-timeout"}, true
		}
		return codexipc.Message{}, false
	})
	dir := filepath.Join(f.b.home, "thread-writer-locks")
	if err := os.MkdirAll(dir, 0700); err != nil {
		t.Fatal(err)
	}
	file, err := os.Create(filepath.Join(dir, "held.lock"))
	if err != nil {
		t.Fatal(err)
	}
	defer file.Close()
	if err := syscall.Flock(int(file.Fd()), syscall.LOCK_EX); err != nil {
		t.Fatal(err)
	}
	ctx, stop := context.WithTimeout(context.Background(), 4*time.Second)
	defer stop()
	err = p.Send(ctx, "held", "first attempt")
	var discovery *ownerDiscoveryError
	var unknown *codexipc.UnknownOutcome
	if !errors.As(err, &discovery) || errors.As(err, &unknown) || !f.b.needsSharedDesktop() {
		t.Fatalf("expected a definite pre-submit failure and recovery action: %v", err)
	}
	mu.Lock()
	count := len(calls)
	mu.Unlock()
	if count != 0 {
		t.Fatal("the held writer received a competing resume or turn")
	}
	if err := syscall.Flock(int(file.Fd()), syscall.LOCK_UN); err != nil {
		t.Fatal(err)
	}
	if err := p.Send(ctx, "held", "retry"); err != nil {
		t.Fatal(err)
	}
	mu.Lock()
	defer mu.Unlock()
	if !reflect.DeepEqual(calls, []string{"thread/resume", "turn/start"}) {
		t.Fatalf("expected one resume and one turn for the unlocked thread, got %v", calls)
	}
}

func TestPrivateRuntimeParentFixture(t *testing.T) {
	if os.Getenv("REIGN_TEST_STDIO_PARENT") != "1" {
		return
	}
	cmd := exec.Command(os.Args[0], "-test.run=^$")
	cmd.Env = append(os.Environ(), "REIGN_TEST_STDIO_CODEX=1", "REIGN_TEST_STDIO_PARENT=0")
	ctx, stop := context.WithTimeout(context.Background(), 20*time.Second)
	defer stop()
	c, err := rpc.StartProcess(ctx, cmd, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	if err = os.WriteFile(os.Getenv("REIGN_TEST_PARENT_READY"), []byte("ready"), 0600); err != nil {
		t.Fatal(err)
	}
	<-ctx.Done()
}
func TestPrivateRuntimeParentDeathReleasesWriterLock(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "writer.lock")
	ready := filepath.Join(dir, "ready")
	cmd := exec.Command(os.Args[0], "-test.run=^TestPrivateRuntimeParentFixture$")
	cmd.Env = append(os.Environ(), "REIGN_TEST_STDIO_PARENT=1", "REIGN_TEST_WRITER_LOCK="+path, "REIGN_TEST_PARENT_READY="+ready)
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = cmd.Process.Kill() })
	deadline := time.Now().Add(5 * time.Second)
	for {
		if _, err := os.Stat(ready); err == nil {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("fixture parent not ready")
		}
		time.Sleep(5 * time.Millisecond)
	}
	if held, err := codexipc.WriterLocked(path); !held || err != nil {
		t.Fatalf("child never held writer: %v %v", held, err)
	}
	_ = cmd.Process.Kill()
	_ = cmd.Wait()
	for {
		held, err := codexipc.WriterLocked(path)
		if err != nil {
			t.Fatal(err)
		}
		if !held {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("orphaned executor retained writer lock after parent death")
		}
		time.Sleep(5 * time.Millisecond)
	}
}
