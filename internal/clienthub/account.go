package clienthub

import (
	"context"
	"fmt"
	"github.com/monkeyWie/reignhub/internal/device"
)

// Account calls use a device credential, never an OAuth cookie or a refresh
// token copied into the native application. The verified email login binds the authenticated device.
func (h *Hub) Account(ctx context.Context, service, name, action, email, otp string) (any, error) {
	origin, err := device.ServiceURL(service)
	if err != nil {
		return nil, err
	}
	identity, err := h.identity(ctx, origin, name)
	if err != nil {
		return nil, err
	}
	paths := map[string]string{"send": "/api/account/device-email/send", "verify": "/api/account/device-email/verify", "status": "/api/account/device-status", "devices": "/api/account/device-list"}
	path, ok := paths[action]
	if !ok {
		return nil, fmt.Errorf("无效账号操作")
	}
	var out map[string]any
	err = device.Post(ctx, origin, identity.Token, path, map[string]any{"email": email, "otp": otp}, &out)
	return out, err
}

// ControllerCredential supplies the companion identity to the local host's login request.
func (h *Hub) ControllerCredential(ctx context.Context, service, name string) (string, error) {
	i, err := h.identity(ctx, service, name)
	return i.Token, err
}
