package hubui

import (
	"strings"

	"github.com/monkeyWie/reignhub/clients/nsgo/hubui/locales"
	"github.com/nsgo-dev/nsgo/core"
	"github.com/nsgo-dev/nsgo/icons/lucide"
)

func activityIdentity(session Object) string {
	ss := copyObject(session)
	ss["providerId"] = agentFamily(str(ss, "providerId"))
	return sessionIdentity(ss)
}

// Only fresh summaries drive completion. Cached, missing or unknown/offline
// state is never evidence that a task finished. Opening a chat acknowledges it.
func (s *State) observeSessionActivity(target string, incoming []Object) {
	if !s.connectionReady(target) {
		return
	}
	if s.sessionActivity == nil {
		s.sessionActivity = map[string]string{}
	}
	if s.unreadSessions == nil {
		s.unreadSessions = map[string]bool{}
	}
	changed := false
	for _, ss := range incoming {
		entry := copyObject(ss)
		entry["deviceId"] = target
		key := activityIdentity(entry)
		trackingKey := sessionIdentity(entry)
		status := strings.ToLower(str(ss, "status"))
		if sessionRunning(ss) {
			s.sessionActivity[trackingKey] = "running"
			continue
		}
		if status != "idle" && status != "completed" && status != "failed" && status != "interrupted" && status != "systemerror" {
			continue
		}
		viewed := s.target == target && activityIdentity(s.selected) == key && !s.backgrounded && s.conversationVisible()
		if s.sessionActivity[trackingKey] == "running" && (status == "idle" || status == "completed") && !viewed && !s.unreadSessions[key] {
			s.unreadSessions[key] = true
			changed = true
		}
		s.sessionActivity[trackingKey] = status
	}
	if changed {
		s.saveSidebarPreferences()
	}
}

func (s *State) markSessionRead(ss Object) {
	key := activityIdentity(ss)
	if s.unreadSessions[key] {
		delete(s.unreadSessions, key)
		s.saveSidebarPreferences()
	}
}

func (s *State) sessionUnread(ss Object) bool {
	return s.unreadSessions[activityIdentity(ss)] && !s.sessionActive(ss)
}

func (s *State) anySessionUnread(sessions []Object) bool {
	for _, ss := range sessions {
		if s.sessionUnread(ss) {
			return true
		}
	}
	return false
}

func unreadDot(cx *core.Scope, id, title string) core.Node {
	return core.View{TestID: "unread-" + id, Label: title, Tooltip: tooltipCard(cx, label(cx, title, 12, false)), Style: core.Style{Width: core.Pt(7), Height: core.Pt(7), CornerRadius: 4, FlexShrink: 0}, StyleFunc: func() core.Style { return core.Style{Background: palette(cx).brand} }}
}

func (s *State) sessionStatusLabel(ss Object) string {
	l := locales.For(s.cx)
	if s.codexRecoveryIssue(ss) {
		return l.CodexSessionUnavailable()
	}
	if s.sessionActive(ss) {
		if sessionWaiting(ss) {
			return l.SessionWaiting()
		}
		return l.SessionRunning()
	}
	if s.unreadSessions[activityIdentity(ss)] {
		return l.SessionCompletedUnread()
	}
	return ""
}

func sessionStatusVisibility(cx *core.Scope) core.Style {
	if !cx.Env().Desktop() {
		return core.Style{}
	}
	hidden := &core.Style{Opacity: core.Alpha(0), PointerEvents: core.PointerEventsNone}
	return core.Style{GroupStyle: &core.GroupStyle{Name: "session-row", Hover: hidden, FocusWithin: hidden}}
}

func (s *State) sessionStatusIndicator(cx *core.Scope, id string, item func() directoryRow) core.Node {
	status := core.NewMemo(cx, func() string { s.read(); return s.sessionStatusLabel(item().Session) })
	return core.View{TestID: "status-" + id, Style: core.Style{Position: core.Absolute, Top: core.Pt(10), Right: core.Pt(choose(cx.Env().Desktop(), sidebarRowActionRight+7, 12)), Width: core.Pt(16), Height: core.Pt(18), AlignItems: core.AlignCenter, JustifyContent: core.JustifyCenter}, StyleFunc: func() core.Style { return sessionStatusVisibility(cx) }, Children: []core.Node{core.Dynamic(func(cx *core.Scope) core.Node {
		label := status.Get()
		if label == "" {
			return nil
		}
		ss := core.Untracked(cx, func() Object { return item().Session })
		if s.sessionActive(ss) {
			if sessionWaiting(ss) {
				return core.View{Label: label, Tooltip: tooltipCard(cx, dynamicText(cx, status.Get, 12, false)), Children: []core.Node{glyph(cx, lucide.CirclePause, 14)}}
			}
			return s.loadingRing("running-"+id, label, 14)
		}
		return unreadDot(cx, id, label)
	})}}
}
