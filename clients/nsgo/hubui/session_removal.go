package hubui

import (
	"context"
	"errors"

	"github.com/monkeyWie/reignhub/clients/nsgo/hubui/locales"
	"github.com/monkeyWie/reignhub/internal/control"
	"github.com/nsgo-dev/nsgo/base"
	"github.com/nsgo-dev/nsgo/core"
	"github.com/nsgo-dev/nsgo/icons/lucide"
	"github.com/nsgo-dev/nsgo/ui"
)

func (s *State) removalMode(ss Object) string {
	for _, p := range s.workspace(str(ss, "deviceId")).providers {
		if str(p, "id") == str(ss, "providerId") {
			return str(object(p["capabilities"]), "sessionRemoval")
		}
	}
	return ""
}

func (s *State) requestSessionRemoval(ss Object) {
	s.removalTarget = copyObject(ss)
	s.dialog.Set("remove-session")
}

func (s *State) removalBlocked(ss Object) string {
	l := locales.For(s.cx)
	key := sessionIdentity(ss)
	if s.uncertainRemovals[key] {
		return l.RemovalUncertain()
	}
	if !s.connectionReady(str(ss, "deviceId")) {
		return l.RemovalOffline()
	}
	if s.removalMode(ss) == "" {
		return l.RemovalUnsupported()
	}
	for _, current := range s.sessions {
		if sessionIdentity(current) == key {
			ss = current
			break
		}
	}
	if sessionRunning(ss) {
		return l.RemovalBusy()
	}
	return ""
}

func (s *State) removeSessionDialog(cx *core.Scope) core.Node {
	ss := copyObject(s.removalTarget)
	key := sessionIdentity(ss)
	action := "remove-session:" + key
	mounted := true
	cx.Cleanup(func() { mounted = false })
	close := func() {
		if mounted {
			s.closeDialog()
		}
	}
	l := locales.For(cx)
	description := l.RemovalDeleteHint()
	switch s.removalMode(ss) {
	case "archive":
		description = l.RemovalArchiveHint()
	case "detach":
		description = l.RemovalDetachHint()
	}
	confirm := busyButton(cx, "confirm-remove-session", l.RemoveSession(), lucide.Trash, "primary", func() bool { return s.actionPending(action) }, func() {
		if reason := s.removalBlocked(ss); reason != "" {
			s.error = reason
			s.changed()
			return
		}
		s.runAction(action, func(ctx context.Context) (any, error) {
			_, err := rpc(ctx, s.api, str(ss, "deviceId"), str(ss, "providerId"), str(ss, "id"), "session.remove", nil)
			// Publish all outcomes on the UI loop, including when the dialog was
			// closed or navigation changed while this write was in flight.
			return removalResult{err}, nil
		}, func(value any) {
			err := value.(removalResult).err
			if err != nil {
				if control.OutcomeUnknown(err) || errors.Is(err, context.DeadlineExceeded) || errors.Is(err, context.Canceled) {
					if s.uncertainRemovals == nil {
						s.uncertainRemovals = map[string]bool{}
					}
					s.uncertainRemovals[key] = true
					s.saveSidebarPreferences()
					s.error = l.RemovalUncertain()
					// Only a new directory read may resolve this write's outcome.
					s.cancelTargetCatalogue(str(ss, "deviceId"))
					s.pollCatalogues()
				} else {
					s.error = err.Error()
				}
				return
			}
			s.finishSessionRemoval(ss)
			close()
		})
	}).(core.View)
	confirm.Disabled = func() bool { s.read(); return s.actionPending(action) || s.removalBlocked(ss) != "" }
	return modalDialog(cx, ui.Dialog{TestID: "dialog-remove-session", Title: l.RemoveSession(), Description: description,
		Open: func() bool { return true }, OnOpenChange: func(open bool, _ base.Reason) {
			if !open {
				close()
			}
		}, Style: core.Style{Width: core.Pt(420), MaxWidth: core.Pct(96)},
		Children: []core.Node{label(cx, str(ss, "title"), 14, false), label(cx, s.environmentName(str(ss, "deviceId"))+" · "+s.providerName(str(ss, "deviceId"), str(ss, "providerId")), 12, true), dynamicText(cx, func() string { s.read(); return s.removalBlocked(ss) }, 12, true)},
		Footer:   []core.Node{button(cx, "cancel-remove-session", l.CancelRemoval(), nil, "outline", close), confirm},
	}, close)
}

