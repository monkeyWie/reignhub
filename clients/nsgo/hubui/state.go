package hubui

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"github.com/monkeyWie/reignhub/pkg/persistence"
	"strings"
	"time"

	"github.com/monkeyWie/reignhub/internal/control"
	"github.com/monkeyWie/reignhub/internal/device"
	"github.com/nsgo-dev/nsgo/core"
)

type draftState struct {
	Text, Model, ModelBase string
	Uploads                []*Upload
}

type State struct {
	confirmedRowKeys map[string]string
	cancelEntry      context.CancelFunc
	cancelPageRead   context.CancelFunc
	terminalPanels   map[string]*terminalPanel
	composerRef      *core.Ref
	draftWriter      *draftWriter
	draftSaveFailed  bool
	draftLoads       map[string]string
	restoreSession   Object
	pinnedRefs       map[string]*core.Ref
	pinDrag          *pinnedDrag

	submittedDraft, submittedDraftSession                   string
	directoryGeneration                                     uint64
	mobileSidebarOpen                                       *core.Signal[bool]
	clientName                                              string
	drafts                                                  map[string]draftState
	expanded                                                map[string]bool
	createProject                                           []projectLocation
	cx                                                      *core.Scope
	api                                                     API
	ctx                                                     context.Context
	cancel                                                  context.CancelFunc
	revision                                                *core.Signal[int]
	width                                                   *core.Signal[core.Points]
	dialog                                                  *core.Signal[string]
	hostAuthorization                                       *core.Signal[string]
	devices, localProviders, providers, sessions, approvals []Object
	agentDefinitions                                        Object
	host, selected, transcript, scanned                     Object
	accountLogin, accountStatus                             Object
	accountStatusLoading                                    bool
	accountDevices                                          []Object
	target, error, notice, model                            string
	modelBase                                               string
	noticeGeneration                                        uint64
	noticeTimer                                             func() bool
	failure                                                 *sessionOperationError
	copiedValue                                             string
	copyVersion                                             uint64
	copyTimer                                               func() bool
	ready, polling, dark                                    bool
	manualRefresh                                           bool
	actions                                                 map[string]bool
	questionDrafts                                          map[string]map[string]questionDraft
	draft                                                   *core.Signal[string]
	sessionWidth                                            *core.Signal[core.Points]
	sidebarHidden                                           *core.Signal[bool]
	collapsed                                               map[string]bool
	models                                                  []Object
	messageRows                                             []Message
	uploads                                                 []*Upload
	uploadQueue                                             []*Upload
	uploadActive                                            int
	downloadTask                                            *attachmentDownload
	images                                                  map[string][]byte
	loadingImages                                           map[string]bool
	imageOrder                                              []string
	imageBytes                                              int
	imageContext                                            context.Context
	imageCancel                                             context.CancelFunc
	preview                                                 []Object
	previewIndex                                            *core.Signal[int]
	watchCancel                                             context.CancelFunc
	prompted                                                map[string]bool
	retrying                                                map[string]time.Time
	connections                                             map[string]*connectionHealth
	connectionDetailTarget                                  string
	presencePolling                                         bool
	presenceGeneration                                      uint64
	devicePresence                                          map[string]Object
	directoryChecking                                       bool
	directoryChecked                                        time.Time
	directoryCancel                                         context.CancelFunc
	timezone                                                *time.Location
	timeline                                                *messageScrollAnchor
	loadingMotion                                           *loadingMotion
	pendingSends                                            map[string]string
	outgoing                                                map[string][]*outgoingMessage
	outgoingRemoved                                         map[string]map[string]bool
	streamKey, streamEpoch                                  string
	streamCursor                                            int64
	connectAttempt                                          *connectAttempt
	workspaces                                              map[string]workspaceCache
	catalogues                                              map[string]*catalogueState
	grouping                                                string
	filters, recentFilters, pinnedFilters                   sessionFilters
	removedSessions                                         map[string]bool
	removalTarget                                           Object
	uncertainRemovals                                       map[string]bool
	unreadSessions                                          map[string]bool
	sessionActivity                                         map[string]string
	pinned                                                  map[string]int64
	deviceMenuTarget                                        string
	sessionCache                                            map[string]Object
	sessionCacheOrder                                       []string
	livePage                                                Object
	openingRow                                              historyRowID
	historyPrefix                                           historyPrefix
	reading, readingOverlap                                 Object
	landmarks                                               []persistence.Landmark
	landmarksKey                                            string
	landmarksVersion                                        uint64
	readingPages                                            []persistence.HistoryPage
	cacheLoading, transcriptLoading, localPolling           bool
	localProviderGeneration                                 uint64
	catalogueVersion, sessionGeneration                     uint64
	transcriptSyncing, snapshotScheduled                    bool
	syncVisible                                             bool
	stream                                                  *sessionStream
	animateText                                             map[string]bool
	snapshots                                               *snapshotWriter
	backgrounded, keepAlive, notificationAsked              bool
	nativeBackgroundState                                   string
	lastTask                                                Object
	pendingNativeSession                                    Object
	nativeTasks                                             map[string]Object
	nativeTaskMonitors                                      map[string]*nativeTaskMonitor

	appearance, settingsReturn, settingsSection string
	rootBackDeadline                            time.Time
}

