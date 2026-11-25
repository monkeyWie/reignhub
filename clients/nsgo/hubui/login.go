package hubui

import (
	"regexp"
	"strings"

	"github.com/monkeyWie/reignhub/clients/nsgo/hubui/locales"
	"github.com/nsgo-dev/nsgo/base"
	"github.com/nsgo-dev/nsgo/core"
	"github.com/nsgo-dev/nsgo/ui"
)

const codeLength = 6

var loginEmailPattern = regexp.MustCompile(`^[^\s@]+@[^\s@]+\.[^\s@]+$`)

func completeCode(value string) bool {
	return len(value) == codeLength && strings.Trim(value, "0123456789") == ""
}

func formField(cx *core.Scope, title string, input core.Node) core.Node {
	return core.View{Style: core.Style{Gap: 6}, Children: []core.Node{label(cx, title, 13, false), input}}
}

func codeField(cx *core.Scope, id, title string, code *core.Signal[string], disabled func() bool, ref *base.OtpInputRef) core.Node {
	input := ui.OtpInput{TestID: id, Label: title, Length: codeLength, Size: 48,
		Value: code.Get, OnChange: code.Set, Ref: ref, Disabled: disabled,
		Style: core.Style{AlignSelf: core.AlignStretch}}
	return formField(cx, title, core.Func(func(cx *core.Scope) core.Node {
		useEditorTheme(cx)
		// Keep the themed cells and native editor, but share the form width evenly.
		field := input.Render(cx).(base.OtpInput)
		field.GroupStyle.Width = core.Pct(100)
		field.GroupStyle.Gap = 8
		field.CellStyle.Width = core.Pt(0)
		field.CellStyle.FlexGrow = 1
		field.CellStyle.FontSize = 18
		return field
	}))
}

