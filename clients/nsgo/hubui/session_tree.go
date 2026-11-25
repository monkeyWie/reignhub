package hubui

import (
	"encoding/json"
	"github.com/monkeyWie/reignhub/clients/nsgo/hubui/locales"
	"reflect"
	"sort"
	"strings"
	"time"

	"github.com/monkeyWie/reignhub/internal/control"
	"github.com/monkeyWie/reignhub/internal/provider"
)

const (
	groupProject = "project"
	groupDevice  = "device"
	groupAgent   = "device-agent"
)

type sidebarPreferences struct {
	Hidden            bool             `json:"hidden,omitempty"`
	Unread            map[string]bool  `json:"unread,omitempty"`
	Grouping          string           `json:"grouping"`
	Pinned            map[string]int64 `json:"pinned"`
	Collapsed         map[string]bool  `json:"collapsed"`
	Removed           map[string]bool  `json:"removed,omitempty"`
	UncertainRemovals map[string]bool  `json:"uncertainRemovals,omitempty"`
}

func (s *State) loadSidebarPreferences() {
	s.grouping = groupProject
	s.pinned = map[string]int64{}
	s.unreadSessions = map[string]bool{}
	s.sessionActivity = map[string]string{}
	var p sidebarPreferences
	if raw, ok := s.cx.Storage().GetItem("session-sidebar"); ok && json.Unmarshal([]byte(raw), &p) == nil {
		s.sidebarHidden.Set(p.Hidden)
		if p.Unread != nil {
			s.unreadSessions = p.Unread
		}
		if p.Grouping == groupProject || p.Grouping == groupDevice || p.Grouping == groupAgent {
			s.grouping = p.Grouping
		}
		if p.Pinned != nil {
			s.pinned = p.Pinned
		}
		s.removedSessions = p.Removed
		s.uncertainRemovals = p.UncertainRemovals
		if p.Collapsed != nil {
			s.collapsed = p.Collapsed
		}
	}
	// Pinned and Recent start expanded on each launch; manual collapse lasts for
	// the current controller lifetime.
	delete(s.collapsed, "section:pinned")
	delete(s.collapsed, "section:recent")
}
func (s *State) saveSidebarPreferences() {
	raw, err := json.Marshal(sidebarPreferences{Hidden: s.sidebarHidden.GetUntracked(), Unread: s.unreadSessions, Grouping: s.grouping, Pinned: s.pinned, Collapsed: s.collapsed, Removed: s.removedSessions, UncertainRemovals: s.uncertainRemovals})
	if err == nil {
		err = s.cx.Storage().SetItem("session-sidebar", string(raw))
	}
	if err != nil {
		s.error = err.Error()
	}
	s.catalogueVersion++
	s.changed()
}
func (s *State) setGrouping(value string) {
	if value != groupProject && value != groupDevice && value != groupAgent {
		return
	}
	s.grouping = value
	s.saveSidebarPreferences()
}
func sessionIdentity(session Object) string {
	b, _ := json.Marshal([]string{str(session, "deviceId"), str(session, "providerId"), str(session, "id")})
	return string(b)
}
func (s *State) togglePinned(session Object) {
	key := sessionIdentity(session)
	if s.pinned == nil {
		s.pinned = map[string]int64{}
	}
	if s.pinned[key] != 0 {
		delete(s.pinned, key)
	} else {
		s.pinned[key] = time.Now().UnixMilli()
		delete(s.collapsed, "section:pinned")
	}
	s.saveSidebarPreferences()
}
func sessionCreated(session Object) int64 {
	return provider.TimestampMillis(number(session, "createdAt"))
}
func sessionUpdated(session Object) int64 {
	return provider.TimestampMillis(number(session, "updatedAt"))
}

func sessionRecency(session Object) int64 {
	if recent := number(session, "recencyAt"); recent > 0 {
		return recent
	}
	return sessionCreated(session)
}