func newState(cx *core.Scope, api API) *State {
	identified := &identifiedAPI{API: api}
	identified.setInfo(platformClientInfo())
	api = identified
	ctx, cancel := context.WithCancel(context.Background())
	s := &State{clientName: platformDeviceName(), drafts: map[string]draftState{}, expanded: map[string]bool{}, cx: cx, api: api, ctx: ctx, cancel: cancel, revision: core.NewSignal(cx, 0), width: core.NewSignal(cx, core.Points(1320)), dialog: core.NewSignal(cx, ""), draft: core.NewSignal(cx, ""), sessionWidth: core.NewSignal(cx, core.Points(300)), collapsed: map[string]bool{}, images: map[string][]byte{}, loadingImages: map[string]bool{}, previewIndex: core.NewSignal(cx, 0), prompted: map[string]bool{}, retrying: map[string]time.Time{}}
	s.snapshots = newSnapshotWriter(ctx, identified.API, s.historySaved)
	s.hostAuthorization = core.NewSignal(cx, "")
	s.loadingMotion = newLoadingMotion(cx)
	s.mobileSidebarOpen = core.NewSignal(cx, false)
	s.sidebarHidden = core.NewSignal(cx, false)
	s.loadSidebarPreferences()
	s.loadLastSession()
	cx.Cleanup(func() {
		cancel()
		if s.watchCancel != nil {
			s.watchCancel()
		}
	})
	cx.OnQuit(func(ctx context.Context) {
		s.persistDraft()
		if s.draftWriter != nil {
			_ = s.draftWriter.flush(ctx)
		}
		if !isWeb() {
			cancel()
			go api.Close()
		}
	})
	return s
}
func (s *State) read()    { s.revision.Get() }
func (s *State) changed() { s.revision.Update(func(n int) int { return n + 1 }) }
func (s *State) async(work func(context.Context) (any, error), done func(any)) {
	go func() {
		v, err := work(s.ctx)
		s.cx.Post(func() {
			if s.ctx.Err() != nil {
				return
			}
			if err != nil {
				s.error = err.Error()
				s.failure = nil
				_ = errors.As(err, &s.failure)
			} else if done != nil {
				done(v)
			}
			s.changed()
		})
	}()
}
func (s *State) call(action, method string, p Object, done func(any)) {
	s.runAction(action, func(ctx context.Context) (any, error) { return s.api.Call(ctx, method, p, "", nil) }, func(v any) {
		if done != nil {
			done(v)
		}
		s.poll()
	})
}
func (s *State) initialize() {
	s.draftLoads = map[string]string{}
	s.draftWriter = newDraftWriter(s.ctx, s.api, func(err error) {
		s.cx.Post(func() {
			if s.ctx.Err() == nil {
				s.draftSaveFailed = s.draftWriter.hasFailures()
				if err != nil {
					s.error = "草稿保存失败：" + err.Error()
				} else if !s.draftSaveFailed && strings.HasPrefix(s.error, "草稿保存失败：") {
					s.error = ""
				}
				s.changed()
			}
		})
	})
	// Saved devices are local data; native host startup must not delay browsing.
	s.pollPresence()
	s.observeLifecycle()
	s.refreshTimezone()
	resolveClientInfo(s.cx, func(info device.ClientInfo) {
		s.clientName = info.Name
		s.api.(*identifiedAPI).setInfo(info)
		s.changed()
	})
	s.async(func(ctx context.Context) (any, error) {
		if s.api.CanHost() {
			if _, e := s.api.Call(ctx, "host.bootstrap", nil, "", nil); e != nil {
				return nil, e
			}
			v, e := s.api.Call(ctx, "host.info", nil, "", nil)
			if e != nil {
				return nil, e
			}
			info := object(v)
			params := Object{"serviceUrl": str(info, "serviceUrl"), "name": str(info, "name")}
			configure := false
			if str(info, "serviceUrl") == "" {
				params["serviceUrl"] = serviceOrigin()
				configure = true
			}
			if name := str(info, "name"); name == "" || name == "RAS" || name == "Agent Hub" {
				params["name"] = platformDeviceName()
				configure = true
			}
			if configure {
				if _, e = s.api.Call(ctx, "host.configure", params, "", nil); e != nil {
					return nil, e
				}
			}
		}
		return nil, nil
	}, func(any) { s.ready = true; s.poll(); s.refreshAccountStatus() })
	go func() {
		tick := time.NewTicker(2 * time.Second)
		defer tick.Stop()
		for {
			select {
			case <-s.ctx.Done():
				return
			case <-tick.C:
				s.cx.Post(s.poll)
			}
		}
	}()
}

