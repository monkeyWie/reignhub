package clienthub

import (
	"context"
	"github.com/monkeyWie/reignhub/internal/device"
)

// Account calls use a device credential, never an OAuth cookie or a refresh
// token copied into the native application. The verified email login binds the authenticated device.
func (h *Hub) Account(ctx context.Context, service, name, action string, params device.AccountRequest) (any, error) {
	origin, err := device.ServiceURL(service)
	if err != nil {
		return nil, err
	}
	identity, err := h.identity(ctx, origin, name)
	if err != nil {
		return nil, err
	}
	return device.AccountAction(ctx, origin, identity.Token, action, params)
}

// ControllerCredential supplies the companion identity to the local host's login request.
func (h *Hub) ControllerCredential(ctx context.Context, service, name string) (string, error) {
	i, err := h.identity(ctx, service, name)
	return i.Token, err
}
