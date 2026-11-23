package system

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/monkeyWie/reignhub/internal/provider"
)

func TestWindowsNativeProcessProbe(t *testing.T) {
	ctx := context.Background()
	started, path, err := ProcessStamp(ctx, os.Getpid())
	if err != nil || started == "" {
		t.Fatalf("current process stamp: %q %q %v", started, path, err)
	}
	exe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	if !strings.EqualFold(path, exe) {
		t.Fatalf("image path %q want %q", path, exe)
	}
	instances, err := Discover(ctx, exe)
	if err != nil {
		t.Fatal(err)
	}
	if len(instances) != 1 || instances[0].PID != os.Getpid() || instances[0].StartedAt != started {
		t.Fatalf("native discovery of isolated test process: %+v", instances)
	}
	other := filepath.Join(t.TempDir(), filepath.Base(exe))
	if err := os.WriteFile(other, nil, 0600); err != nil {
		t.Fatal(err)
	}
	instances, err = Discover(ctx, other)
	if err != nil || len(instances) != 0 {
		t.Fatalf("same name at unrelated path matched: %+v %v", instances, err)
	}
	canceled, cancel := context.WithCancel(ctx)
	cancel()
	if _, _, err := ProcessStamp(canceled, os.Getpid()); err != context.Canceled {
		t.Fatalf("cancellation: %v", err)
	}
}

func TestWindowsDesktopProcessTreeRoots(t *testing.T) {
	candidates := []provider.Instance{{PID: 10}, {PID: 11}, {PID: 13}, {PID: 20}}
	parents := map[int]int{10: 1, 11: 10, 12: 10, 13: 12, 20: 2}
	got := desktopRoots(candidates, parents)
	if len(got) != 2 || got[0].PID != 10 || got[1].PID != 20 {
		t.Fatalf("Electron subprocesses counted as App instances: %+v", got)
	}
}

func TestWindowsCodexRestartStopsOnlyApprovedProcess(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 8*time.Second)
	defer cancel()
	exe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	cmd := exec.CommandContext(ctx, exe, "-test.run=^TestConsoleChildFixture$")
	cmd.Env = append(os.Environ(), "REIGN_CONSOLE_FIXTURE=wait")
	HideConsole(cmd)
	stdin, err := cmd.StdinPipe()
	if err != nil {
		t.Fatal(err)
	}
	defer stdin.Close()
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() { done <- cmd.Wait() }()

	pid := cmd.Process.Pid
	stamp, executable, err := ProcessStamp(ctx, pid)
	if err != nil {
		t.Fatal(err)
	}
	approved := provider.Instance{PID: pid, StartedAt: stamp}
	stale := provider.Instance{PID: pid, StartedAt: "stale"}
	if err := stopCodexDesktop(ctx, executable, []provider.Instance{approved, stale}); err == nil {
		t.Fatal("termination accepted a stale process generation")
	}
	canceled, stop := context.WithCancel(ctx)
	stop()
	if err := stopCodexDesktop(canceled, executable, []provider.Instance{approved}); !errors.Is(err, context.Canceled) {
		t.Fatalf("canceled restart: %v", err)
	}
	if _, _, err := ProcessStamp(ctx, pid); err != nil {
		t.Fatalf("process exited before a valid request: %v", err)
	}
	if err := stopCodexDesktop(ctx, executable, []provider.Instance{approved}); err != nil {
		t.Fatal(err)
	}
	select {
	case err := <-done:
		var exit *exec.ExitError
		if !errors.As(err, &exit) || exit.ExitCode() != 1 {
			t.Fatalf("expected termination exit code: %v", err)
		}
	case <-ctx.Done():
		t.Fatal(ctx.Err())
	}
}