// Local host discovery and the selected remote catalogue have independent
// lifetimes. A slow local Agent or remote device cannot block cached navigation.
func (s *State) pollLocal() {
	if !s.ready || !s.api.CanHost() || s.localPolling {
		return
	}
	s.localPolling = true
	generation := s.localProviderGeneration
	go func() {
		info, err := s.api.Call(s.ctx, "host.info", nil, "", nil)
		var providers any
		if err == nil {
			providers, err = s.api.Call(s.ctx, "providers", nil, "", nil)
		}
		s.cx.Post(func() {
			s.localPolling = false
			if s.ctx.Err() != nil {
				return
			}
			if info != nil {
				s.host = object(info)
			}
			if providers != nil && generation == s.localProviderGeneration {
				s.localProviders = visibleProviders(objects(providers))
			}
			s.setConnectionIssue("", "catalogue", err)
			s.changed()
			s.promptHost()
			if generation != s.localProviderGeneration {
				s.pollLocal()
			}
		})
	}()
}

func (s *State) cancelCatalogue() {
	for target := range s.catalogues {
		s.cancelTargetCatalogue(target)
	}
	s.polling = false
	s.manualRefresh = false
}

func (s *State) refreshSessions() {
	if s.manualRefresh || !s.anyConnected() || s.ctx.Err() != nil {
		return
	}
	// A manual refresh supersedes a background read instead of showing its
	// progress or accepting an older response as the user's fresh result.
	s.cancelCatalogue()
	s.manualRefresh = true
	s.poll()
}

func (s *State) poll() {
	if s.ctx.Err() != nil {
		return
	}
	s.pollPresence()
	s.pollLocal()
	s.pollCatalogues()
}

