package hubui

import (
	"encoding/json"
	"testing"
	"time"
)

func outgoingFixture() *State {
	return &State{target: "device", selected: Object{"providerId": "codex-app", "id": "chat"}, outgoing: map[string][]*outgoingMessage{}}
}
func outgoingTranscript(items ...Object) Object {
	return Object{"turns": []Object{{"id": "turn", "status": "completed", "items": items}}}
}
func TestOutgoingReconcilesNewEchoWithoutMatchingOldIdenticalText(t *testing.T) {
	s := outgoingFixture()
	a := &outgoingMessage{ID: "local-a", Text: "same", Status: "failed", Known: map[string]bool{"turn|old": true}}
	b := &outgoingMessage{ID: "local-b", Text: "same", Status: "sending", Known: map[string]bool{"turn|old": true}}
	s.outgoing[s.key()] = []*outgoingMessage{a, b}
	s.transcript = outgoingTranscript(Object{"id": "old", "role": "user", "text": "same"})
	s.reconcileOutgoing(time.Time{})
	if len(s.outgoing[s.key()]) != 2 {
		t.Fatal("matched an old identical message")
	}
	s.transcript = outgoingTranscript(Object{"id": "old", "role": "user", "text": "same"}, Object{"id": "new", "role": "user", "text": "same\n"})
	s.reconcileOutgoing(time.Time{})
	if len(s.outgoing[s.key()]) != 1 || s.outgoing[s.key()][0] != b {
		t.Fatal("echo was not merged exactly once")
	}
	s.reconcileOutgoing(time.Time{})
	if len(s.outgoing[s.key()]) != 1 {
		t.Fatal("repeated snapshot consumed a second pending message")
	}
	s.rebuildMessages()
	if len(s.messageRows) != 3 || s.messageRows[2].Delivery != "sending" {
		t.Fatal("pending overlay lost its status")
	}
}
func TestOnlyFreshSnapshotAfterRPCSettlesRemovesAbsentLocalMessage(t *testing.T) {
	s := outgoingFixture()
	now := time.Now()
	m := &outgoingMessage{ID: "local", Text: "missing", Status: "accepted", FinishedAt: now}
	s.outgoing[s.key()] = []*outgoingMessage{m}
	s.transcript = outgoingTranscript()
	for _, started := range []time.Time{{}, now.Add(-time.Second)} {
		s.reconcileOutgoing(started)
		if len(s.outgoing[s.key()]) != 1 {
			t.Fatal("old/cache snapshot removed accepted message")
		}
	}
	s.reconcileOutgoing(now.Add(time.Second))
	if len(s.outgoing[s.key()]) != 1 {
		t.Fatal("fresh history removed an accepted send before its echo")
	}
	s.transcript = outgoingTranscript(Object{"id": "echo", "role": "user", "text": "missing"})
	s.reconcileOutgoing(time.Time{})
	s.rebuildMessages()
	if len(s.outgoing[s.key()]) != 0 || len(s.messageRows) != 1 || s.messageRows[0].ID != "turn|echo" {
		t.Fatal("delayed user echo did not replace exactly one accepted send")
	}
	s.transcript = outgoingTranscript()
	m.Status = "failed"
	s.outgoing[s.key()] = []*outgoingMessage{m}
	s.reconcileOutgoing(now.Add(time.Second))
	if len(s.outgoing[s.key()]) != 0 {
		t.Fatal("failed message absent from fresh history was retained")
	}
	m.FinishedAt = time.Time{}
	m.Status = "sending"
	s.outgoing[s.key()] = []*outgoingMessage{m}
	s.reconcileOutgoing(now.Add(time.Second))
	if len(s.outgoing[s.key()]) != 1 {
		t.Fatal("in-flight message removed by concurrent sync")
	}
}
func TestOutgoingAttachmentEchoUsesContentAndFullText(t *testing.T) {
	s := outgoingFixture()
	m := &outgoingMessage{Text: "inspect", Attachments: []Object{{"id": "upload", "sha256": "digest"}}}
	s.outgoing[s.key()] = []*outgoingMessage{m}
	s.transcript = outgoingTranscript(Object{"id": "echo", "role": "user", "text": "inspect", "attachments": []Object{{"id": "imported", "sha256": "other"}}})
	s.reconcileOutgoing(time.Time{})
	if len(s.outgoing[s.key()]) != 1 {
		t.Fatal("matched different attachment")
	}
	s.transcript = outgoingTranscript(Object{"id": "echo", "role": "user", "text": "inspect", "attachments": []Object{{"id": "imported", "sha256": "digest"}}})
	s.reconcileOutgoing(time.Time{})
	if len(s.outgoing[s.key()]) != 0 {
		t.Fatal("failed to merge imported attachment")
	}
}
func TestOutgoingRestoresFromDiskAndIsolatesSessions(t *testing.T) {
	s := outgoingFixture()
	s.restoreOutgoing(nil)
	original := Object{"localMessages": []*outgoingMessage{{ID: "pending", Text: "saved", Status: "sending", Known: map[string]bool{"old": true}}}}
	raw, _ := json.Marshal(original)
	var decoded Object
	_ = json.Unmarshal(raw, &decoded)
	s.restoreOutgoing(decoded)
	m := s.outgoing[s.key()][0]
	if m.Text != "saved" || m.Status != "uncertain" || !m.Known["old"] {
		t.Fatal("pending disk snapshot was not restored", m)
	}
	key := s.key()
	s.selected = Object{"providerId": "codex-app", "id": "other"}
	s.reconcileOutgoing(time.Now())
	s.rebuildMessages()
	if len(s.messageRows) != 0 || len(s.outgoing[key]) != 1 {
		t.Fatal("local message leaked between sessions")
	}
}

