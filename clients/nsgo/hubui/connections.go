package hubui

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/monkeyWie/reignhub/internal/connectivity"
	"github.com/nsgo-dev/nsgo/core"
	"github.com/nsgo-dev/nsgo/icons/lucide"
	"github.com/nsgo-dev/nsgo/ui"
)

// Transport readiness is independent of provider read failures. Keep those
// failures for diagnostics without declaring a healthy socket disconnected.
type connectionHealth struct {
	attempt        *connectAttempt
	issues         map[string]string
	retryFailures  int
	reconnecting   bool
	waitingOnline  bool
	established    bool
	startupResumed bool
	diagnostics    []connectionStep
	observed       bool
	lastTransport  string
}

func (s *State) connection(id string) *connectionHealth {
	if s.connections == nil {
		s.connections = map[string]*connectionHealth{}
	}
	if s.connections[id] == nil {
		s.connections[id] = &connectionHealth{issues: map[string]string{}}
	}
	return s.connections[id]
}
func transportError(err error) bool {
	if err == nil {
		return false
	}
	if errors.Is(err, context.DeadlineExceeded) {
		return true
	}
	raw := strings.ToLower(err.Error())
	for _, text := range []string{"deadline exceeded", "timeout", "timed out", "device offline", "disconnected", "connection reset", "connection refused", "network is unreachable", "network is down", "broken pipe", "websocket", "设备离线", "relay 已断开", "连接已断开"} {
		if strings.Contains(raw, text) {
			return true
		}
	}
	return false
}
func (s *State) setConnectionIssue(id, source string, err error) {
	h := s.connection(id)
	if err == nil {
		if h.issues[source] != "" {
			s.logConnection(id, "read:"+source, "操作恢复", source, "success")
		}
		delete(h.issues, source)
	} else if !errors.Is(err, context.Canceled) {
		if h.issues[source] != err.Error() {
			s.logConnection(id, "read:"+source, "操作异常", source+": "+err.Error(), "warning")
		}
		h.issues[source] = err.Error()
	}
}

// Every connection indicator and command gate uses this same predicate. A stale
// presence snapshot cannot enable commands while a replacement is being verified.
func (s *State) connectionReady(id string) bool {
	h := s.connection(id)
	if h.reconnecting || h.attempt != nil && !h.attempt.complete {
		return false
	}
	if id == "" && !s.api.CanHost() {
		return h.issues["presence"] == ""
	}
	if id == "" && s.api.CanHost() {
		network := object(s.host["network"])
		return str(network, "state") == "" || str(network, "state") == "connected"
	}
	for _, d := range s.devices {
		if str(d, "id") == id {
			return flag(d, "connected")
		}
	}
	return false
}
func (s *State) connectionProblem(id string) bool { return !s.connectionReady(id) }
func (s *State) connectionRecovering(id string) bool {
	if id == "" && s.api.CanHost() {
		state := str(object(s.host["network"]), "state")
		return state == "connecting" || state == "error"
	}
	h := s.connection(id)
	if s.deviceOffline(id) {
		return false
	}
	if h.reconnecting && h.attempt != nil && h.attempt.active {
		return true
	}
	for _, d := range s.devices {
		if str(d, "id") == id {
			return (h.established || h.startupResumed) && !flag(d, "connected") && flag(d, "autoReconnect")
		}
	}
	return false
}
func (s *State) connectionTitle(id string) string {
	if s.connectionReady(id) {
		return "连接正常"
	}
	if s.deviceOffline(id) {
		return "设备离线"
	}
	if s.connectionRecovering(id) {
		return "连接中"
	}
	return "未连接"
}
func (s *State) connectionIndicator(cx *core.Scope, target func() string, namespace ...string) core.Node {
	prefix := ""
	if len(namespace) > 0 {
		prefix = namespace[0]
	}
	status := core.NewMemo(cx, func() [2]string { s.read(); id := target(); return [2]string{id, s.connectionTitle(id)} })
	return core.Dynamic(func(cx *core.Scope) core.Node {
		value := status.Get()
		id := value[0]
		icon := lucide.Wifi
		if s.connectionProblem(id) && !s.connectionRecovering(id) {
			icon = lucide.WifiOff
		}
		node := button(cx, prefix+"connection-status-"+id, "", icon, "ghost", func() {
			cx.DismissKeyboard()
			s.connectionDetailTarget = id
			s.dialog.Set("connection-status")
		}).(core.View)
		if s.connectionRecovering(id) {
			node.Children = []core.Node{reconnectingGlyph(cx)}
		}
		node.Label = value[1]
		node.Style.Padding = core.All(8)
		if prefix == "sidebar-" {
			node.Style.Width, node.Style.Height, node.Style.MinHeight = core.Pt(40), core.Pt(36), core.Pt(36)
		}
		baseStyle := node.StyleFunc
		node.StyleFunc = func() core.Style {
			st := baseStyle()
			if s.deviceOffline(id) {
				st.Color = palette(cx).mutedText
			}
			return st
		}
		return node
	})
}