// Presence is local transport state. Never block reconnect detection behind a
// slow transcript/catalogue transfer, and never impose a business-read deadline.
func (s *State) pollPresence() {
	if s.presencePolling {
		return
	}
	s.presencePolling = true
	s.presenceGeneration++
	generation := s.presenceGeneration
	go func() {
		v, err := s.api.Call(s.ctx, "client.list", nil, "", nil)
		s.cx.Post(func() {
			if generation != s.presenceGeneration {
				return
			}
			s.presencePolling = false
			if s.ctx.Err() != nil {
				return
			}
			s.setConnectionIssue("", "presence", err)
			if err == nil {
				s.devices = objects(v)
				s.reconcileEnvironments()
				s.observeTransports()
				s.openNativeSession()
				s.refreshDevicePresence()
			}
			s.changed()
			s.autoReconnect()
		})
	}()
}
func visibleProviders(in []Object) []Object {
	out := []Object{}
	for _, p := range in {
		if str(p, "id") != "codex-vscode" && str(p, "id") != "codex-cli" {
			if str(p, "id") == "codex-app" {
				p = copyObject(p)
				p["name"] = "Codex"
			}
			out = append(out, p)
		}
	}
	return out
}
func providerReady(p Object) bool {
	return str(p, "state") == "ready" || str(p, "state") == "connected"
}
func (s *State) device() Object {
	for _, d := range s.devices {
		if str(d, "id") == s.target {
			return d
		}
	}
	return Object{}
}
func (s *State) connected() bool { s.read(); return s.connectionReady(s.target) }
func (s *State) key() string {
	return s.target + "|" + str(s.selected, "providerId") + "|" + str(s.selected, "id")
}
func (s *State) rememberDraft() {
	if s.selected != nil && s.draftReady() {
		s.drafts[s.key()] = draftState{Text: s.draft.GetUntracked(), Model: s.model, ModelBase: s.modelBase, Uploads: s.uploads}
	}
}
func (s *State) selectDevice(id string) {
	s.cancelSessionEntry()
	if s.target == id {
		return
	}
	s.persistDraft()
	s.rememberDraft()
	s.rememberSession()
	if s.watchCancel != nil {
		s.watchCancel()
	}
	s.target = id
	s.selected = nil
	s.transcriptSyncing = false
	s.transcript = nil
	s.messageRows = nil
	s.approvals = nil
	s.providers = s.workspaces[id].providers
	s.draft.Set("")
	s.uploads = nil
	s.changed()
	s.loadCachedWorkspace(id)
	s.poll()
}
func (s *State) selectSession(session Object) {
	s.cancelSessionEntry()
	s.restoreSession = nil
	target := str(session, "deviceId")
	if target == "" {
		target = s.target
	}
	if s.key() == target+"|"+str(session, "providerId")+"|"+str(session, "id") {
		s.markSessionRead(session)
		s.closeMobileSidebar()
		s.changed()
		return
	}
	if s.selected != nil && s.snapshots != nil && s.snapshots.storage != nil {
		s.prepareSession(session, target)
		return
	}
	s.openSession(session, target, nil, false)
}