func (s *State) emailSignIn(cx *core.Scope, service *core.Signal[string], prefix string, close func()) core.Node {
	msg := locales.For(cx)
	email, code := core.NewSignal(cx, ""), core.NewSignal(cx, "")
	// Each issued code gets a fresh native field, including after a resend.
	challenge := core.NewSignal(cx, 0)
	showService := core.NewSignal(cx, false)
	methods := loginServiceMethods(s, cx, service, prefix)
	passwordMode := core.NewSignal(cx, false)
	passwordOnly := core.NewMemo(cx, func() bool { opts := methods.Get(); return opts["password"] && !opts["email"] })
	approval := core.NewSignalFunc(cx, Object(nil), nil)
	ref := new(base.OtpInputRef)
	open := true
	cx.Cleanup(func() { open = false })
	pending := func() bool {
		return s.actionPending("account-start") || s.actionPending("account-verify") || s.actionPending("account-social-github") || s.actionPending("account-social-google")
	}
	address := func() string { return strings.TrimSpace(email.Get()) }
	canSend := func() bool {
		return !pending() && len(address()) <= 254 && loginEmailPattern.MatchString(address()) && strings.TrimSpace(service.Get()) != ""
	}
	showCode := func() {
		if open {
			challenge.Set(challenge.GetUntracked() + 1)
			cx.Post(func() {
				if open {
					ref.Focus()
				}
			})
		}
	}
	send := func() {
		if !canSend() {
			return
		}
		params := Object{"service": service.Get(), "name": s.clientName, "email": address()}
		s.call("account-start", prefix+"send", params, func(any) {
			if open {
				code.Set("")
				showCode()
				s.showNotice(msg.LoginCodeSent())
			}
		})
	}
	finish := func(v any) {
		s.refreshAccountStatus()
		close()
		s.showNotice(choose(flag(object(v), "hostLimit"), msg.SignedInHostLimit(), msg.SignedIn()))
	}
	verify := func() {
		if !canSend() || !completeCode(code.Get()) {
			return
		}
		s.call("account-verify", prefix+"verify", Object{"service": service.Get(), "name": s.clientName, "email": address(), "otp": code.Get()}, finish)
	}
	return core.Dynamic(func(cx *core.Scope) core.Node {
		if login := approval.Get(); login != nil {
			return s.browserApproval(cx, service.GetUntracked(), prefix, login, close, func() { approval.Set(nil) })
		}
		if challenge.Get() != 0 {
			edit := button(cx, "account-edit-email", msg.EditLoginEmail(), nil, "ghost", func() {
				code.Set("")
				s.error = ""
				s.changed()
				challenge.Set(0)
			}).(core.View)
			edit.Style.MinHeight = core.Pt(44)
			edit.Disabled = pending
			recipient := singleLine(cx, address, 13, true)
			recipient.Style.FlexGrow = 1
			resend := s.actionButton(cx, "account-start", msg.ResendLoginCode(), nil, "ghost", send).(core.View)
			resend.Style.MinHeight = core.Pt(44)
			resend.Disabled = func() bool { return !canSend() }
			login := s.actionButton(cx, "account-verify", msg.ConfirmLogin(), nil, "primary", verify).(core.View)
			login.Style.MinHeight = core.Pt(44)
			login.Disabled = func() bool { return !canSend() || !completeCode(code.Get()) }
			return core.View{Style: core.Style{Gap: 14}, Children: []core.Node{
				core.View{Style: core.Style{Direction: core.Row, AlignItems: core.AlignCenter}, Children: []core.Node{recipient, edit}},
				codeField(cx, "account-otp", msg.LoginEmailCode(), code, pending, ref), login, resend,
			}}
		}
		password := core.NewSignal(cx, "")
		usePassword := passwordMode.Get() || passwordOnly.Get()
		submit := func() {
			if usePassword {
				if canSend() && password.Get() != "" {
					s.call("account-verify", prefix+"password", Object{"service": service.Get(), "name": s.clientName, "email": address(), "password": password.Get()}, finish)
				}
			} else {
				send()
			}
		}
		input := formInput{TestID: "account-email", Label: msg.LoginEmail(), Placeholder: "name@example.com", KeyboardType: core.KeyboardEmail,
			AutoComplete: core.AutoCompleteEmail, NoAutoCorrect: true,
			Value: email.Get, OnChange: email.Set, Disabled: pending, OnSubmit: func(string) { submit() }, Style: core.Style{Height: core.Pt(44)}}
		children := []core.Node{label(cx, msg.LoginEmailHint(), 13, true), formField(cx, msg.LoginEmail(), input)}
		if !s.api.CanHost() {
			children = append(children, ui.Collapsible{Text: msg.LoginService(), Open: showService.Get, OnOpenChange: showService.Set, Disabled: pending,
				Content: formInput{TestID: "account-service", Label: msg.LoginService(), Value: service.Get, OnChange: service.Set, Disabled: pending, Style: core.Style{Height: core.Pt(44)}}})
		}
		if usePassword {
			children = append(children, formField(cx, msg.LoginPassword(), formInput{TestID: "account-password", Label: msg.LoginPassword(), Masked: true, AutoComplete: core.AutoCompletePassword, Value: password.Get, OnChange: password.Set, Disabled: pending, OnSubmit: func(string) { submit() }, Style: core.Style{Height: core.Pt(44)}}))
		}
		start := s.actionButton(cx, choose(usePassword, "account-verify", "account-start"), choose(usePassword, msg.ConfirmLogin(), msg.SendLoginCode()), nil, "primary", submit).(core.View)
		start.Style.MinHeight = core.Pt(44)
		start.Disabled = func() bool { return !canSend() || (usePassword && password.Get() == "") }
		children = append(children, start)
		if !usePassword {
			existing := button(cx, "account-have-code", msg.HaveLoginCode(), nil, "ghost", showCode).(core.View)
			existing.Disabled = start.Disabled
			children = append(children, existing)
		}
		children = append(children, core.Dynamic(func(cx *core.Scope) core.Node {
			opts := methods.Get()
			var choices []core.Node
			if opts["password"] && opts["email"] {
				toggle := button(cx, "account-password-toggle", choose(usePassword, msg.UseEmailCode(), msg.UsePassword()), nil, "ghost", func() { passwordMode.Set(!usePassword) }).(core.View)
				toggle.Disabled = pending
				choices = append(choices, toggle)
			}
			var social []core.Node
			for _, p := range []string{"github", "google"} {
				if !opts[p] {
					continue
				}
				btn := s.actionButton(cx, "account-social-"+p, choose(p == "github", "GitHub", "Google"), nil, "outline", func() {
					args := Object{"service": service.Get(), "name": s.clientName, "provider": p}
					s.call("account-social-"+p, prefix+"start", args, func(result any) {
						if open {
							approval.Set(object(result))
						}
					})
				}).(core.View)
				btn.Disabled = pending
				btn.Style.FlexGrow = 1
				btn.Style.MinHeight = core.Pt(44)
				social = append(social, btn)
			}
			if len(social) > 0 {
				choices = append(choices, core.View{Style: core.Style{Direction: core.Row, Gap: 8}, Children: social})
			}
			if len(choices) == 0 {
				return nil
			}
			return core.View{Style: core.Style{Gap: 8}, Children: choices}
		}))
		return core.View{Style: core.Style{Gap: 12}, Children: children}
	})
}
