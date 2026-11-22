package remote

import (
	"context"
	"fmt"
	"sync/atomic"
	"time"

	"github.com/coder/websocket"
	"github.com/monkeyWie/reignhub/internal/connectivity"
)

type socketHeartbeat struct {
	lastReceived atomic.Int64
}

func newSocketHeartbeat() *socketHeartbeat {
	h := &socketHeartbeat{}
	h.received()
	return h
}
func (h *socketHeartbeat) received() { h.lastReceived.Store(time.Now().UnixNano()) }

func (h *socketHeartbeat) run(ctx context.Context, ws *websocket.Conn, renew bool, observe func(connectivity.Progress)) {
	h.runWithPolicy(ctx, ws, renew, observe, 25*time.Second, 85*time.Second, 10*time.Second)
}

// Use the same application heartbeat as browsers. CF answers it without waking
// the Durable Object. A missing native control pong must not destroy a channel
// that is still delivering authenticated data. Every received frame proves
// liveness, independently of Agent response time or a large history transfer.
func (h *socketHeartbeat) runWithPolicy(ctx context.Context, ws *websocket.Conn, renew bool, observe func(connectivity.Progress), interval, staleAfter, writeTimeout time.Duration) {
	stage := "relay"
	if renew {
		stage = "signal"
	}
	fail := func(message string, err error) {
		if ctx.Err() == nil && observe != nil {
			observe(connectivity.Progress{ID: stage + "-heartbeat", Stage: stage, Message: message, Detail: err.Error(), State: "warning"})
		}
		_ = ws.CloseNow()
	}
	tick := time.NewTicker(interval)
	defer tick.Stop()
	lastTick := time.Now()
	pulses := 0
	for {
		select {
		case <-ctx.Done():
			return
		case <-tick.C:
			// Mobile suspend can stop both reader and timer. Give the existing socket
			// a new chance to answer after waking, instead of expiring it immediately.
			if time.Since(lastTick) > interval*2 {
				h.received()
			}
			lastTick = time.Now()
			if idle := time.Since(time.Unix(0, h.lastReceived.Load())); idle > staleAfter {
				fail("连接心跳未收到响应", fmt.Errorf("%s 内未收到服务消息或心跳响应", idle.Round(time.Second)))
				return
			}
			probe, stop := context.WithTimeout(ctx, writeTimeout)
			err := ws.Write(probe, websocket.MessageText, []byte("reign:ping"))
			pulses++
			if err == nil && renew && pulses%3 == 0 {
				err = ws.Write(probe, websocket.MessageText, []byte(`{"kind":"renew"}`))
			}
			stop()
			if err != nil {
				fail("连接心跳发送失败", err)
				return
			}
		}
	}
}

// Business relay ingress receives only service control frames and heartbeats.
func drainRelay(ctx context.Context, ws *websocket.Conn, heartbeat *socketHeartbeat) error {
	if ws == nil {
		return nil
	}
	defer ws.CloseNow()
	for {
		if _, _, err := ws.Read(ctx); err != nil {
			return err
		}
		heartbeat.received()
	}
}