func (s *State) openSession(session Object, target string, page Object, pageRead bool) {
	if target != s.target {
		s.selectDevice(target)
	}
	s.markSessionRead(session)
	s.persistDraft()
	s.rememberDraft()
	s.rememberSession()
	s.sessionGeneration++
	if s.watchCancel != nil {
		s.watchCancel()
	}
	s.selected = session
	s.saveLastSession()
	s.transcriptSyncing = false
	s.transcript = page
	s.openingRow = firstHistoryRow(page)
	if !pageRead {
		s.transcript = s.sessionCache[s.key()]
	}
	s.restoreOutgoing(s.transcript)
	s.reconcileOutgoing(time.Time{})
	s.rebuildMessages()
	s.approvals = nil
	s.models = nil
	s.model = str(session, "model")
	s.modelBase = s.model
	d := s.drafts[s.key()]
	s.draft.Set(d.Text)
	s.uploads = d.Uploads
	s.restoreDraftModel(d)
	s.loadDraft()
	s.timeline = nil
	s.closeMobileSidebar()
	s.changed()
	s.loadCachedSession(pageRead)
	s.startSessionWatch()
}
func (s *State) startSessionWatch() {
	if s.ctx.Err() != nil || s.selected == nil || !s.connected() {
		return
	}
	if s.watchCancel != nil {
		s.watchCancel()
	}
	s.resumeDraftUploads()
	s.transcriptSyncing = false
	s.syncVisible = false
	s.animateText = map[string]bool{}
	target, p, id, key := s.target, str(s.selected, "providerId"), str(s.selected, "id"), s.key()
	if s.streamKey != key {
		resume := object(s.transcript["resume"])
		s.streamKey, s.streamEpoch, s.streamCursor = key, str(resume, "epoch"), number(resume, "cursor")
	}
	epoch, cursor := s.streamEpoch, s.streamCursor
	if s.transcript == nil {
		// A replay cursor cannot replace a missing history baseline.
		epoch, cursor = "", 0
	}
	ctx, cancel := context.WithCancel(s.ctx)
	s.watchCancel = cancel
	go func() {
		v, err := rpc(ctx, s.api, target, p, id, "models", nil)
		s.cx.Post(func() {
			if ctx.Err() != nil || key != s.key() {
				return
			}
			s.setConnectionIssue(target, "models", err)
			if err == nil {
				s.models = objects(v)
			}
			s.changed()
		})
	}()
	w := &sessionStream{s: s, ctx: ctx, target: target, provider: p, session: id, key: key, epoch: epoch, cursor: cursor, baseline: epoch != "", approvals: make(chan struct{}, 1)}
	if previous := s.stream; previous != nil && previous.key == key {
		w.verified = previous.verified
	}
	s.stream = w
	if epoch == "" || !w.verified {
		// Do not gate the first readable messages on IPC/live subscription setup.
		// A persisted cursor proves cache consistency, not current freshness.
		w.startSnapshot()
	}
	go w.run(epoch, cursor)
}
func (s *State) setTranscript(t Object) {
	s.transcriptLoading = false
	s.restoreOutgoing(t)
	s.transcript = persistence.Align(s.transcript, mergeLatest(s.transcript, t))
	summary := copyObject(object(s.transcript["session"]))
	summary["recencyAt"] = transcriptRecency(s.transcript)
	s.transcript["session"] = summary
	s.updateSelectedSummary()
	s.reconcileOutgoing(time.Time{})
	s.rebuildMessages()
	s.syncModel(str(object(t["session"]), "model"))
	s.scheduleSnapshot()
	s.changed()
}
func (s *State) older() { s.loadOlder(s.ctx, false, func() bool { return true }) }

