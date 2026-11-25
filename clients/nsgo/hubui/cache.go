package hubui

import (
	"context"
	"time"

	"github.com/monkeyWie/reignhub/pkg/persistence"

	"github.com/nsgo-dev/nsgo/core"
)

type workspaceCache struct{ providers, sessions []Object }

func (s *State) rememberSession() {
	if s.cancelPageRead != nil {
		s.cancelPageRead()
		s.cancelPageRead = nil
	}
	s.persistSnapshot()
	s.confirmedRowKeys = nil
	s.landmarks = nil
	s.landmarksKey = ""
	s.landmarksVersion++
	s.historyPrefix = historyPrefix{}
	s.openingRow = historyRowID{}
	s.livePage = nil
	s.reading = nil
	s.readingPages = nil
	s.readingOverlap = nil
	s.resetImages()
	if s.selected == nil || s.transcript == nil {
		return
	}
	if s.snapshots != nil && s.snapshots.storage != nil {
		return
	}
	if s.sessionCache == nil {
		s.sessionCache = map[string]Object{}
	}
	key := s.key()
	if _, exists := s.sessionCache[key]; !exists {
		s.sessionCacheOrder = append(s.sessionCacheOrder, key)
	}
	s.sessionCache[key] = s.snapshotProjection()
	// Disk remains the source for other sessions; bound the in-memory history.
	for len(s.sessionCacheOrder) > 6 {
		delete(s.sessionCache, s.sessionCacheOrder[0])
		s.sessionCacheOrder = s.sessionCacheOrder[1:]
	}
}

func (s *State) loadCachedWorkspace(target string) {
	if target == "" {
		return
	}
	c := s.catalogue(target)
	if c.cached {
		return
	}
	c.cached, c.cacheLoading = true, true
	s.catalogueProgress()
	go func() {
		v, err := s.api.Call(s.ctx, "client.cached", nil, target, nil)
		s.cx.Post(func() {
			if s.ctx.Err() != nil || s.catalogues[target] != c {
				return
			}
			c.cacheLoading = false
			s.catalogueProgress()
			// A slow disk result must never replace a newer remote catalogue.
			if err == nil {
				cached := object(v)
				w := s.workspace(target)
				if p := objects(cached["providers"]); len(p) > 0 && len(w.providers) == 0 {
					w.providers = visibleProviders(p)
				}
				known := map[string]bool{}
				for _, session := range w.sessions {
					known[sessionIdentity(session)] = true
				}
				for _, session := range objects(cached["sessions"]) {
					if visibleSession(session) && !c.completed[str(session, "providerId")] && !known[sessionIdentity(session)] {
						w.sessions = append(w.sessions, session)
					}
				}
				s.workspaces[target] = w
				s.publishWorkspaces()
			}
			s.changed()
		})
	}()
}

func (s *State) cancelSessionEntry() {
	if s.cancelEntry != nil {
		s.cancelEntry()
		s.cancelEntry = nil
	}
}

func openingPageQuery() persistence.PageQuery {
	return persistence.PageQuery{ExtendTurn: func(turn Object, items []Object) bool {
		if terminalTurn(turn) {
			for _, item := range items {
				if visibleTool(item) || str(item, "phase") == "commentary" {
					return true
				}
			}
		}
		return false
	}}
}

// Switch the heading, editor and history together when the local page is
// ready. A slow store gets the usual loading view and can never hold navigation.
func (s *State) prepareSession(session Object, target string) {
	route := persistence.Session{Env: target, Provider: str(session, "providerId"), ID: str(session, "id")}
	ctx, cancel := context.WithCancel(s.ctx)
	s.cancelEntry = cancel
	// Keep a single read alive across the handoff if the disk is slow.
	var opened bool
	var generation uint64
	open := func(page Object) {
		s.cancelEntry = nil
		s.openSession(session, target, page, true)
		generation = s.sessionGeneration
		opened = true
		s.cancelPageRead = cancel
	}
	timer := time.AfterFunc(100*time.Millisecond, func() {
		s.cx.Post(func() {
			if ctx.Err() == nil && !opened {
				open(nil)
				s.transcriptLoading = true
				s.changed()
			}
		})
	})
	dao := s.snapshots.storage
	go func() {
		defer timer.Stop()
		page, err := dao.ReadPage(ctx, route, openingPageQuery())
		s.cx.Post(func() {
			if ctx.Err() != nil {
				return
			}
			var cached Object
			if err == nil {
				cached = page.View()
			}
			if !opened {
				open(cached)
			} else if generation == s.sessionGeneration {
				s.applyCachedTranscript(cached)
			}
			s.cancelPageRead = nil
			cancel()
		})
	}()
}