// Connection waves travel outwards; the icon never rotates like Refresh.
var connectionWaves = []*core.SVG{
	core.MustParseSVG([]byte(`<svg xmlns="http://www.w3.org/2000/svg" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round"><path d="M8.5 16.429a5 5 0 0 1 7 0"/></svg>`)),
	core.MustParseSVG([]byte(`<svg xmlns="http://www.w3.org/2000/svg" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round"><path d="M5 12.859a10 10 0 0 1 14 0"/></svg>`)),
	core.MustParseSVG([]byte(`<svg xmlns="http://www.w3.org/2000/svg" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round"><path d="M2 8.82a15 15 0 0 1 20 0"/></svg>`)),
}

func reconnectingGlyph(cx *core.Scope) core.Node {
	layers := []core.Node{core.Icon{SVG: core.MustParseSVG([]byte(`<svg xmlns="http://www.w3.org/2000/svg" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round"><path d="M12 20h.01"/></svg>`)), Style: core.Style{Position: core.Absolute, Width: core.Pt(18), Height: core.Pt(18)}}}
	for i, svg := range connectionWaves {
		opacity := core.NewAnimatedValue(cx, 1)
		if !ui.ReduceMotion(cx) {
			cx.StartAnimation(core.ValueKeyframes(opacity, core.KeyframeTiming{Duration: 1400 * time.Millisecond, Delay: time.Duration(i) * 180 * time.Millisecond, Iterations: core.Forever}, core.StopAt(0, .25), core.StopAt(.3, 1), core.StopAt(.65, .25), core.StopAt(1, .25)), nil)
		}
		layers = append(layers, core.Icon{SVG: svg, Style: core.Style{Position: core.Absolute, Width: core.Pt(18), Height: core.Pt(18)}, StyleFunc: func() core.Style {
			return core.Style{Color: palette(cx).foreground, Animated: &core.AnimatedStyle{Opacity: opacity}}
		}})
	}
	return core.View{TestID: "connection-retrying", Style: core.Style{Width: core.Pt(18), Height: core.Pt(18)}, Children: layers}
}
func (s *State) connectionDetails(id string) string {
	h := s.connection(id)
	// Multiple automatic reads may fail with the same transport error.
	// Display that error once in the details panel.
	unique := map[string]bool{}
	if id == "" && s.api.CanHost() {
		network := object(s.host["network"])
		if str(network, "state") != "connected" && str(network, "detail") != "" {
			unique[str(network, "detail")] = true
		}
	}
	for _, value := range h.issues {
		unique[value] = true
	}
	if id != "" {
		if value := s.connection("").issues["presence"]; value != "" {
			unique[value] = true
		}
	}
	lines := []string{}
	for value := range unique {
		lines = append(lines, value)
	}
	sort.Strings(lines)
	return strings.Join(lines, "\n\n")
}

// A verified handshake starts a fresh state epoch. Late local-state/catalogue
// results from the previous transport must not overwrite the successful result.
func (s *State) acceptConnection(id string, devices []Object) {
	s.presenceGeneration++
	s.presencePolling = false
	s.cancelTargetCatalogue(id)
	s.devices = devices
	s.observeTransports()
	h := s.connection(id)
	h.established = true
	h.waitingOnline = false
	h.issues = map[string]string{}
	delete(s.connection("").issues, "presence")
	s.reconcileEnvironments()
}

func (s *State) reconnectDevice(id string) { s.reconnect(id, false) }

func (s *State) reconnect(id string, automatic bool) {
	pair := !automatic && s.connection(id).attempt != nil && s.connection(id).attempt.savedPairing
	s.connectDevice(id, automatic, pair)
}

// Stop the task and its UI state together. Late progress/completion callbacks
// are fenced by the attempt identity and cannot finish a replacement attempt.
func (s *State) cancelDeviceAttempt(id, reason string) {
	h := s.connection(id)
	if a := h.attempt; a != nil && a.active {
		a.stop(reason)
	}
	h.reconnecting = false
}

