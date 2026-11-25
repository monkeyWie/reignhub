package hubui

import (
	"context"

	"github.com/monkeyWie/reignhub/internal/control"
	"github.com/monkeyWie/reignhub/internal/system"
)

type sessionCodexRestart struct {
	id        string
	activated bool
	failure   string
}

func (s *State) codexSessionTarget(ss Object) string {
	if target := str(ss, "deviceId"); target != "" {
		return target
	}
	return s.target
}

func (s *State) codexRecoveryIssue(ss Object) bool {
	if str(ss, "providerId") != "codex-app" {
		return false
	}
	target := s.codexSessionTarget(ss)
	if !s.connectionReady(target) {
		return false
	}
	if r := s.codexRestart; r != nil && target == control.LocalTarget && r.id == str(ss, "id") {
		if s.actionPending("confirm-activate") || r.activated {
			return true
		}
	}
	return flag(ss, "codexOwnerUnreachable")
}

func (s *State) canRestartSessionCodex(ss Object) bool {
	return system.CodexSharedDesktop && s.api.CanHost() && s.codexSessionTarget(ss) == control.LocalTarget && s.codexRecoveryIssue(ss)
}

func (s *State) sessionCodexRecoveryFeedback(ss Object) (bool, string) {
	if s.codexSessionTarget(ss) != control.LocalTarget || str(ss, "providerId") != "codex-app" {
		return false, ""
	}
	busy := s.actionPending("confirm-activate")
	if r := s.codexRestart; r != nil && r.id == str(ss, "id") {
		return busy, r.failure
	}
	return busy, ""
}

func (s *State) restartSessionCodex(ss Object) {
	if !s.canRestartSessionCodex(ss) || s.actionPending("confirm-activate") {
		return
	}
	id := str(ss, "id")
	if r := s.codexRestart; r != nil && r.id == id && r.activated {
		// Retry only the read unless Codex now needs to be launched again.
		for _, p := range s.localProviders {
			if str(p, "id") == "codex-app" && str(p, "state") == "launch-required" {
				s.dialog.Set("restart-session-codex:" + id)
				return
			}
		}
		s.runSessionCodexRestart(id, nil, true)
		return
	}
	s.settingsReturn = ""
	s.dialog.Set("restart-session-codex:" + id)
}

func (s *State) confirmSessionCodexRestart(id string, p Object) {
	if s.actionPending("confirm-activate") {
		return
	}
	for _, ss := range s.workspace(control.LocalTarget).sessions {
		if str(ss, "id") == id && str(ss, "providerId") == "codex-app" {
			local := copyObject(ss)
			local["deviceId"] = control.LocalTarget
			if s.canRestartSessionCodex(local) {
				s.runSessionCodexRestart(id, p, false)
			}
			return
		}
	}
}

func (s *State) runSessionCodexRestart(id string, p Object, verifyOnly bool) {
	r := &sessionCodexRestart{id: id, activated: verifyOnly}
	s.codexRestart = r
	s.runActionResult(s.ctx, "confirm-activate", false, func(ctx context.Context) (any, error) {
		if !verifyOnly {
			if err := s.activateLocalCodex(ctx, p, true); err != nil {
				return false, err
			}
		}
		_, err := rpc(ctx, s.api, control.LocalTarget, "codex-app", id, "watch", Object{"probe": true})
		return true, err
	}, func(activated any, err error) {
		// Fence directory responses started against the previous backend.
		s.cancelTargetCatalogue(control.LocalTarget)
		s.poll()
		if err != nil {
			r.activated, _ = activated.(bool)
			r.failure = err.Error()
			return
		}
		s.codexRestart = nil
		w := s.workspace(control.LocalTarget)
		for i, row := range w.sessions {
			if str(row, "providerId") == "codex-app" && str(row, "id") == id && flag(row, "codexOwnerUnreachable") {
				w.sessions = append([]Object(nil), w.sessions...)
				w.sessions[i] = copyObject(row)
				delete(w.sessions[i], "codexOwnerUnreachable")
				s.workspaces[control.LocalTarget] = w
				s.publishWorkspaces()
				break
			}
		}
		if s.target == control.LocalTarget && str(s.selected, "providerId") == "codex-app" {
			if s.stream != nil {
				s.stream.verified = false
			}
			s.startSessionWatch()
		}
	})
}