// Deduplication is confined to an environment and known adapter family. A
// remote conversation with the same vendor ID is still a different session.
func uniqueSessions(sessions []Object) []Object {
	unique := map[string]Object{}
	for _, session := range sessions {
		if !visibleSession(session) {
			continue
		}
		b, _ := json.Marshal([]string{str(session, "deviceId"), agentFamily(str(session, "providerId")), str(session, "id")})
		key := string(b)
		old := unique[key]
		if old == nil || sessionUpdated(session) > sessionUpdated(old) {
			unique[key] = session
		}
	}
	result := make([]Object, 0, len(unique))
	for _, session := range unique {
		result = append(result, session)
	}
	sort.Slice(result, func(i, j int) bool {
		if a, b := sessionCreated(result[i]), sessionCreated(result[j]); a != b {
			return a > b
		}
		return sessionIdentity(result[i]) < sessionIdentity(result[j])
	})
	return result
}
func sessionRunning(session Object) bool {
	switch strings.ToLower(str(session, "status")) {
	case "active", "inprogress", "running", "waiting", "waitingforinput", "waitingforapproval", "needsinput":
		return true
	case "completed", "failed", "interrupted", "idle", "notloaded":
		return false
	}
	return str(session, "activeTurnId") != ""
}
func (s *State) environmentName(target string) string {
	if target == control.LocalTarget {
		return locales.For(s.cx).LocalDevice()
	}
	for _, d := range s.devices {
		if str(d, "id") == target {
			return deviceName(d)
		}
	}
	return "未命名设备"
}
func (s *State) providerName(target, id string) string {
	if id == "codex-app" {
		return "Codex"
	}
	for _, p := range s.workspaces[target].providers {
		if str(p, "id") == id && str(p, "name") != "" {
			return str(p, "name")
		}
	}
	for _, p := range s.providers {
		if str(p, "id") == id && str(p, "name") != "" {
			return str(p, "name")
		}
	}
	return id
}
func (s *State) sessionActive(session Object) bool {
	if !sessionRunning(session) {
		return false
	}
	target := str(session, "deviceId")
	if target == "" {
		target = s.target
	}
	if !s.connectionReady(target) {
		return false
	}
	return true
}

type sessionTreeNode struct {
	Key, Title, Kind, Path, Target, Provider, Project string
	Sessions                                          []Object
	Children                                          []*sessionTreeNode
	childrenByKey                                     map[string]*sessionTreeNode
	Created                                           int64
	Count                                             int
	named                                             bool
	Unread                                            bool
	Active                                            int
	Repositories                                      []string
	Directories                                       []groupDirectory
}

func (s *State) sessionTree(sessions []Object) []*sessionTreeNode {
	root := &sessionTreeNode{}
	for _, ss := range sessions {
		target, p := str(ss, "deviceId"), str(ss, "providerId")
		dir := projectDirectory(ss)
		project := sessionProjectKey(ss)
		levels := []sessionTreeNode{}
		if s.grouping == groupDevice || s.grouping == groupAgent {
			levels = append(levels, sessionTreeNode{Key: target, Title: s.environmentName(target), Kind: "device", Target: target})
		}
		if s.grouping == groupAgent {
			levels = append(levels, sessionTreeNode{Key: p, Title: s.providerName(target, p), Kind: "agent", Target: target, Provider: p})
		}
		levels = append(levels, sessionTreeNode{Key: project, Project: project, Title: dir.Title, Kind: "project", Path: dir.Path, Target: target, Provider: p})
		parent := root
		for _, level := range levels {
			encoded, _ := json.Marshal([]string{parent.Key, level.Kind, level.Key})
			level.Key = string(encoded)
			if parent.childrenByKey == nil {
				parent.childrenByKey = map[string]*sessionTreeNode{}
			}
			found := parent.childrenByKey[level.Key]
			if found == nil {
				copy := level
				found = &copy
				parent.Children = append(parent.Children, found)
				parent.childrenByKey[level.Key] = found
			}
			if level.Kind == "project" && str(object(ss["project"]), "name") != "" && !found.named {
				found.Title = dir.Title
				found.named = true
			}
			found.Count++
			if s.sessionActive(ss) {
				found.Active++
			}
			if level.Kind == "project" {
				found.Repositories, found.Directories = appendProjectDetails(found.Repositories, found.Directories, ss)
			}
			found.Unread = found.Unread || s.sessionUnread(ss)
			found.Created = max(found.Created, sessionCreated(ss))
			if found.Provider != p {
				found.Provider = ""
			}
			if found.Target != target {
				found.Target = ""
				found.Provider = ""
				found.Path = ""
			}
			parent = found
		}
		parent.Sessions = append(parent.Sessions, ss)
	}
	var order func(*sessionTreeNode)
	order = func(node *sessionTreeNode) {
		node.childrenByKey = nil
		sort.Slice(node.Children, func(i, j int) bool {
			a, b := node.Children[i], node.Children[j]
			if a.Created != b.Created {
				return a.Created > b.Created
			}
			if a.Title != b.Title {
				return a.Title < b.Title
			}
			return a.Key < b.Key
		})
		for _, child := range node.Children {
			order(child)
		}
	}
	order(root)
	return root.Children
}