func (s *State) loadOlder(owner context.Context, fill bool, current func() bool) {
	if s.olderPage(owner, fill, current) {
		return
	}
	cursor := str(s.transcript, "nextCursor")
	if cursor == "" {
		return
	}
	key, target, p, id := s.key(), s.target, str(s.selected, "providerId"), str(s.selected, "id")
	connected := s.connected()
	s.runScopedAction(owner, "history:"+key, false, func(ctx context.Context) (any, error) {
		v, err := s.api.Call(ctx, "client.cached", nil, target, Object{"method": "transcript", "provider": p, "session": id, "cursor": cursor})
		if err == nil && v != nil {
			return v, nil
		}
		if !connected {
			return nil, fmt.Errorf("更早的消息尚未缓存，请连接设备后加载")
		}
		options := Object{"cursor": cursor}
		if target != control.LocalTarget {
			options["historyItems"] = persistence.PageSize
		}
		return rpc(ctx, s.api, target, p, id, "transcript", options)
	}, func(v any) {
		if key != s.key() || str(s.transcript, "nextCursor") != cursor {
			return
		}
		page := object(v)
		seen := map[string]bool{}
		current := objects(s.transcript["turns"])
		for _, t := range current {
			seen[str(t, "id")] = true
		}
		older := []Object{}
		for _, t := range objects(page["turns"]) {
			if len(current) > 0 && str(t, "id") == str(current[0], "id") {
				// The older page can end in the same turn as the mounted page.
				// Keep the current version of overlapping records.
				items := objects(current[0]["items"])
				existing := map[string]bool{}
				for _, v := range items {
					existing[str(v, "id")] = true
				}
				var prefix []Object
				for _, v := range objects(t["items"]) {
					if !existing[str(v, "id")] {
						prefix = append(prefix, v)
					}
				}
				current = append([]Object{}, current...)
				current[0] = copyObject(current[0])
				current[0]["items"] = append(prefix, items...)
			}
			if !seen[str(t, "id")] {
				seen[str(t, "id")] = true
				older = append(older, t)
			}
		}
		s.transcript = copyObject(s.transcript)
		s.transcript["turns"] = append(older, current...)
		s.transcript["nextCursor"] = page["nextCursor"]
		s.rebuildMessages()
		s.scheduleSnapshot()
	}, func() bool { return key == s.key() && current() })
}
func (s *State) pendingSend() string {
	s.read()
	return s.pendingSends[s.key()]
}
func (s *State) canSubmitMessage() bool {
	s.read()
	if s.selected == nil || !s.connected() || !s.draftReady() || s.pendingSend() != "" {
		return false
	}
	if activeTurn(s.transcript) != nil && !s.agentCapability("steer") {
		return false
	}
	return true
}
func (s *State) canSend() bool {
	if !s.canSubmitMessage() {
		return false
	}
	for _, u := range s.uploads {
		if u.Status != "ready" {
			return false
		}
	}
	return strings.TrimSpace(s.draft.Get()) != "" || len(s.uploads) > 0
}
func (s *State) send(method string) {
	if s.selected == nil || !s.connected() || !s.draftReady() || s.pendingSend() != "" {
		return
	}
	if method == "interrupt" && !s.agentCapability("interrupt") {
		return
	}
	if method != "interrupt" && !s.canSend() {
		return
	}
	body := strings.TrimSpace(s.draft.Get())
	sent := append([]*Upload{}, s.uploads...)
	ids := []string{}
	for _, u := range sent {
		if u.Status == "ready" {
			ids = append(ids, u.ID)
		}
	}
	extra := Object{"turn": str(activeTurn(s.transcript), "id")}
	if method != "interrupt" {
		extra["text"], extra["attachments"] = body, ids
		if s.model != "" {
			extra["model"] = s.model
		}
	}
	var outgoing *outgoingMessage
	if method != "interrupt" {
		outgoing = s.queueOutgoing(body, sent)
	}
	s.submitMessage(method, extra, outgoing)
}

