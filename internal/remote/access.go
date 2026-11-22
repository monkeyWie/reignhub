package remote

import (
	"crypto/ed25519"
	"encoding/base64"
	"errors"
	"time"

	"github.com/monkeyWie/reignhub/internal/control"
	"github.com/monkeyWie/reignhub/pkg/access"
)

var errConnectionAuthorization = errors.New("连接授权已失效，请重新连接")

func authorizationFailure(id string, err error) control.Response {
	r := control.Response{ID: id, Error: err.Error()}
	if errors.Is(err, errConnectionAuthorization) {
		r.Code = control.ConnectionAuthorizationExpired
	}
	return r
}

func (w *wire) authorize(host, device string, req control.Request) error {
	// Production hosts authenticate the handshake with this local device key.
	// Recheck it for every message and outgoing fragment, including detached P2P.
	if w.opts.ResolveKey != nil {
		if key, ok := w.opts.ResolveKey(device); !ok || key != w.key {
			return errConnectionAuthorization
		}
		return nil
	}
	w.grantMu.RLock()
	ticket := w.grant
	w.grantMu.RUnlock()
	return w.authorizeTicket(ticket, host, device, req)
}
func (w *wire) authorizeTicket(ticket, host, device string, req control.Request) error {
	if w.opts.GrantPublicKey == "" {
		if w.opts.GrantRequired || w.opts.ServiceID != "" {
			return errors.New("服务未提供有效的授权公钥")
		}
		return nil
	}
	key, err := base64.RawURLEncoding.DecodeString(w.opts.GrantPublicKey)
	if err != nil {
		return err
	}
	g, err := access.Verify(ed25519.PublicKey(key), ticket, time.Now())
	if err != nil || g.Role != "client" || !g.Allows(w.opts.ServiceID, host, device, "connect") {
		return errConnectionAuthorization
	}
	if (w.independent != nil || req.Method == "peer.grant") && !g.Allows(w.opts.ServiceID, host, device, "offline") {
		return errConnectionAuthorization
	}
	if req.Provider != "" && !g.AllowsProduct(req.Provider) {
		return errors.New("当前套餐未启用此 Agent，请在账号设备页调整 Agent 权益")
	}
	return nil
}
