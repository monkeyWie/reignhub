package system

import (
	"github.com/monkeyWie/reignhub/internal/provider"
	"testing"
)

func TestInstanceGenerationGuard(t *testing.T) {
	a := []provider.Instance{{PID: 42, StartedAt: "today", Path: "/Applications/Codex.app"}}
	b := []provider.Instance{{PID: 42, StartedAt: "tomorrow", Path: a[0].Path}}
	if SameInstances(a, b) {
		t.Fatal("recycled PID accepted")
	}
	if !SameInstances(a, a) || SameInstances(a, nil) {
		t.Fatal("invalid comparison")
	}
}

func TestApprovedDesktopInstancesAllowsAlreadyExitedProcess(t *testing.T) {
	approved := []provider.Instance{{PID: 17, StartedAt: "old", Path: "app"}}
	if !approvedDesktopInstances(approved, nil) || !approvedDesktopInstances(approved, approved) {
		t.Fatal("rejected an approved or already exited instance")
	}
	if approvedDesktopInstances(approved, []provider.Instance{{PID: 17, StartedAt: "new", Path: "app"}}) {
		t.Fatal("allowed a new instance that reused the approved PID")
	}
}
