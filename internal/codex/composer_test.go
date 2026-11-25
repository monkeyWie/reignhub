package codex

import (
	"context"
	"database/sql"
	"encoding/json"
	"os"
	"path/filepath"
	"slices"
	"testing"
	"time"

	"github.com/monkeyWie/reignhub/internal/codexipc"
	"github.com/monkeyWie/reignhub/internal/config"
	"github.com/monkeyWie/reignhub/internal/events"
	"github.com/monkeyWie/reignhub/internal/provider"
	"github.com/monkeyWie/reignhub/internal/rpc"
)

func TestComposerPermissionFromHostSettings(t *testing.T) {
	for _, tc := range []struct{ raw, id, label string }{
		{`{"activePermissionProfile":{"id":":workspace"},"approvalPolicy":"on-request","approvalsReviewer":"guardian_subagent"}`, "auto", "帮我批准"},
		{`{"activePermissionProfile":{"id":"team-review"},"sandboxPolicy":{"type":"readOnly"}}`, "", "team-review"},
		{`{"sandbox":{"type":"readOnly","networkAccess":false},"approvalPolicy":"on-request"}`, "read-only", "只读"},
		{`{"sandboxPolicy":{"type":"workspaceWrite","networkAccess":false},"approvalPolicy":"on-request"}`, "ask", "请求批准"},
		{`{"sandboxPolicy":{"type":"workspaceWrite","networkAccess":true},"approvalPolicy":"on-request"}`, "", "自定义权限"},
		{`{"sandbox":{"type":"externalSandbox","networkAccess":"enabled"},"approvalPolicy":"never"}`, "", "自定义权限"},
		{`{"sandboxPolicy":{"type":"dangerFullAccess"},"approvalPolicy":"on-request"}`, "", "自定义权限"},
		{`{"sandboxPolicy":{"type":"workspaceWrite"}}`, "", ""},
		{`{}`, "", ""},
	} {
		var s codexSettings
		if err := json.Unmarshal([]byte(tc.raw), &s); err != nil {
			t.Fatal(err)
		}
		if id, label := s.permission(); id != tc.id || label != tc.label {
			t.Errorf("%s gave %q %q, want %q %q", tc.raw, id, label, tc.id, tc.label)
		}
	}
}

func TestBundledComposerSettings(t *testing.T) {
	cli := os.Getenv("REIGNHUB_TEST_SHARED_CODEX_CLI")
	if cli == "" {
		t.Skip("requires bundled Codex CLI")
	}
	dir := t.TempDir()
	t.Setenv("CODEX_HOME", dir)
	t.Setenv("OPENAI_API_KEY", "")
	t.Setenv("CODEX_API_KEY", "")
	if err := os.WriteFile(filepath.Join(dir, "config.toml"), []byte("sandbox_mode = 'read-only'\napproval_policy = 'on-request'\n"), 0600); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	p := New(config.Config{}, "", events.New())
	observe, publish := p.runtimeObserver()
	server, err := startSharedServer(ctx, cli, observe)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(server.close)
	p.mu.Lock()
	p.client = server.client
	p.mu.Unlock()
	publish(server.client)
	session, err := p.Create(ctx, provider.CreateOptions{CWD: dir})
	if err != nil {
		t.Fatal(err)
	}
	empty, err := p.Transcript(ctx, session.ID, "")
	if err != nil || empty.Session.ID != session.ID || len(empty.Turns) != 0 {
		t.Fatalf("new thread before first message: %v", err)
	}
	controls, err := p.ComposerControls(ctx, session.ID, "")
	if err != nil {
		t.Fatal(err)
	}
	if !slices.ContainsFunc(controls, func(c provider.ComposerControl) bool {
		return c.ID == "permission" && c.Selected == "read-only" && c.SelectedLabel == "只读"
	}) {
		t.Fatalf("first read did not reflect host permissions: %+v", controls)
	}
	// Simulate a desktop choice independent of the CLI's read-only config.
	state := map[string]any{"electron-renderer-content-owner": "active", "electron-owner-atoms:active": map[string]any{
		desktopPermissionKey: map[string]string{"kind": "agent-mode", "agentMode": "full-access"},
	}}
	if err := os.WriteFile(filepath.Join(dir, ".codex-global-state.json"), rawJSON(state), 0600); err != nil {
		t.Fatal(err)
	}
	remote, err := p.Create(ctx, provider.CreateOptions{CWD: dir})
	if err != nil {
		t.Fatal(err)
	}
	controls, err = p.ComposerControls(ctx, remote.ID, "")
	if err != nil || !slices.ContainsFunc(controls, func(c provider.ComposerControl) bool {
		return c.ID == "permission" && c.Selected == "full"
	}) {
		t.Fatalf("desktop full access was not effective on the new thread: %+v %v", controls, err)
	}
	var effort, model string
	p.mu.Lock()
	model = p.composerSettings[session.ID].Model
	p.mu.Unlock()
	for _, c := range controls {
		if c.ID == "effort" && len(c.Choices) > 1 {
			effort = c.Choices[len(c.Choices)-1].ID
		}
	}
	if model == "" || effort == "" {
		t.Fatalf("missing model or reasoning levels: %+v", controls)
	}
	for _, change := range []provider.ComposerChange{
		{Control: "permission", Value: "auto"}, {Control: "permission", Value: "full"},
		{Control: "permission", Value: "ask"}, {Control: "effort", Value: effort, Model: model},
	} {
		if err := p.ConfigureComposer(ctx, session.ID, change); err != nil {
			t.Fatal(change, err)
		}
		for {
			rows, err := p.ComposerControls(ctx, session.ID, model)
			if err != nil {
				t.Fatal(err)
			}
			found := false
			for _, r := range rows {
				if r.ID == change.Control && r.Selected == change.Value {
					found = true
					break
				}
			}
			if found {
				break
			}
			select {
			case <-ctx.Done():
				t.Fatalf("agent never reported %+v in %+v", change, rows)
			case <-time.After(20 * time.Millisecond):
			}
		}
	}
}

