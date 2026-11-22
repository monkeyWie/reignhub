package nativepath

import (
	"context"
	"errors"
	"github.com/coder/websocket"
)

const NodeProtocol = "reign.node.v2"

func WebSocketLink(ws *websocket.Conn) *Link {
	ctx, cancel := context.WithCancel(context.Background())
	conn := websocket.NetConn(ctx, ws, websocket.MessageBinary)
	ws.SetReadLimit(32771)
	return &Link{done: make(chan struct{}), Network: "WSS", lanes: []stream{conn}, close: func() { _ = conn.Close(); cancel() }}
}

func DialWebSocket(ctx context.Context, address string) (*Link, error) {
	ws, _, err := websocket.Dial(ctx, address, &websocket.DialOptions{Subprotocols: []string{NodeProtocol}})
	if err != nil {
		return nil, err
	}
	if ws.Subprotocol() != NodeProtocol {
		ws.Close(websocket.StatusNormalClosure, "")
		return nil, errors.New("invalid node protocol")
	}
	return WebSocketLink(ws), nil
}
