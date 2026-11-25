package hubui

import (
	"context"
	"errors"
	"fmt"
	"reflect"
	"sort"
	"strings"
	"time"

	"github.com/monkeyWie/reignhub/internal/control"
	"github.com/monkeyWie/reignhub/internal/provider"
)

// Each environment owns its directory lifetime. Navigation never cancels it.
// Generations fence late callbacks after reconnect, removal or manual refresh.
type catalogueState struct {
	generation                            uint64
	cancel                                context.CancelFunc
	polling, manual, cached, cacheLoading bool
	loaded                                bool
	completed                             map[string]bool
	nextPoll                              time.Time
	pollInterval                          time.Duration
}

func (s *State) catalogue(target string) *catalogueState {
	if s.catalogues == nil {
		s.catalogues = map[string]*catalogueState{}
	}
	if s.catalogues[target] == nil {
		s.catalogues[target] = &catalogueState{completed: map[string]bool{}}
	}
	return s.catalogues[target]
}
func (s *State) anyConnected() bool {
	for _, d := range s.devices {
		if s.connectionReady(str(d, "id")) {
			return true
		}
	}
	return false
}
func (s *State) cancelTargetCatalogue(target string) {
	c := s.catalogue(target)
	if c.cancel != nil {
		c.cancel()
		c.cancel = nil
	}
	c.generation++
	c.nextPoll = time.Time{}
	c.pollInterval = 0
	c.polling, c.manual = false, false
	s.catalogueProgress()
}
func (s *State) catalogueProgress() {
	s.polling, s.manualRefresh, s.cacheLoading = false, false, false
	for _, c := range s.catalogues {
		s.polling = s.polling || c.polling
		s.manualRefresh = s.manualRefresh || c.manual
		s.cacheLoading = s.cacheLoading || c.cacheLoading
	}
}
func (s *State) reconcileEnvironments() {
	known := map[string]bool{}
	for _, d := range s.devices {
		id := str(d, "id")
		known[id] = true
		c := s.catalogue(id)
		w := s.workspace(id)
		s.workspaces[id] = w
		if !c.cached {
			s.loadCachedWorkspace(id)
		}
		if !flag(d, "connected") && (c.polling || !c.nextPoll.IsZero()) {
			s.cancelTargetCatalogue(id)
		}
	}
	for id := range s.catalogues {
		if !known[id] {
			s.cancelTargetCatalogue(id)
			delete(s.catalogues, id)
			delete(s.workspaces, id)
		}
	}
	s.publishWorkspaces()
	s.catalogueVersion++
	s.pollCatalogues()
}
func (s *State) pollCatalogues() {
	if s.ctx.Err() != nil {
		return
	}
	manual := s.manualRefresh
	for _, d := range s.devices {
		target := str(d, "id")
		c := s.catalogue(target)
		if c.polling || !s.connectionReady(target) || !manual && time.Now().Before(c.nextPoll) {
			continue
		}
		c.polling, c.manual = true, manual
		c.generation++
		generation := c.generation
		ctx, cancel := context.WithCancel(s.ctx)
		c.cancel = cancel
		known := map[string]bool{}
		for _, session := range s.workspaces[target].sessions {
			known[str(session, "providerId")] = true
		}
		go s.loadCatalogue(ctx, cancel, c, generation, target, known, s.workspace(target))
	}
	s.catalogueProgress()
	s.changed()
}

