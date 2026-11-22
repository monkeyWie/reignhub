package device

import (
	"context"
	"encoding/hex"
	"errors"
	"fmt"
	"time"
)

// AccountKey binds the two device keys to one server-confirmed account link.
// Removing and recreating a link produces a new key, even for the same devices.
func AccountKey(private, public, binding string) (string, error) {
	if b, err := hex.DecodeString(binding); err != nil || len(b) != 16 {
		return "", errors.New("无效的账号设备关联")
	}
	key, err := sharedKey(private, public, "reignhub/account/link/"+binding)
	if err != nil {
		return "", err
	}
	return hex.EncodeToString(key), nil
}

func WaitAccountHost(ctx context.Context, service, token, binding string) error {
	ctx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	tick := time.NewTicker(500 * time.Millisecond)
	defer tick.Stop()
	for {
		var r struct{ Ready bool }
		if err := Post(ctx, service, token, "/api/account/access/ready", map[string]string{"binding": binding}, &r); err != nil {
			return err
		}
		if r.Ready {
			return nil
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-tick.C:
		}
	}
}

// AccountRequest carries the inputs for a single authenticated account operation.
type AccountRequest struct {
	Email           string `json:"email,omitempty"`
	OTP             string `json:"otp,omitempty"`
	Password        string `json:"password,omitempty"`
	Provider        string `json:"provider,omitempty"`
	ID              string `json:"id,omitempty"`
	ControllerToken string `json:"controllerToken,omitempty"`
}

func AccountAction(ctx context.Context, service, token, action string, params AccountRequest) (any, error) {
	paths := map[string]string{
		"send": "/api/account/device-email/send", "verify": "/api/account/device-email/verify",
		"password": "/api/account/device-email/password", "start": "/api/account/device-login/start",
		"poll": "/api/account/device-login/poll", "status": "/api/account/device-status", "devices": "/api/account/device-list",
	}
	path, ok := paths[action]
	if !ok {
		return nil, fmt.Errorf("无效账号操作")
	}
	var out map[string]any
	err := Post(ctx, service, token, path, params, &out)
	return out, err
}
