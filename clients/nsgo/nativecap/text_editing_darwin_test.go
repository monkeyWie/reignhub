//go:build darwin && !ios

package nativecap

import (
	"runtime"
	"testing"

	"github.com/ebitengine/purego"
	"github.com/ebitengine/purego/objc"
)

func TestAppKitCapitalization(t *testing.T) {
	if _, err := purego.Dlopen("/System/Library/Frameworks/AppKit.framework/AppKit", purego.RTLD_NOW|purego.RTLD_GLOBAL); err != nil {
		t.Fatal(err)
	}
	runtime.LockOSThread()
	defer runtime.UnlockOSThread()
	sel := objc.RegisterName
	pool := objc.ID(objc.GetClass("NSAutoreleasePool")).Send(sel("new"))
	defer pool.Send(sel("drain"))
	defaults := objc.ID(objc.GetClass("NSUserDefaults")).Send(sel("standardUserDefaults"))
	nsString := func(s string) objc.ID {
		return objc.ID(objc.GetClass("NSString")).Send(sel("stringWithUTF8String:"), s)
	}
	domain := nsString("NSArgumentDomain")
	previous := defaults.Send(sel("volatileDomainForName:"), domain)
	defer defaults.Send(sel("setVolatileDomain:forName:"), previous, domain)
	args := previous.Send(sel("mutableCopy"))
	defer args.Send(sel("release"))
	enabled := objc.ID(objc.GetClass("NSNumber")).Send(sel("numberWithBool:"), true)
	args.Send(sel("setObject:forKey:"), enabled, nsString("NSAutomaticCapitalizationEnabled"))
	marker := nsString("ReignHubFixture")
	args.Send(sel("setObject:forKey:"), enabled, marker)
	defaults.Send(sel("setVolatileDomain:forName:"), args, domain)
	checker := objc.ID(objc.GetClass("NSSpellChecker"))
	if !objc.Send[bool](checker, sel("isAutomaticCapitalizationEnabled")) {
		t.Fatal("fixture must start with system capitalization enabled")
	}
	ConfigureTextEditing()
	if objc.Send[bool](checker, sel("isAutomaticCapitalizationEnabled")) || !objc.Send[bool](defaults, sel("boolForKey:"), marker) {
		t.Fatal("expected capitalization disabled with other process settings preserved")
	}
}