type directoryRow struct {
	ID, Title, Path, Group, Kind, Section, Target, Provider, Project string
	Session                                                          Object
	Header, More                                                     bool
	Unread                                                           bool
	Depth, Count                                                     int
	Active                                                           int
	Repositories                                                     []string
	Directories                                                      []groupDirectory
}

func (s *State) directoryRows() []directoryRow {
	s.read()
	sessions := s.sessionsMatching(sessionFilters{})
	// A completely empty directory has one shared onboarding/loading/filter
	// state. Section placeholders only explain empty subsets of existing data.
	if len(sessions) == 0 {
		return nil
	}
	rows := []directoryRow{}
	pinned, projects, recent := []Object{}, []Object{}, []Object{}
	hasPins := false
	for _, ss := range sessions {
		hasPins = hasPins || s.pinned[sessionIdentity(ss)] != 0
	}
	for _, ss := range s.sessionsMatching(s.pinnedFilters) {
		if s.pinned[sessionIdentity(ss)] != 0 {
			pinned = append(pinned, ss)
		}
	}
	for _, ss := range s.filteredSessions() {
		if s.pinned[sessionIdentity(ss)] == 0 {
			projects = append(projects, ss)
		}
	}
	for _, ss := range s.sessionsMatching(s.recentFilters) {
		if s.pinned[sessionIdentity(ss)] == 0 {
			recent = append(recent, ss)
		}
	}
	sort.Slice(pinned, func(i, j int) bool {
		a, b := s.pinned[sessionIdentity(pinned[i])], s.pinned[sessionIdentity(pinned[j])]
		if a != b {
			return a > b
		}
		return sessionIdentity(pinned[i]) < sessionIdentity(pinned[j])
	})
	sort.Slice(recent, func(i, j int) bool {
		a, b := sessionRecency(recent[i]), sessionRecency(recent[j])
		if a != b {
			return a > b
		}
		return sessionIdentity(recent[i]) < sessionIdentity(recent[j])
	})
	addSession := func(section string, ss Object, depth int) {
		rows = append(rows, directoryRow{ID: section + ":" + sessionIdentity(ss), Title: str(ss, "title"), Path: str(ss, "cwd"), Session: ss, Section: section, Depth: depth})
	}
	if hasPins {
		rows = append(rows, directoryRow{ID: "section:pinned", Title: locales.For(s.cx).PinnedSessions(), Kind: "section", Section: "pinned", Count: len(pinned), Header: true, Unread: s.anySessionUnread(pinned)})
		if !s.collapsed["section:pinned"] {
			if s.pinnedFilters.count() > 0 {
				rows = append(rows, directoryRow{ID: "filters:pinned", Kind: "filters", Section: "pinned", Header: true})
			}
			for _, ss := range pinned {
				addSession("pinned", ss, 0)
			}
		}
	}
	rows = append(rows, directoryRow{ID: "section:all", Title: locales.For(s.cx).Projects(), Kind: "section", Section: "all", Count: len(projects), Header: true, Unread: s.anySessionUnread(projects)})
	var walk func([]*sessionTreeNode, int)
	walk = func(nodes []*sessionTreeNode, depth int) {
		for _, node := range nodes {
			rows = append(rows, directoryRow{ID: "tree:" + node.Key, Group: node.Key, Title: node.Title, Path: node.Path, Target: node.Target, Provider: node.Provider, Project: node.Project, Kind: node.Kind, Count: node.Count, Depth: depth, Header: true, Unread: node.Unread, Active: node.Active, Repositories: node.Repositories, Directories: node.Directories})
			if s.collapsed[node.Key] {
				continue
			}
			walk(node.Children, depth+1)
			for _, ss := range node.Sessions {
				addSession("all", ss, depth+1)
			}
		}
	}
	if !s.collapsed["section:all"] {
		if s.filters.count() > 0 {
			rows = append(rows, directoryRow{ID: "filters:all", Kind: "filters", Section: "all", Header: true})
		}
		if len(projects) == 0 && s.filters.count() == 0 {
			title := locales.For(s.cx).NoProjectSessions()
			if hasPins {
				title = locales.For(s.cx).AllSessionsPinned()
			}
			rows = append(rows, directoryRow{ID: "empty:projects", Kind: "empty", Section: "all", Header: true, Title: title})
		}
		walk(s.sessionTree(projects), 0)
	}
	rows = append(rows, directoryRow{ID: "section:recent", Title: locales.For(s.cx).RecentSessions(), Kind: "section", Section: "recent", Count: len(recent), Header: true, Unread: s.anySessionUnread(recent)})
	if !s.collapsed["section:recent"] {
		if s.recentFilters.count() > 0 {
			rows = append(rows, directoryRow{ID: "filters:recent", Kind: "filters", Section: "recent", Header: true})
		}
		if len(recent) == 0 && s.recentFilters.count() == 0 {
			title := locales.For(s.cx).NoRecentSessions()
			if hasPins {
				title = locales.For(s.cx).AllSessionsPinned()
			}
			rows = append(rows, directoryRow{ID: "empty:recent", Kind: "empty", Section: "recent", Header: true, Title: title})
		}
		for _, ss := range recent {
			addSession("recent", ss, 0)
		}
	}
	return rows
}

