//go:build !windows && (!darwin || ios)

package system

import (
	"context"
	"errors"

	"github.com/monkeyWie/reignhub/internal/provider"
)

const CodexSharedDesktop = false

func LaunchCodexDesktop(context.Context, string, string, []provider.Instance, bool) (int, error) {
	return 0, errors.New("当前系统暂不支持启动共享 Codex App")
}