func (s *State) connectDevice(id string, automatic, pair bool) {
	h := s.connection(id)
	if s.ctx.Err() != nil {
		return
	}
	if h.attempt != nil && h.attempt.active {
		if automatic {
			return
		}
		s.cancelDeviceAttempt(id, "手动重试，结束上一轮连接")
	}
	// A stale reconnecting flag must never swallow an explicit user retry.
	h.reconnecting = true
	h.waitingOnline = false
	if !automatic {
		h.established = false
		h.startupResumed = false
	}
	ctx, cancel := context.WithCancel(s.ctx)
	params := Object{}
	for _, device := range s.devices {
		if str(device, "id") == id {
			params = Object{"code": str(device, "connectionId"), "service": str(device, "service"), "public": device["public"], "name": s.clientName, "relayOnly": device["relayOnly"]}
		}
	}
	a := &connectAttempt{params: params, target: id, savedPairing: pair, cancel: cancel, active: true, started: time.Now(), stepStarted: time.Now()}
	a.inherit(h.attempt)
	reason := "手动重新连接"
	detail := "用户发起重新连接"
	if automatic {
		reason = "连接已断开，正在自动重连"
		detail = "后台检测到与设备的连接已断开"
	}
	a.record(connectivity.Progress{ID: "retry", Stage: "retry", Message: reason, Detail: detail, State: "info"})
	a.record(connectivity.Progress{ID: "service", Stage: "service", Message: "正在重新连接服务", State: "running"})
	h.attempt = a
	ctx = connectivity.WithProgress(ctx, func(p connectivity.Progress) {
		s.cx.Post(func() {
			if s.ctx.Err() == nil && (a.active || a.complete) {
				if h.attempt != a {
					// Resume can reuse a wire whose observer belongs to the
					// preceding successful attempt. Preserve its live events.
					if a.complete {
						s.logConnection(id, p.Stage, p.Message, p.Detail, p.State)
						s.changed()
					}
					return
				}
				a.record(p)
				s.changed()
			}
		})
	})
	go func() {
		tick := time.NewTicker(time.Second)
		defer tick.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-tick.C:
				s.cx.Post(func() {
					if h.attempt == a && a.active {
						s.changed()
					}
				})
			}
		}
	}()
	s.retrying[id] = time.Now().Add(15 * time.Second)
	s.changed()
	go func() {
		method, request := "client.reconnect", Object{"id": id}
		if pair {
			method, request = "client.connect", params
		}
		v, err := s.api.Call(ctx, method, request, "", nil)
		finished := time.Now()
		cancel()
		s.cx.Post(func() {
			if s.ctx.Err() != nil || h.attempt != a || !a.active {
				return
			}
			a.finish(finished, err)
			h.reconnecting = false
			if err != nil {
				if !h.established {
					h.startupResumed = false
				}
				h.retryFailures++
				s.retrying[id] = time.Now().Add(min(15*time.Second, time.Second<<min(h.retryFailures, 4)))
			} else {
				h.retryFailures = 0
			}
			s.setConnectionIssue(id, "reconnect", err)
			if err != nil {
				s.logConnection(id, "retry-result", "本轮连接结束", err.Error(), "warning")
			}
			if err == nil {
				s.acceptConnection(id, objects(v))
				if id == s.target {
					s.startSessionWatch()
				}
			}
			s.changed()
			s.poll()
		})
	}()
}

func (s *State) observeLifecycle() {
	cx := s.cx
	observed := s.backgrounded
	cx.Effect(func() {
		background := cx.Env().Lifecycle() == core.LifecycleBackground
		if background != observed {
			observed = background
			// Do not subscribe the lifecycle effect to page revisions.
			cx.Post(func() { s.setBackground(background) })
		}
	})
}
func (s *State) setBackground(background bool) {
	if s.ctx.Err() != nil || background == s.backgrounded {
		return
	}
	s.backgrounded = background
	s.changed()
	for _, d := range s.devices {
		s.logConnection(str(d, "id"), "lifecycle", "应用生命周期变化", fmt.Sprintf("background=%t keepAlive=%t", background, s.keepAlive), "info")
	}
	if background {
		s.clearRootBack()
		// Visibility does not own synchronization on any platform. Retain
		// subscriptions, reads, retries and cache writes while the OS lets us run.
		return
	}
	if !s.keepAlive {
		// A denied/failed service start may be tried again while Android
		// permits starting an FGS, rather than looping in the background.
		s.nativeBackgroundState = ""
		s.changed()
	}
	// The OS may have frozen execution or dropped a transport. Fence stale
	// directory responses and reconcile once without replacing a healthy watch.
	if s.directoryCancel != nil {
		s.directoryCancel()
		s.directoryCancel = nil
	}
	s.directoryGeneration++
	s.directoryChecking = false
	s.cancelCatalogue()
	s.presenceGeneration++
	s.presencePolling = false
	s.polling = false
	// Never replay commands or discard a draft when returning to the app.
	for id := range s.retrying {
		delete(s.retrying, id)
	}
	s.refreshTimezone()
	s.directoryChecked = time.Time{}
	if s.stream == nil || !s.stream.current() {
		s.startSessionWatch()
	} else if !s.stream.snapshotInFlight || s.stream.baseline {
		// A valid replay cursor alone cannot establish freshness after sleep or
		// a missed upstream notification. Reconcile once, retaining the healthy
		// watch and drafts. An initial read already in flight needs no duplicate.
		s.stream.refreshSnapshot()
	}
	s.poll()
}