func sessionWaiting(session Object) bool {
	switch strings.ToLower(str(session, "status")) {
	case "waiting", "waitingforinput", "waitingforapproval", "needsinput":
		return true
	}
	return false
}

// Live selected-session events update the shared summary without changing its
// creation time. Background directories still own all other conversations.
func (s *State) updateSelectedSummary() {
	if s.selected == nil {
		return
	}
	w := s.workspace(s.target)
	for i, row := range w.sessions {
		if str(row, "providerId") != str(s.selected, "providerId") || str(row, "id") != str(s.selected, "id") {
			continue
		}
		next := copyObject(row)
		summary := object(s.transcript["session"])
		if str(summary, "id") == str(row, "id") && str(summary, "providerId") == str(row, "providerId") {
			for k, v := range summary {
				if k == "title" && (strings.TrimSpace(str(row, "title")) != "" || strings.TrimSpace(str(summary, "title")) == "") {
					continue
				}
				// Cached history can arrive after current owner discovery.
				if k != "deviceId" && k != "codexOwnerUnreachable" {
					next[k] = v
				}
			}
		}
		next["recencyAt"] = max(number(row, "recencyAt"), number(next, "recencyAt"))
		turns := objects(s.transcript["turns"])
		if len(turns) > 0 {
			status := str(turns[len(turns)-1], "status")
			if status == "inProgress" {
				next["status"] = "running"
				if sessionWaiting(summary) || len(s.approvals) > 0 {
					next["status"] = "waiting"
				}
				next["activeTurnId"] = str(turns[len(turns)-1], "id")
			} else if status == "completed" || status == "failed" || status == "interrupted" {
				next["status"] = status
				next["activeTurnId"] = ""
			}
		}
		if !reflect.DeepEqual(next, row) {
			s.observeSessionActivity(s.target, []Object{next})
			w.sessions = append([]Object(nil), w.sessions...)
			w.sessions[i] = next
			s.workspaces[s.target] = w
			s.publishWorkspaces()
		}
		return
	}
}

func decodeSessionIdentity(key string) []string {
	var identity []string
	_ = json.Unmarshal([]byte(key), &identity)
	return identity
}