func TestCachedAndPreviewCataloguesExcludeSDK(t *testing.T) {
	sessions := []Object{
		{"id": "old-route", "providerId": "codex-cli", "sourceKind": "cli"},
		{"id": "sdk", "providerId": "codex-app", "originator": "codex_sdk_ts", "source": `"appServer"`},
		{"id": "exec", "providerId": "codex-app", "sourceKind": "exec"},
		{"id": "cli", "providerId": "codex-app", "sourceKind": "cli", "cwd": "/project"},
	}
	groups := projectGroups(sessions)
	if len(groups) != 1 || len(groups[0].Sessions) != 1 || str(groups[0].Sessions[0], "id") != "cli" {
		t.Fatal("cached SDK sessions leaked", groups)
	}
}

func TestFreshReconciliationPreservesRequestsStillRunningOrFinishedAfterSnapshotStarted(t *testing.T) {
	s := outgoingFixture()
	started := time.Now()
	sending := &outgoingMessage{ID: "inflight", Text: "in flight", Status: "sending", FinishedAt: started.Add(-time.Second)}
	recent := &outgoingMessage{ID: "recent", Text: "settled during read", Status: "failed", FinishedAt: started.Add(time.Second)}
	failed := &outgoingMessage{ID: "failed", Text: "absent failure", Status: "failed", FinishedAt: started.Add(-time.Second)}
	s.outgoing[s.key()] = []*outgoingMessage{sending, recent, failed}
	s.transcript = outgoingTranscript()
	s.reconcileOutgoing(started)
	if got := s.outgoing[s.key()]; len(got) != 2 || got[0] != sending || got[1] != recent {
		t.Fatal("reconciled incomplete write results", got)
	}
}

func TestOutgoingKeepsSendPositionThroughStreamingCacheAndEcho(t *testing.T) {
	s := outgoingFixture()
	a := &outgoingMessage{ID: "local-a", Text: "first failure", Status: "failed", Anchors: []string{"turn|old"}}
	b := &outgoingMessage{ID: "local-b", Text: "second failure", Status: "failed", Anchors: []string{"local-a", "turn|old"}}
	s.outgoing[s.key()] = []*outgoingMessage{a, b}
	s.transcript = outgoingTranscript(Object{"id": "old", "role": "assistant", "text": "before sending"}, Object{"id": "new", "role": "assistant", "text": "arrived later"})
	assertOrder := func(want ...string) {
		t.Helper()
		s.rebuildMessages()
		if len(s.messageRows) != len(want) {
			t.Fatalf("rows: %+v", s.messageRows)
		}
		for i, id := range want {
			if s.messageRows[i].ID != id {
				t.Fatalf("row %d = %s; want %s", i, s.messageRows[i].ID, id)
			}
		}
	}
	assertOrder("turn|old", "local-a", "local-b", "turn|new")
	// Rows have no timestamps; cache restoration must retain their anchors.
	snapshot := s.snapshotProjection()
	raw, _ := json.Marshal(snapshot)
	var cached Object
	_ = json.Unmarshal(raw, &cached)
	s.outgoing = map[string][]*outgoingMessage{}
	s.restoreOutgoing(cached)
	s.transcript = cached
	assertOrder("turn|old", "local-a", "local-b", "turn|new")
	// An acknowledged predecessor becomes a remote ID; the other overlay stays
	// beside it instead of falling to the end or jumping before the echo.
	s.transcript = outgoingTranscript(Object{"id": "old", "role": "assistant", "text": "before sending"}, Object{"id": "echo", "role": "user", "text": "first failure"}, Object{"id": "new", "role": "assistant", "text": "arrived later"})
	s.reconcileOutgoing(time.Time{})
	assertOrder("turn|old", "turn|echo", "local-b", "turn|new")
	// A removed nearest anchor falls back to a surviving predecessor.
	s.transcript = outgoingTranscript(Object{"id": "old", "role": "assistant", "text": "before sending"}, Object{"id": "new", "role": "assistant", "text": "arrived later"})
	assertOrder("turn|old", "local-b", "turn|new")
}

func TestOutgoingInInitiallyEmptyHistoryPrecedesLaterRemoteRows(t *testing.T) {
	s := outgoingFixture()
	s.outgoing[s.key()] = []*outgoingMessage{{ID: "local", Text: "pending", Status: "uncertain", Anchors: []string{}, FinishedAt: time.Now().Add(-time.Minute)}}
	s.transcript = outgoingTranscript(Object{"id": "new", "role": "assistant", "text": "later"})
	s.reconcileOutgoing(time.Now())
	s.rebuildMessages()
	if len(s.messageRows) != 2 || s.messageRows[0].ID != "local" {
		t.Fatal("unconfirmed write moved or removed", s.messageRows)
	}
}
