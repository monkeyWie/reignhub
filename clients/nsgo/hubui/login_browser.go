package hubui

import (
	"context"
	"net/url"
	"strings"
	"time"

	"github.com/monkeyWie/reignhub/clients/nsgo/hubui/locales"
	"github.com/nsgo-dev/nsgo/core"
)

func loginServiceMethods(s *State, cx *core.Scope, service *core.Signal[string], prefix string) *core.Signal[map[string]bool] {
	methods := core.NewSignalFunc(cx, map[string]bool{"email": true}, nil)
	var stop context.CancelFunc
	cx.Cleanup(func() {
		if stop != nil {
			stop()
		}
	})
	cx.Effect(func() {
		origin := strings.TrimSpace(service.Get())
		if stop != nil {
			stop()
		}
		ctx, cancel := context.WithTimeout(s.ctx, 8*time.Second)
		stop = cancel
		methods.Set(map[string]bool{"email": true})
		name := s.clientName
		go func() {
			select {
			case <-ctx.Done():
				return
			case <-time.After(300 * time.Millisecond):
			}
			v, err := s.api.Call(ctx, prefix+"status", Object{"service": origin, "name": name}, "", nil)
			if err != nil {
				return
			}
			providers, ok := object(v)["providers"].([]any)
			if !ok {
				return
			}
			list := map[string]bool{}
			for _, provider := range providers {
				if key, ok := provider.(string); ok {
					list[key] = true
				}
			}
			s.cx.Post(func() {
				if ctx.Err() == nil {
					methods.Set(list)
				}
			})
		}()
	})
	return methods
}

func browserApprovalURL(origin string, login Object) (string, bool) {
	u, err := url.Parse(str(login, "url"))
	base, baseErr := url.Parse(origin)
	if err != nil || baseErr != nil || (u.Scheme != "https" && u.Scheme != "http") || u.User != nil || u.Scheme != base.Scheme || !strings.EqualFold(u.Host, base.Host) || u.Path != "/account" || str(login, "id") == "" || str(login, "code") == "" || u.Query().Get("code") != str(login, "code") {
		return "", false
	}
	return u.String(), true
}

func (s *State) browserApproval(cx *core.Scope, origin, prefix string, login Object, close func(), back func()) core.Node {
	msg := locales.For(cx)
	target, valid := browserApprovalURL(origin, login)
	if !valid {
		return core.View{Children: []core.Node{label(cx, msg.BrowserLoginFailed(), 13, false), button(cx, "account-back", msg.BackToLogin(), nil, "ghost", back)}}
	}
	owner, closeOwner := context.WithCancel(s.ctx)
	cx.Cleanup(closeOwner)
	ctx, cancel := context.WithTimeout(owner, 10*time.Minute)
	params := Object{"service": origin, "name": s.clientName, "id": str(login, "id")}
	status := core.NewSignal(cx, msg.BrowserLoginWait())
	launch := func() {
		cx.OpenURL(target, func(ok bool) {
			if !ok && ctx.Err() == nil {
				status.Set(msg.BrowserLoginFailed())
			}
		})
	}
	cx.Post(func() {
		if ctx.Err() == nil {
			launch()
		}
	})
	go func() {
		defer cancel()
		err := s.waitLoginApproval(ctx, prefix, params)
		s.cx.Post(func() {
			if owner.Err() != nil {
				return
			}
			if err != nil {
				status.Set(msg.BrowserLoginExpired())
				return
			}
			s.refreshAccountStatus()
			close()
			s.showNotice(msg.SignedIn())
		})
	}()
	return core.View{Style: core.Style{Gap: 12}, Children: []core.Node{
		dynamicText(cx, status.Get, 13, true), label(cx, str(login, "code"), 20, false),
		button(cx, "account-reopen", msg.OpenLoginBrowser(), nil, "primary", launch),
		button(cx, "account-back", msg.BackToLogin(), nil, "ghost", back),
	}}
}

func (s *State) waitLoginApproval(ctx context.Context, prefix string, params Object) error {
	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(2 * time.Second):
		}
		probe, cancel := context.WithTimeout(ctx, 8*time.Second)
		v, err := s.api.Call(probe, prefix+"poll", params, "", nil)
		cancel()
		if err == nil && str(object(v), "state") == "approved" {
			return nil
		}
	}
}
