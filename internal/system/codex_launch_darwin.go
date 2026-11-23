//go:build darwin && !ios

package system

import (
	"context"
	"errors"
	"fmt"
	"runtime"
	"strconv"
	"sync"
	"time"

	"github.com/ebitengine/purego"
	"github.com/ebitengine/purego/objc"

	"github.com/monkeyWie/reignhub/internal/provider"
)

const CodexSharedDesktop = true

var loadCodexAppKit = sync.OnceValue(func() error {
	_, err := purego.Dlopen("/System/Library/Frameworks/AppKit.framework/AppKit", purego.RTLD_NOW|purego.RTLD_GLOBAL)
	return err
})

var sel = objc.RegisterName

func appString(value string) objc.ID {
	return objc.ID(objc.GetClass("NSString")).Send(sel("stringWithUTF8String:"), value)
}

func appPool() func() {
	runtime.LockOSThread()
	pool := objc.ID(objc.GetClass("NSAutoreleasePool")).Send(sel("new"))
	return func() {
		pool.Send(sel("drain"))
		runtime.UnlockOSThread()
	}
}

func sameBundle(instance objc.ID, path objc.ID) bool {
	actual := instance.Send(sel("bundleURL")).Send(sel("path")).Send(sel("stringByStandardizingPath"))
	return objc.Send[bool](actual, sel("isEqualToString:"), path)
}

func Discover(ctx context.Context, app string) ([]provider.Instance, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if AppExecutable(app) == "" {
		return nil, nil
	}
	if err := loadCodexAppKit(); err != nil {
		return nil, err
	}
	defer appPool()()
	path := appString(app).Send(sel("stringByStandardizingPath"))
	bundle := objc.ID(objc.GetClass("NSBundle")).Send(sel("bundleWithPath:"), path)
	id := bundle.Send(sel("bundleIdentifier"))
	if id == 0 {
		return nil, errors.New("无法识别 Codex App 的应用标识")
	}
	apps := objc.ID(objc.GetClass("NSRunningApplication")).Send(sel("runningApplicationsWithBundleIdentifier:"), id)
	var out []provider.Instance
	for i := range objc.Send[uint](apps, sel("count")) {
		instance := apps.Send(sel("objectAtIndex:"), i)
		birth := instance.Send(sel("launchDate"))
		if objc.Send[bool](instance, sel("isTerminated")) || birth == 0 || !sameBundle(instance, path) {
			continue
		}
		out = append(out, provider.Instance{PID: int(objc.Send[int32](instance, sel("processIdentifier"))), Path: app,
			StartedAt: strconv.FormatFloat(objc.Send[float64](birth, sel("timeIntervalSince1970")), 'g', -1, 64)})
	}
	return out, nil
}

func LaunchCodexDesktop(ctx context.Context, app, endpoint string, expected []provider.Instance, restart bool) (int, error) {
	if AppExecutable(app) == "" {
		return 0, errors.New("找不到 Codex App 的启动程序")
	}
	current, err := Discover(ctx, app)
	if err != nil {
		return 0, err
	}
	if !approvedDesktopInstances(expected, current) {
		return 0, errors.New("Codex App 的运行状态已变化，请刷新后重试")
	}
	if err := quitCodexInstances(app, current, restart); err != nil {
		return 0, err
	}
	wait, stop := context.WithTimeout(ctx, 9*time.Second)
	defer stop()
	for len(current) != 0 {
		select {
		case <-wait.Done():
			if ctx.Err() != nil {
				return 0, ctx.Err()
			}
			return 0, errors.New("Codex App 正在退出或等待确认，请在 App 中处理后重试")
		case <-time.After(150 * time.Millisecond):
		}
		current, err = Discover(wait, app)
		if err != nil {
			return 0, err
		}
		if !approvedDesktopInstances(expected, current) {
			return 0, errors.New("检测到新的 Codex App 实例，请刷新后重试")
		}
	}
	if err := ctx.Err(); err != nil {
		return 0, err
	}
	return openCodexApp(ctx, app, endpoint)
}

func quitCodexInstances(app string, instances []provider.Instance, restart bool) error {
	if len(instances) == 0 {
		return nil
	}
	if !restart {
		return errors.New("请确认重启 Codex App")
	}
	defer appPool()()
	path := appString(app).Send(sel("stringByStandardizingPath"))
	for _, instance := range instances {
		proc := objc.ID(objc.GetClass("NSRunningApplication")).Send(sel("runningApplicationWithProcessIdentifier:"), int32(instance.PID))
		if proc == 0 || objc.Send[bool](proc, sel("isTerminated")) {
			continue
		}
		birth := proc.Send(sel("launchDate"))
		stamp := strconv.FormatFloat(objc.Send[float64](birth, sel("timeIntervalSince1970")), 'g', -1, 64)
		if birth == 0 || stamp != instance.StartedAt || !sameBundle(proc, path) {
			return errors.New("Codex App 的运行状态已变化，请刷新后重试")
		}
		if !objc.Send[bool](proc, sel("terminate")) {
			return errors.New("Codex App 未接受退出请求，请从应用菜单退出后重试")
		}
	}
	return nil
}

func openCodexApp(ctx context.Context, app, endpoint string) (int, error) {
	type launchResult struct{ pid, code int }
	results := make(chan launchResult, 1)
	func() {
		defer appPool()()
		url := objc.ID(objc.GetClass("NSURL")).Send(sel("fileURLWithPath:isDirectory:"), appString(app), true)
		config := objc.ID(objc.GetClass("NSWorkspaceOpenConfiguration")).Send(sel("configuration"))
		config.Send(sel("setCreatesNewApplicationInstance:"), true)
		env := objc.ID(objc.GetClass("NSMutableDictionary")).Send(sel("dictionary"))
		env.Send(sel("setObject:forKey:"), appString(endpoint), appString("CODEX_APP_SERVER_WS_URL"))
		env.Send(sel("setObject:forKey:"), appString("0"), appString("CODEX_APP_SERVER_FORCE_CLI"))
		config.Send(sel("setEnvironment:"), env)
		done := objc.NewBlock(func(_ objc.Block, process, failure objc.ID) {
			result := launchResult{}
			if failure != 0 {
				result.code = objc.Send[int](failure, sel("code"))
			} else {
				result.pid = int(objc.Send[int32](process, sel("processIdentifier")))
			}
			results <- result
		})
		defer done.Release()
		workspace := objc.ID(objc.GetClass("NSWorkspace")).Send(sel("sharedWorkspace"))
		workspace.Send(sel("openApplicationAtURL:configuration:completionHandler:"), url, config, done)
	}()
	select {
	case <-ctx.Done():
		return 0, ctx.Err()
	case result := <-results:
		if result.pid <= 0 {
			return 0, fmt.Errorf("macOS 未能启动 Codex App（%d）", result.code)
		}
		return result.pid, nil
	}
}