// Initial sends and explicit retries share completion, session isolation and
// delivery tracking. Retrying never consumes the current composer's draft.
func (s *State) submitMessage(method string, extra Object, outgoing *outgoingMessage) {
	key, target, p, id := s.key(), s.target, str(s.selected, "providerId"), str(s.selected, "id")
	if s.pendingSends == nil {
		s.pendingSends = map[string]string{}
	}
	s.pendingSends[key] = method
	s.error = ""
	s.changed()
	failure := &sessionOperationError{target: target, device: str(s.device(), "name"), provider: p, session: id, source: str(s.selected, "sourceKind"), method: method}
	go func() {
		_, err := rpc(s.ctx, s.api, target, p, id, method, extra)
		s.cx.Post(func() {
			if s.ctx.Err() != nil {
				return
			}
			delete(s.pendingSends, key)
			confirmed := outgoing != nil && outgoing.Status == "confirmed"
			if err != nil && !confirmed {
				if s.key() == key {
					failure.cause = err
					s.error, s.failure = err.Error(), failure
				}
			}
			if outgoing != nil {
				outgoing.FinishedAt = time.Now()
				if !confirmed {
					outgoing.Status = "accepted"
				}
				if err != nil && !confirmed {
					outgoing.Status, outgoing.Error = "failed", err.Error()
					if control.OutcomeUnknown(err) {
						outgoing.Status = "uncertain"
					}
				}
				if s.key() == key {
					s.rebuildMessages()
					if s.transcript == nil {
						s.persistSending()
					} else {
						s.persistSnapshot()
					}
				} else if s.snapshots != nil && s.snapshots.storage != nil {
					s.writeSnapshot(target, p, id, Object{"_deliveryOnly": true, "localMessages": s.outgoing[key]})
				} else if cached := s.sessionCache[key]; cached != nil {
					cached["localMessages"] = s.outgoing[key]
					s.writeSnapshot(target, p, id, cached)
				}
				if (err == nil || control.OutcomeUnknown(err)) && s.key() == key && s.connected() {
					s.refreshAfterSend(key, target, p, id, control.OutcomeUnknown(err))
				}
			}
			s.changed()
		})
	}()
}
func (s *State) decision(a Object, decision string) {
	s.approvalAction(a, Object{"decision": decision})
}
func (s *State) autoReconnect() {
	if s.ctx.Err() != nil || s.directoryChecking {
		return
	}
	for _, d := range s.devices {
		id := str(d, "id")
		h := s.connection(id)
		if flag(d, "connected") && h.attempt == nil {
			h.established = true
		}
		if flag(d, "connected") || h.reconnecting || s.deviceOffline(id) {
			continue
		}
		if h.waitingOnline {
			if online, known := s.deviceOnline(d); known && online {
				// A click on an offline device queues one initial attempt, not
				// an infinite retry loop after a failed first connection.
				s.connectDevice(id, h.established, !flag(d, "autoReconnect"))
			}
			continue
		}
		if flag(d, "autoReconnect") && (h.established || h.startupResumed || h.attempt == nil) && time.Now().After(s.retrying[id]) {
			if h.attempt == nil {
				h.startupResumed = true
			}
			s.reconnect(id, true)
		}
	}
}
func (s *State) promptHost() {
	if s.hostAuthorization.GetUntracked() != "" {
		return
	}
	for _, p := range objects(s.host["pending"]) {
		id := str(p, "id")
		if id != "" && !s.prompted[id] {
			s.prompted[id] = true
			s.hostAuthorization.Set(id)
			return
		}
	}
}
func (s *State) copy(value string) {
	if value == "" {
		return
	}
	s.cx.SetClipboard(value)
	if s.copyTimer != nil {
		s.copyTimer()
		s.copyTimer = nil
	}
	s.copiedValue = value
	s.copyVersion++
	version := s.copyVersion
	s.changed()
	// Bind the timer to the workspace, not the button's event scope: the
	// confirmation replaces that button as soon as its label/icon changes.
	s.cx.Post(func() {
		if s.ctx.Err() != nil || s.copyVersion != version {
			return
		}
		s.copyTimer = s.cx.AfterFunc(2*time.Second, func() {
			s.copyTimer = nil
			s.copiedValue = ""
			s.changed()
		})
	})
}
func (s *State) toggleTheme() {
	s.setAppearance(choose(s.cx.Dark(), "light", "dark"))
}

func pairingCode(v Object) string { b, _ := json.Marshal(v); return "agenthub2:" + base64URL(b) }

func (s *State) agentCapabilities() Object {
	s.read()
	id := str(s.selected, "providerId")
	for _, p := range s.providers {
		if str(p, "id") == id {
			return object(p["capabilities"])
		}
	}
	return nil
}
func (s *State) agentCapability(name string) bool { return flag(s.agentCapabilities(), name) }
func (s *State) answerApproval(a Object, value string) {
	s.approvalAction(a, Object{"decision": "input", "answers": Object{"value": Object{"answers": []string{value}}}})
}
