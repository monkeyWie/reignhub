package hubui

import (
	"net/url"

	"github.com/monkeyWie/reignhub/internal/control"
	"github.com/monkeyWie/reignhub/pkg/filelink"
	"github.com/nsgo-dev/nsgo/core"
	"github.com/nsgo-dev/nsgo/desktop"
)

func (s *State) openLink(cx *core.Scope, m Message, href string) {
	if u, err := url.Parse(href); err == nil && (u.Scheme == "https" || u.Scheme == "http") {
		cx.OpenURL(href, nil)
		return
	}
	if _, location := filelink.Path(href); location {
		return
	}
	if s.target == control.LocalTarget {
		cwd := str(s.selected, "cwd")
		if cwd == "" {
			cwd = str(object(s.transcript["session"]), "cwd")
		}
		if path := filelink.Resolve(href, cwd); path != "" {
			// The message row can be recycled while the system request completes.
			s.showNotice("正在打开文件位置…")
			desktop.RevealFile(s.cx, path, func(err error) {
				if err != nil {
					s.error = "无法显示文件位置：" + err.Error()
					s.showNotice("")
				} else {
					s.showNotice("已在文件管理器中显示")
				}
			})
			return
		}
	}
	for _, a := range m.Attachments {
		if str(a, "source") == href {
			if str(a, "state") != "ready" {
				s.showNotice("此文件暂不可用，请在所在设备上检查")
				return
			}
			s.download(cx, a)
			return
		}
	}
	// File references without a Provider-exported attachment cannot authorize
	// arbitrary remote filesystem reads. Explain the missing action explicitly.
	if path, _ := filelink.Path(href); path != "" {
		s.showNotice("此链接暂无下载信息。如设备使用旧版 ReignHub，请更新后重新打开会话")
	}
}
