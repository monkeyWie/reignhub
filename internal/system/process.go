package system

import (
	"os"
	"os/exec"
	"slices"

	"github.com/monkeyWie/reignhub/internal/provider"
)

func FindCLI(configured, app string) string {
	paths := append([]string{configured}, bundledCLIPaths(app)...)
	for _, p := range paths {
		if p == "" {
			continue
		}
		if st, err := os.Stat(p); err == nil && !st.IsDir() && isExecutable(st) {
			return p
		}
	}
	p, _ := exec.LookPath("codex")
	return p
}

func SameInstances(a, b []provider.Instance) bool {
	if len(a) != len(b) {
		return false
	}
	for _, x := range a {
		found := false
		for _, y := range b {
			if x.PID == y.PID && x.StartedAt == y.StartedAt && x.Path == y.Path {
				found = true
				break
			}
		}
		if !found {
			return false
		}
	}
	return true
}

// A user may quit an approved process before the action is dispatched. Any
// process still present must match one of the explicitly approved generations.
func approvedDesktopInstances(expected, current []provider.Instance) bool {
	for _, instance := range current {
		if !slices.Contains(expected, instance) {
			return false
		}
	}
	return true
}