func TestIPCComposerValidatesRestrictedChoicesAtOwner(t *testing.T) {
	p := New(config.Config{}, "", events.New())
	backendFixture(t, p, func(m rpc.Message) (any, *rpc.Error) {
		switch m.Method {
		case "model/list":
			return map[string]any{"data": []map[string]any{{"model": "native", "supportedReasoningEfforts": []map[string]string{{"reasoningEffort": "low"}}}}}, nil
		case "permissionProfile/list":
			return map[string]any{"data": []map[string]any{{"id": ":workspace", "allowed": true}, {"id": ":danger-full-access", "allowed": true}}}, nil
		case "configRequirements/read":
			return map[string]any{"requirements": map[string]any{"allowedApprovalPolicies": []string{"on-request"}, "allowedApprovalsReviewers": []string{"user"}}}, nil
		case "thread/queue/list":
			return nil, &rpc.Error{Code: -32601, Message: "Unsupported method"}
		default:
			t.Errorf("unexpected private method %s", m.Method)
			return nil, nil
		}
	})
	writes := make(chan codexipc.Message, 2)
	var fixture *ipcFixture
	fixture = attachIPCFixture(t, p, func(m codexipc.Message) (codexipc.Message, bool) {
		switch m.Method {
		case "thread-stream-following-changed":
			go fixture.snapshot("external", 1, "fixture")
		case "thread-follower-update-thread-settings":
			writes <- m
			return codexipc.Message{HandledBy: "app-owner", ResultType: "success", Result: rawJSON(map[string]any{"method": m.Method, "result": map[string]bool{"applied": true}})}, true
		}
		return codexipc.Message{}, false
	})
	ctx, cancel := context.WithTimeout(context.Background(), 4*time.Second)
	defer cancel()
	if _, err := p.Probe(ctx, "external", "", 0); err != nil {
		t.Fatal(err)
	}
	epoch, cursor := p.stream().log.Position()
	p.appBridge().Broadcast(codexipc.Message{Method: "thread-stream-state-changed", Source: "app-owner", Version: 11, Params: rawJSON(map[string]any{
		"hostId": "local", "conversationId": "external", "change": map[string]any{"type": "patches", "baseRevision": 1, "revision": 2,
			"patches": []any{map[string]any{"op": "add", "path": []string{"latestThreadSettings"}, "value": map[string]any{
				"sandboxPolicy": map[string]any{"type": "dangerFullAccess"}, "approvalPolicy": "never",
			}}},
		},
	})})
	batch, err := p.stream().log.Probe(ctx, "external", epoch, cursor)
	if err != nil || !slices.ContainsFunc(batch.Events, func(e provider.StreamEvent) bool { return e.Kind == "composerSettings" }) {
		t.Fatal("missing owner permission refresh", batch, err)
	}
	controls, err := p.ComposerControls(ctx, "external", "")
	if err != nil || !slices.ContainsFunc(controls, func(c provider.ComposerControl) bool {
		return c.ID == "permission" && c.Selected == "full" && c.SelectedLabel == "完全访问" && len(c.Choices) == 1 && c.Choices[0].ID == "ask"
	}) {
		t.Fatal("active permission missing from restricted host", controls, err)
	}
	for _, change := range []provider.ComposerChange{{Control: "permission", Value: "full"}, {Control: "permission", Value: "auto"}, {Control: "effort", Model: "native", Value: "high"}} {
		if err := p.ConfigureComposer(ctx, "external", change); err == nil {
			t.Fatal("unavailable choice accepted", change)
		}
	}
	if err := p.ConfigureComposer(ctx, "external", provider.ComposerChange{Control: "permission", Value: "ask"}); err != nil {
		t.Fatal(err)
	}
	m := <-writes
	settings := object(decodeObject(m.Params)["threadSettings"])
	if len(writes) != 0 || m.Version != 2 || stringValue(settings["permissions"]) != ":workspace" || stringValue(settings["approvalsReviewer"]) != "user" || stringValue(settings["approvalPolicy"]) != "on-request" {
		t.Fatal("incorrect IPC settings", settings)
	}
}

