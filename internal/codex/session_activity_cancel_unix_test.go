//go:build darwin || linux || freebsd || openbsd || netbsd || dragonfly

package codex

import (
	"context"
	"os"
	"path/filepath"
	"syscall"
	"testing"
	"time"

	"github.com/monkeyWie/reignhub/internal/config"
	"github.com/monkeyWie/reignhub/internal/events"
	"github.com/monkeyWie/reignhub/internal/provider"
)

func TestDirectoryOwnerTimeoutPreservesMetadataAsUnknown(t *testing.T) {
	p := New(config.Config{}, "", events.New())
	f := attachIPCFixture(t, p, nil) // Owner discovery succeeds, but no snapshot arrives.
	dir := filepath.Join(f.b.home, "thread-writer-locks")
	if err := os.MkdirAll(dir, 0700); err != nil {
		t.Fatal(err)
	}
	lock, err := os.Create(filepath.Join(dir, "waiting.lock"))
	if err != nil {
		t.Fatal(err)
	}
	defer lock.Close()
	if err := syscall.Flock(int(lock.Fd()), syscall.LOCK_EX); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	rows := []provider.Session{{ID: "waiting", Title: "Keep this metadata", Status: "idle"}, {ID: "unlocked", Status: "completed"}}
	started := time.Now()
	p.enrichSessionActivity(ctx, rows)
	if time.Since(started) > time.Second || rows[0].Title != "Keep this metadata" || rows[0].Status != "unknown" || rows[0].CodexOwnerUnreachable || f.b.needsSharedDesktop() || rows[1].Status != "completed" {
		t.Fatal("slow owner blocked or falsified directory state", rows)
	}
}