type removalResult struct{ err error }

func (s *State) finishSessionRemoval(ss Object) {
	target, agent, id := str(ss, "deviceId"), str(ss, "providerId"), str(ss, "id")
	s.cancelTargetCatalogue(target)
	if s.removedSessions == nil {
		s.removedSessions = map[string]bool{}
	}
	// Codex may expose the same native thread; its archive removes both.
	matches := func(row Object) bool {
		return str(row, "id") == id && agentFamily(str(row, "providerId")) == agentFamily(agent)
	}
	s.removedSessions[sessionIdentity(ss)] = true
	delete(s.pinned, sessionIdentity(ss))
	delete(s.unreadSessions, activityIdentity(ss))
	delete(s.sessionActivity, sessionIdentity(ss))
	delete(s.uncertainRemovals, sessionIdentity(ss))
	w := s.workspace(target)
	kept := make([]Object, 0, len(w.sessions))
	for _, row := range w.sessions {
		if matches(row) {
			entry := copyObject(row)
			entry["deviceId"] = target
			s.removedSessions[sessionIdentity(entry)] = true
			delete(s.pinned, sessionIdentity(entry))
			delete(s.sessionActivity, sessionIdentity(entry))
		} else {
			kept = append(kept, row)
		}
	}
	w.sessions = kept
	s.workspaces[target] = w
	s.cx.Storage().RemoveItem(permissionCacheKey(target, agent, id))
	s.discardSessionDraft(target, agent, id)
	s.clearRemovedSelection(target, agent, id)
	s.publishWorkspaces()
	s.showNotice(locales.For(s.cx).SessionRemoved())
	s.saveSidebarPreferences()
	s.pollCatalogues()
}

func (s *State) clearRemovedSelection(target, agent, id string) {
	s.cancelSessionEntry()
	if s.target == target && str(s.selected, "id") == id && agentFamily(str(s.selected, "providerId")) == agentFamily(agent) {
		s.showHome()
		if s.watchCancel != nil {
			s.watchCancel()
		}
		s.sessionGeneration++
		s.selected, s.transcript = nil, nil
		s.messageRows, s.approvals, s.uploads = nil, nil, nil
		s.transcriptLoading, s.transcriptSyncing = false, false
		s.draft.Set("")
	}
}

// Clear acknowledged cache fences only after a complete directory has durably
// observed absence. Later native unarchives may then appear normally.
func (s *State) reconcileSessionRemovals(target, agent string, incoming []Object) {
	present := map[string]bool{}
	for _, ss := range incoming {
		present[str(ss, "id")] = true
	}
	changed := false
	for _, markers := range []map[string]bool{s.removedSessions, s.uncertainRemovals} {
		for key := range markers {
			identity := decodeSessionIdentity(key)
			if len(identity) == 3 && identity[0] == target && identity[1] == agent && !present[identity[2]] {
				s.cx.Storage().RemoveItem(permissionCacheKey(target, agent, identity[2]))
				s.discardSessionDraft(target, agent, identity[2])
				s.clearRemovedSelection(target, agent, identity[2])
				if s.dialog.GetUntracked() == "remove-session" && sessionIdentity(s.removalTarget) == key {
					s.closeDialog()
				}
				if s.uncertainRemovals[key] {
					l := locales.For(s.cx)
					s.showNotice(l.SessionRemoved())
					if s.error == l.RemovalUncertain() {
						s.error = ""
					}
				}
				delete(markers, key)
				delete(s.pinned, key)
				delete(s.unreadSessions, activityIdentity(Object{"deviceId": target, "providerId": agent, "id": identity[2]}))
				delete(s.sessionActivity, key)
				changed = true
			}
		}
	}
	if changed {
		s.saveSidebarPreferences()
	}
}