func (s *State) loadCachedSession(pageRead bool) {
	key, generation := s.key(), s.sessionGeneration
	target, provider, session := s.target, str(s.selected, "providerId"), str(s.selected, "id")
	s.transcriptLoading = !pageRead && s.transcript == nil
	if !pageRead {
		go func() {
			request := Object{"method": "transcript", "provider": provider, "session": session}
			v, err := s.api.Call(s.ctx, "client.cached", nil, target, request)
			s.cx.Post(func() {
				if s.ctx.Err() != nil || s.key() != key || s.sessionGeneration != generation {
					return
				}
				var cached Object
				if err == nil && v != nil {
					cached = object(v)
				}
				s.applyCachedTranscript(cached)
			})
		}()
	}
	go func() {
		v, err := s.api.Call(s.ctx, "client.cached", nil, target, Object{"method": "models", "provider": provider, "session": session})
		s.cx.Post(func() {
			if s.ctx.Err() == nil && s.key() == key && s.sessionGeneration == generation && err == nil && len(s.models) == 0 {
				s.models = objects(v)
				s.changed()
			}
		})
	}()
}

func (s *State) applyCachedTranscript(cached Object) {
	s.transcriptLoading = false
	if cached != nil && s.transcript == nil {
		s.openingRow = firstHistoryRow(cached)
		s.setTranscript(cached)
		// A disk baseline may arrive after the initial watch was launched.
		// Resume only before any remote baseline or snapshot has started.
		if resume := object(cached["resume"]); str(resume, "epoch") != "" && s.stream != nil && s.stream.current() && !s.stream.baseline && !s.stream.snapshotInFlight {
			s.streamKey, s.streamEpoch, s.streamCursor = s.key(), str(resume, "epoch"), number(resume, "cursor")
			s.startSessionWatch()
		}
	} else if cached != nil {
		s.restoreOutgoing(cached)
		s.reconcileOutgoing(time.Time{})
		s.rebuildMessages()
	}
	s.changed()
}

func (s *State) cataloguePending() bool {
	if s.cacheLoading {
		return true
	}
	if s.polling {
		if len(s.catalogues) == 0 {
			return true
		}
		for _, c := range s.catalogues {
			if c.polling && (!c.loaded || c.manual) {
				return true
			}
		}
	}
	for _, h := range s.connections {
		if h.reconnecting {
			return true
		}
	}
	return false
}

func (s *State) emptyTranscript(cx *core.Scope) core.Node {
	return core.Show{When: func() bool { s.read(); return s.selected != nil && s.transcript == nil }, Then: func(cx *core.Scope) core.Node {
		pending := func() bool {
			s.read()
			return s.transcriptLoading || s.transcriptSyncing || s.connection(s.target).reconnecting || s.stream != nil && s.stream.current() && s.stream.snapshotInFlight
		}
		return core.View{TestID: "transcript-placeholder", Style: core.Style{Position: core.Absolute, Top: core.Pct(40), Left: core.Pt(24), Right: core.Pt(24), Gap: 12, AlignItems: core.AlignCenter}, Children: []core.Node{
			core.Show{When: pending, Then: func(*core.Scope) core.Node {
				return s.loadingRing("transcript-loading", "正在加载消息", 20)
			}},
			dynamicText(cx, func() string {
				if pending() {
					return "正在加载消息…"
				}
				if s.connected() {
					if s.stream != nil && s.stream.current() && s.stream.historyError != nil {
						_, detail := operationErrorText(s.stream.historyError.Error(), nil)
						return "消息加载失败：" + detail
					}
					return "消息尚未加载，请重试。"
				}
				return "此会话尚无本地消息，连接后会自动加载。"
			}, 13, true),
			core.Show{When: func() bool { return !pending() && s.connected() }, Then: func(cx *core.Scope) core.Node {
				return button(cx, "retry-transcript", "重试加载", nil, "outline", s.startSessionWatch)
			}},
		}}
	}}
}
