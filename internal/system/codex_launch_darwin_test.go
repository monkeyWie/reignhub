//go:build darwin && !ios

package system

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"

	"testing"
	"time"
)

func TestNativeCodexDesktopLaunchAndRestart(t *testing.T) {
	dir := t.TempDir()
	app := filepath.Join(dir, "Isolated launcher test.app")
	executable := filepath.Join(app, "Contents", "MacOS", "Codex")
	if err := os.MkdirAll(filepath.Dir(executable), 0700); err != nil {
		t.Fatal(err)
	}
	plist := `<?xml version="1.0" encoding="UTF-8"?>
<plist version="1.0"><dict>
<key>CFBundleExecutable</key><string>Codex</string>
<key>CFBundleIdentifier</key><string>dev.reignhub.fixture.codex-launcher</string>
<key>CFBundlePackageType</key><string>APPL</string>
<key>LSUIElement</key><true/>
</dict></plist>`
	if err := os.WriteFile(filepath.Join(app, "Contents", "Info.plist"), []byte(plist), 0600); err != nil {
		t.Fatal(err)
	}
	source := filepath.Join(dir, "fixture.m")
	content := `#import <AppKit/AppKit.h>
int main(void) {
    @autoreleasepool {
        NSApplication *app = [NSApplication sharedApplication];
        NSProcessInfo *process = [NSProcessInfo processInfo];
        NSString *url = process.environment[@"CODEX_APP_SERVER_WS_URL"];
        NSString *mode = process.environment[@"CODEX_APP_SERVER_FORCE_CLI"];
        NSString *dir = [[NSBundle mainBundle].bundlePath stringByDeletingLastPathComponent];
        NSString *file = [dir stringByAppendingPathComponent:[NSString stringWithFormat:@"%d", process.processIdentifier]];
        [[NSString stringWithFormat:@"%@ %@", url, mode] writeToFile:file atomically:YES encoding:NSUTF8StringEncoding error:nil];
        [app run];
    }
    return 0;
}`
	if err := os.WriteFile(source, []byte(content), 0600); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 25*time.Second)
	defer cancel()
	if out, err := exec.CommandContext(ctx, "/usr/bin/clang", "-framework", "AppKit", source, "-o", executable).CombinedOutput(); err != nil {
		t.Fatalf("compile temporary test App: %s (%v)", out, err)
	}
	t.Cleanup(func() {
		instances, _ := Discover(context.Background(), app)
		for _, instance := range instances {
			if process, err := os.FindProcess(instance.PID); err == nil {
				_ = process.Kill()
			}
		}
	})
	const first = "ws://127.0.0.1:1/fixture-one"
	pid, err := LaunchCodexDesktop(ctx, app, first, nil, false)
	if err != nil {
		t.Fatal(err)
	}
	waitDesktopFixtureFile(t, filepath.Join(dir, strconv.Itoa(pid)))
	instances, err := Discover(ctx, app)
	if err != nil || len(instances) != 1 || instances[0].PID != pid {
		t.Fatalf("native discovery returned %v, %v", instances, err)
	}
	const second = "ws://127.0.0.1:1/fixture-two"
	next, err := LaunchCodexDesktop(ctx, app, second, instances, true)
	if err != nil || pid == next {
		t.Fatalf("isolated native restart returned PID %d, %v", next, err)
	}
	for _, fixture := range []struct {
		pid int
		url string
	}{{pid, first}, {next, second}} {
		file := filepath.Join(dir, strconv.Itoa(fixture.pid))
		waitDesktopFixtureFile(t, file)
		if got, err := os.ReadFile(file); err != nil || string(got) != fixture.url+" 0" {
			t.Fatalf("fixture PID %d did not receive its launch environment: %q, %v", fixture.pid, got, err)
		}
	}
}