func TestFirstIPCPermissionSnapshotRefreshesComposer(t *testing.T) {
	p := New(config.Config{}, "", events.New())
	state := ipcProjectionFixture(0, 0)
	state["currentPermissions"] = map[string]any{"activePermissionProfile": map[string]string{"id": "team-review"}}
	epoch, cursor := p.stream().log.Position()
	observeFixtureState(t, p, nil, rawJSON(state))
	batch, err := p.stream().log.Probe(context.Background(), "fixture", epoch, cursor)
	if err != nil || !slices.ContainsFunc(batch.Events, func(e provider.StreamEvent) bool { return e.Kind == "composerSettings" }) {
		t.Fatal("first owner snapshot did not refresh permissions", batch, err)
	}
}

func TestDesktopPermissionUsesActiveSQLiteAccount(t *testing.T) {
	dir := t.TempDir()
	db, err := sql.Open("sqlite", filepath.Join(dir, ".codex-global-state.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	_, err = db.Exec(`CREATE TABLE settings (scope TEXT, key TEXT, value TEXT, PRIMARY KEY(scope,key)); PRAGMA user_version=1;
		INSERT INTO settings VALUES ('global','electron-renderer-content-owner','"active"'),
		('electron-owner-atoms:active','permission-selection-by-host-id:local','{"kind":"agent-mode","agentMode":"read-only"}'),
		('electron-owner-atoms:active','permission-selection-by-host-id:remote','{"kind":"agent-mode","agentMode":"full-access"}'),
		('atom','permission-selection-by-host-id:local','{"kind":"agent-mode","agentMode":"full-access"}')`)
	if err != nil {
		t.Fatal(err)
	}
	read := func() desktopPermission {
		t.Helper()
		got, err := readDesktopPermission(context.Background(), dir)
		if err != nil {
			t.Fatal(err)
		}
		return got
	}
	if got := read(); got.AgentMode != "read-only" {
		t.Fatal(got)
	}
	if _, err := db.Exec("DELETE FROM settings WHERE scope='electron-owner-atoms:active'"); err != nil {
		t.Fatal(err)
	}
	if got := read(); got != (desktopPermission{}) {
		t.Fatal("picked up a different owner's permission", got)
	}
}

func TestDesktopPermissionModeIsAnExplicitLocalOverride(t *testing.T) {
	dir := t.TempDir()
	for _, tc := range []struct{ mode, derived, want string }{
		{"full-access", "auto", "full-access"}, {"full-access", "full-access", ""}, {"read-only", "full-access", "read-only"},
	} {
		state := map[string]any{"electron-persisted-atom-state": map[string]any{
			"agent-mode-by-host-id": map[string]string{"local": tc.mode}, "config-derived-agent-mode-by-host-id": map[string]string{"local": tc.derived},
		}}
		if err := os.WriteFile(filepath.Join(dir, ".codex-global-state.json"), rawJSON(state), 0600); err != nil {
			t.Fatal(err)
		}
		got, err := readDesktopPermission(context.Background(), dir)
		if err != nil || got.AgentMode != tc.want {
			t.Fatal(got, err)
		}
	}
}

func TestIPCSendUsesCurrentOwnerPermissions(t *testing.T) {
	state := map[string]any{"currentPermissions": map[string]any{"approvalPolicy": "on-request", "sandboxPolicy": map[string]any{"type": "workspaceWrite"}},
		"latestThreadSettings": map[string]any{"approvalPolicy": "never", "approvalsReviewer": "auto_review", "sandboxPolicy": map[string]any{"type": "dangerFullAccess"}}}
	request := ipcTurnStart(state, "fixture", nil, "")
	if request["approvalPolicy"] != "on-request" || object(request["sandboxPolicy"])["type"] != "workspaceWrite" || request["approvalsReviewer"] != nil {
		t.Fatal("send mixed policies from different snapshots", request)
	}
}

func TestStoredThreadPermission(t *testing.T) {
	dir := t.TempDir()
	db, err := sql.Open("sqlite", filepath.Join(dir, "state_5.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	_, err = db.Exec(`CREATE TABLE threads(id TEXT PRIMARY KEY,sandbox_policy TEXT,approval_mode TEXT);
		INSERT INTO threads VALUES ('full','{"type":"disabled"}','never'),('managed','{"type":"managed"}','on-request'),('other','{"type":"unknown"}','never')`)
	if err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct{ thread, id, label string }{
		{"full", "full", "完全访问"}, {"managed", "", "受限访问（按需审批）"}, {"other", "", ""}, {"missing", "", ""},
	} {
		if id, label := storedThreadPermission(t.Context(), dir, tc.thread); id != tc.id || label != tc.label {
			t.Fatalf("%s: %s %s", tc.thread, id, label)
		}
	}
}
