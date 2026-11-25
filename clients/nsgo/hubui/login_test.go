package hubui

import (
	"context"
	"errors"
	"math"
	"testing"
	"time"

	"github.com/nsgo-dev/nsgo/core"
	"github.com/nsgo-dev/nsgo/nsgotest"
)

func TestEmailSignInCodeAndRetry(t *testing.T) {
	if isWeb() {
		t.Skip("browser delegates account sign-in to the account page")
	}
	f := &feedbackAPI{demoAPI: newDemoAPI(), requests: make(chan feedbackRequest, 8)}
	app := nsgotest.Start(t, func(cx *core.Scope) core.Node {
		s := newState(cx, f)
		s.applyColors()
		s.dialog.Set("account")
		return core.Dynamic(func(cx *core.Scope) core.Node { return s.dialogView(cx, s.dialog.Get()) })
	}, nsgotest.Size(390, 700), nsgotest.Environment(core.Environment{Locale: "zh-CN"}))
	const email = "alex@example.com"
	const otp = "id:account-otp >> label:/.+/"
	app.Type("id:account-email", email)
	field := app.Find("id:account-email").Input
	if field.CaretColor != field.Style.Color || field.AccentColor != field.Style.Color || field.AutoCapitalize != core.AutoCapitalizeNone {
		t.Fatal("email editor must use foreground for caret and composition and preserve typed case")
	}
	app.Screenshot("email-sign-in")
	app.Press("id:account-have-code")
	app.Type(otp, "01")
	cell := app.Find("id:account-otp").Children[0].Children[2]
	if app.Find(otp).Input.AccentColor != field.Style.Color || len(cell.Children) != 1 || cell.Children[0].Style.Background != field.Style.Color {
		t.Fatal("the code field must use the same neutral color for its drawn caret and input method")
	}
	app.Press("id:account-edit-email")
	if app.Find("id:account-email").Text != email {
		t.Fatal("changing email discarded the address")
	}
	app.Press("id:account-start")
	send := awaitFeedback(t, f)
	if send.method != "host.account.send" || str(send.params, "email") != email {
		t.Fatal("incorrect send request")
	}
	send.result <- nil
	app.WaitFor(otp, time.Second)
	app.Type(otp, "12")
	if !app.Find("id:account-verify").Props.Disabled {
		t.Fatal("incomplete codes enable sign-in")
	}
	if nsgotest.Target() == "" {
		app.Paste(otp, core.PasteEvent{Text: "０１２ ３４５"})
	} else {
		// Paste injects a synthetic platform event in the headless harness.
		app.Clear(otp)
		app.Type(otp, "012345")
	}
	if app.Find(otp).Text != "012345" {
		t.Fatal("pasting a six-digit code must replace the draft and keep the leading zero")
	}
	app.KeyPress(otp, "Backspace")
	app.Type(otp, "6")
	assertCodeFillsRow(t, app, "account-otp", "account-verify")
	app.Screenshot("email-verification")
	app.Press("id:account-verify")
	verify := awaitFeedback(t, f)
	if verify.method != "host.account.verify" || str(verify.params, "otp") != "012346" || str(verify.params, "email") != email {
		t.Fatal("incorrect verify request")
	}
	verify.result <- errors.New("Test code rejected")
	app.WaitFor("text:Test code rejected", time.Second)
	app.Press("id:account-start")
	resend := awaitFeedback(t, f)
	if resend.method != "host.account.send" {
		t.Fatal("resend did not call send")
	}
	resend.result <- nil
	app.WaitUntil(time.Second, func() bool { return app.Find(otp).Text == "" })
	app.Type(otp, "001122")
	app.Press("id:account-verify")
	awaitFeedback(t, f).result <- nil
	app.WaitUntil(time.Second, func() bool { return !app.Exists("id:dialog-account") })
}

func assertCodeFillsRow(t *testing.T, app *nsgotest.App, codeID, controlID string) {
	t.Helper()
	cells := app.Find("id:" + codeID).Children[0].Children
	if len(cells) != codeLength {
		t.Fatalf("expected six cells, got %d", len(cells))
	}
	control := globalBounds(app.Find("id:" + controlID))
	first, last := globalBounds(cells[0]), globalBounds(cells[len(cells)-1])
	near := func(a, b core.Points) bool { return math.Abs(float64(a-b)) <= 1 }
	if !near(first.Origin.X, control.Origin.X) || !near(last.Origin.X+last.Size.Width, control.Origin.X+control.Size.Width) {
		t.Fatalf("code row does not align with control: first=%v last=%v control=%v", first, last, control)
	}
	for _, cell := range cells {
		if first.Size.Width <= 0 || !near(first.Size.Width, cell.Frame.Size.Width) {
			t.Fatalf("unequal code cells: %v and %v", first.Size.Width, cell.Frame.Size.Width)
		}
	}
}

type loginMethodsFixture struct {
	*demoAPI
	start, poll, password chan Object
}

func (f *loginMethodsFixture) Call(ctx context.Context, method string, p Object, target string, request Object) (any, error) {
	switch method {
	case "host.account.status":
		return Object{"providers": []any{"email", "password", "google", "github"}}, nil
	case "host.account.start":
		f.start <- p
		return Object{"id": "fixture-id", "code": "AABBCCDDEEFF", "url": DefaultService + "/account?code=AABBCCDDEEFF&provider=" + str(p, "provider")}, nil
	case "host.account.poll":
		f.poll <- p
		return Object{"state": "approved"}, nil
	case "host.account.password":
		f.password <- p
		return Object{"ok": true}, nil
	}
	return f.demoAPI.Call(ctx, method, p, target, request)
}

func TestNativePasswordAndSocialLogin(t *testing.T) {
	if nsgotest.Target() != "" || isWeb() {
		t.Skip("uses the isolated headless URL handler")
	}
	for _, provider := range []string{"password", "github", "google"} {
		t.Run(provider, func(t *testing.T) {
			f := &loginMethodsFixture{newDemoAPI(), make(chan Object, 1), make(chan Object, 1), make(chan Object, 1)}
			app := nsgotest.Start(t, func(cx *core.Scope) core.Node {
				s := newState(cx, f)
				s.dialog.Set("account")
				return core.Dynamic(func(cx *core.Scope) core.Node { return s.dialogView(cx, s.dialog.Get()) })
			}, nsgotest.Size(390, 700))
			if provider == "password" {
				app.WaitFor("id:account-password-toggle", 2*time.Second)
				app.Press("id:account-password-toggle")
				app.Type("id:account-email", "fixture@example.invalid")
				app.Type("id:account-password", "fixture secret")
				app.Press("id:account-verify")
			} else {
				btn := "id:account-social-" + provider
				app.WaitFor(btn, 2*time.Second)
				app.Press(btn)
				app.WaitFor("text:AABBCCDDEEFF", time.Second)
			}
			app.WaitUntil(5*time.Second, func() bool { return !app.Exists("id:dialog-account") })
			if provider == "password" {
				if len(f.password) != 1 || str(<-f.password, "password") != "fixture secret" {
					t.Fatal("missing password submission")
				}
			} else if len(f.start) != 1 || str(<-f.start, "provider") != provider || len(f.poll) != 1 || str(<-f.poll, "id") != "fixture-id" {
				t.Fatal("device approval did not complete for the selected provider")
			}
		})
	}
}
