package device

import (
	"context"
	"encoding/hex"
	"errors"
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