// Metadata only: no background transcript downloads for unselected sessions.
func (s *State) loadCatalogue(ctx context.Context, cancel context.CancelFunc, instance *catalogueState, generation uint64, target string, known map[string]bool, before workspaceCache) {
	defer cancel()
	current := func() bool {
		return s.ctx.Err() == nil && s.catalogues[target] == instance && generation == s.catalogues[target].generation
	}
	v, err := rpc(ctx, s.api, target, "", "", "providers", nil)
	providers := visibleProviders(objects(v))
	if err == nil {
		s.cx.Post(func() {
			if !current() {
				return
			}
			w := s.workspace(target)
			if !reflect.DeepEqual(w.providers, providers) {
				s.catalogueVersion++
			}
			w.providers = providers
			s.workspaces[target] = w
			s.publishWorkspaces()
			s.changed()
		})
		results := make(chan error, len(providers))
		slots := make(chan struct{}, 4)
		count := 0
		for _, p := range providers {
			// History is a capability, not availability or deletion authority. Retain
			// cached sessions and resume synchronization when the Agent becomes ready.
			if p["installed"] == false || !providerReady(p) {
				continue
			}
			providerID := str(p, "id")
			count++
			go func() {
				select {
				case slots <- struct{}{}:
					defer func() { <-slots }()
				case <-ctx.Done():
					results <- ctx.Err()
					return
				}
				if !known[providerID] {
					page, previewErr := rpc(ctx, s.api, target, providerID, "", "sessions", nil)
					if previewErr == nil {
						sessions := objects(object(page)["sessions"])
						s.cx.Post(func() {
							if current() {
								s.applyEnvironmentPage(target, providerID, sessions, false)
							}
						})
					}
				}
				list, syncErr := s.api.Call(ctx, "client.sync", Object{"provider": providerID}, target, nil)
				if syncErr == nil {
					sessions := objects(list)
					s.cx.Post(func() {
						if current() {
							s.applyEnvironmentPage(target, providerID, sessions, true)
						}
					})
				}
				if syncErr != nil {
					syncErr = fmt.Errorf("%s: %w", providerID, syncErr)
				}
				results <- syncErr
			}()
		}
		var failures []error
		for range count {
			if e := <-results; e != nil {
				failures = append(failures, e)
			}
		}
		err = errors.Join(failures...)
	}
	s.cx.Post(func() {
		if !current() {
			return
		}
		c := s.catalogue(target)
		after := s.workspace(target)
		quiet := c.loaded && !c.manual && target != control.LocalTarget && reflect.DeepEqual(before.providers, after.providers) && reflect.DeepEqual(before.sessions, after.sessions)
		if quiet && err == nil {
			for _, session := range after.sessions {
				if sessionRunning(session) {
					quiet = false
					break
				}
			}
		}
		if quiet {
			c.pollInterval = min(max(c.pollInterval, 2*time.Second)*2, 30*time.Second)
		} else {
			c.pollInterval = 2 * time.Second
		}
		c.polling, c.manual, c.cancel = false, false, nil
		c.loaded = true
		c.nextPoll = time.Now().Add(c.pollInterval)
		s.catalogueProgress()
		s.setConnectionIssue(target, "catalogue", err)
		s.changed()
	})
}
func (s *State) workspace(target string) workspaceCache {
	if s.workspaces == nil {
		s.workspaces = map[string]workspaceCache{}
	}
	w, ok := s.workspaces[target]
	if !ok {
		for _, session := range s.sessions {
			id := str(session, "deviceId")
			if id == target || id == "" && target == s.target {
				w.sessions = append(w.sessions, session)
			}
		}
		if target == s.target {
			w.providers = s.providers
		}
	}
	return w
}
func (s *State) publishWorkspaces() {
	keys := make([]string, 0, len(s.workspaces))
	for id := range s.workspaces {
		keys = append(keys, id)
	}
	sort.Strings(keys)
	var sessions []Object
	for _, id := range keys {
		for _, session := range s.workspaces[id].sessions {
			if !visibleSession(session) {
				continue
			}
			entry := copyObject(session)
			entry["deviceId"] = id
			if s.selected != nil && id == s.target && str(entry, "id") == str(s.selected, "id") && str(entry, "providerId") == str(s.selected, "providerId") {
				title := str(entry, "title")
				if title != "" && title != str(s.selected, "title") || flag(entry, "codexOwnerUnreachable") != flag(s.selected, "codexOwnerUnreachable") {
					s.selected = copyObject(s.selected)
					if title != "" {
						s.selected["title"] = title
					}
					if flag(entry, "codexOwnerUnreachable") {
						s.selected["codexOwnerUnreachable"] = true
					} else {
						delete(s.selected, "codexOwnerUnreachable")
					}
				}
			}
			sessions = append(sessions, entry)
		}
	}
	s.providers = s.workspaces[s.target].providers
	if !reflect.DeepEqual(s.sessions, sessions) {
		s.sessions = sessions
		s.catalogueVersion++
	}
	s.restoreLastSession()
}
func (s *State) applyCataloguePage(providerID string, incoming []Object, complete bool) {
	s.applyEnvironmentPage(s.target, providerID, incoming, complete)
}
func (s *State) applyEnvironmentPage(target, providerID string, incoming []Object, complete bool) {
	s.observeSessionActivity(target, incoming)
	if complete {
		s.reconcileSessionRemovals(target, providerID, incoming)
	}
	w := s.workspace(target)
	merged := map[string]Object{}
	previous := map[string]Object{}
	for _, session := range w.sessions {
		previous[str(session, "providerId")+"|"+str(session, "id")] = session
		if visibleSession(session) && (!complete || str(session, "providerId") != providerID) {
			merged[str(session, "providerId")+"|"+str(session, "id")] = session
		}
	}
	for _, session := range incoming {
		if str(session, "providerId") == providerID && visibleSession(session) {
			key := providerID + "|" + str(session, "id")
			next := copyObject(session)
			if strings.TrimSpace(str(next, "title")) == "" {
				next["title"] = previous[key]["title"]
			}
			next["recencyAt"] = max(number(previous[key], "recencyAt"), number(session, "recencyAt"))
			merged[key] = next
		}
	}
	w.sessions = make([]Object, 0, len(merged))
	for _, session := range merged {
		w.sessions = append(w.sessions, session)
	}
	sort.Slice(w.sessions, func(i, j int) bool { return sessionIdentity(w.sessions[i]) < sessionIdentity(w.sessions[j]) })
	s.workspaces[target] = w
	if complete {
		s.catalogue(target).completed[providerID] = true
	}
	s.publishWorkspaces()
	s.changed()
}
func visibleSession(s Object) bool {
	return str(s, "providerId") != "codex-vscode" && str(s, "providerId") != "codex-cli" && provider.SyncSession(provider.Session{ProviderID: str(s, "providerId"), Source: str(s, "source"), SourceKind: str(s, "sourceKind"), Originator: str(s, "originator")})
}
