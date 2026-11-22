package hubui

import (
	"context"
	"time"

	"github.com/nsgo-dev/nsgo/core"
)

// Directory presence is independent of this controller's peer transports.
// One batch per service every 30 seconds, while the controller is running.
func (s *State) refreshDevicePresence() {
	if s.ctx.Err() != nil || s.directoryChecking || len(s.devices) == 0 || time.Since(s.directoryChecked) < 30*time.Second {
		return
	}
	s.directoryGeneration++
	generation := s.directoryGeneration
	s.directoryChecking = true
	s.directoryChecked = time.Now()
	ctx, cancel := context.WithCancel(s.ctx)
	s.directoryCancel = cancel
	go func() {
		defer cancel()
		v, err := s.api.Call(ctx, "client.presence", nil, "", nil)
		s.cx.Post(func() {
			if generation != s.directoryGeneration {
				return
			}
			s.directoryChecking = false
			s.directoryCancel = nil
			if s.ctx.Err() != nil {
				return
			}
			if err == nil {
				s.applyDevicePresence(objects(v))
			} else {
				s.autoReconnect()
			}
			s.changed()
		})
	}()
}

func (s *State) presenceFor(d Object) Object {
	status := s.devicePresence[str(d, "id")]
	if number(status, "checkedAt") < time.Now().Add(-75*time.Second).Unix() {
		return nil
	}
	return status
}

// A live data path proves presence even if the service's last observation is
// stale. Absence of a response to a presence query does not prove offline.
func (s *State) deviceOnline(d Object) (online, known bool) {
	if flag(d, "connected") {
		return true, true
	}
	online, known = s.presenceFor(d)["online"].(bool)
	if !online && flag(d, "independentRoutes") {
		return false, false
	}
	return
}

func (s *State) deviceOffline(id string) bool {
	for _, d := range s.devices {
		if str(d, "id") == id {
			online, known := s.deviceOnline(d)
			return known && !online
		}
	}
	return false
}

func (s *State) applyDevicePresence(statuses []Object) {
	for _, status := range statuses {
		id := str(status, "id")
		wasOffline := s.deviceOffline(id)
		if s.devicePresence == nil {
			s.devicePresence = map[string]Object{}
		}
		s.devicePresence[id] = status
		h := s.connection(id)
		if s.deviceOffline(id) {
			if h.reconnecting && h.attempt != nil && h.attempt.active {
				s.cancelDeviceAttempt(id, "设备离线，结束本轮连接")
				h.waitingOnline = true
				s.logConnection(id, "presence", "设备离线，等待上线", "检测到上线后恢复连接", "info")
			}
		} else if online, known := s.presenceFor(status)["online"].(bool); known && online && wasOffline {
			delete(s.retrying, id)
		}
	}
	s.autoReconnect()
}

func (s *State) deviceListStatus(d Object) string {
	s.read()
	if online, known := s.deviceOnline(d); known {
		if online {
			return "在线"
		}
		return "离线"
	}
	// Keep an unknown presence neutral instead of inventing an offline state.
	return ""
}

func (s *State) devicePresenceIndicator(cx *core.Scope, d func() Object) core.Node {
	return core.Dynamic(func(cx *core.Scope) core.Node {
		s.read()
		device := d()
		color := palette(cx).mutedText.Faded(.5)
		if online, known := s.deviceOnline(device); known && online {
			color = core.RGB(0x22a060)
		}
		return core.View{TestID: "device-presence-" + str(device, "id"), Label: s.deviceListStatus(device), Style: core.Style{Width: core.Pt(18), Height: core.Pt(18), AlignItems: core.AlignCenter, JustifyContent: core.JustifyCenter}, Children: []core.Node{core.View{Style: core.Style{Width: core.Pt(7), Height: core.Pt(7), CornerRadius: 4, Background: color}}}}
	})
}

// Navigation is local. Connection setup runs behind the status indicator and
// must never gate reading cached sessions or messages.
func (s *State) openDevice(id string) {
	s.selectDevice(id)
	s.connectSavedDevice(id)
}

func (s *State) connectSavedDevice(id string) {
	for _, d := range s.devices {
		if str(d, "id") != id {
			continue
		}
		if flag(d, "connected") || s.connection(id).reconnecting {
			return
		}
		if s.deviceOffline(id) {
			s.connection(id).waitingOnline = true
			s.changed()
			return
		}
		s.connection(id).established = false
		s.connectDevice(id, false, !flag(d, "autoReconnect"))
		return
	}
}
