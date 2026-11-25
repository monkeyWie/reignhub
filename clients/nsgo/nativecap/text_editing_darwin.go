//go:build darwin && !ios

package nativecap

import "github.com/ebitengine/purego/objc"

// ConfigureTextEditing runs after AppKit starts and before the first editor opens.
// NSTextView reads capitalization from NSSpellChecker, including for shared field
// editors. The argument domain applies only for the lifetime of this process.
func ConfigureTextEditing() {
	sel := objc.RegisterName
	nsString := func(value string) objc.ID {
		return objc.ID(objc.GetClass("NSString")).Send(sel("stringWithUTF8String:"), value)
	}
	defaults := objc.ID(objc.GetClass("NSUserDefaults")).Send(sel("standardUserDefaults"))
	domain := nsString("NSArgumentDomain")
	overrides := defaults.Send(sel("volatileDomainForName:"), domain).Send(sel("mutableCopy"))
	defer overrides.Send(sel("release"))
	disabled := objc.ID(objc.GetClass("NSNumber")).Send(sel("numberWithBool:"), false)
	overrides.Send(sel("setObject:forKey:"), disabled, nsString("NSAutomaticCapitalizationEnabled"))
	defaults.Send(sel("setVolatileDomain:forName:"), overrides, domain)
}
