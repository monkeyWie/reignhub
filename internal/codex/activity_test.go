package codex

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/monkeyWie/reignhub/internal/provider"
)

func TestMessageTimesKeepRecordedPrecisionAndMarkTurnFallbacks(t *testing.T) {
	turn := normalizeTurn(json.RawMessage(`{"id":"turn","status":"completed","startedAt":1700000000,"completedAt":1700000060,"items":[
		{"id":"user","type":"userMessage","startedAtMs":1700000000123,"content":[{"type":"text","text":"question"}]},
		{"id":"progress","type":"agentMessage","phase":"commentary","text":"working"},
		{"id":"answer","type":"agentMessage","phase":"final_answer","text":"answer"},
		{"id":"exact","type":"agentMessage","completedAtMs":1700000050456,"text":"recorded"}
	]}`))
	want := []int64{1700000000123, 1700000000000, 1700000060000, 1700000050456}
	sources := []string{"", "turnStarted", "turnCompleted", ""}
	for i, item := range turn.Items {
		if item.TimestampMS == nil || *item.TimestampMS != want[i] || item.TimestampSource != sources[i] {
			t.Fatalf("incorrect message time: %+v", item)
		}
	}
	old := normalizeTurn(json.RawMessage(`{"id":"old","status":"completed","items":[{"id":"user","type":"userMessage","content":[{"type":"text","text":"old"}]}]}`))
	if old.Items[0].TimestampMS != nil {
		t.Fatal("must not invent a timestamp for history with no recorded time")
	}
}

func TestReasoningKeepsDisplaySummaryWithoutExposingContent(t *testing.T) {
	turn := normalizeTurn(json.RawMessage(`{"id":"turn","status":"inProgress","items":[
		{"id":"user","type":"userMessage","content":[{"type":"text","text":"question"}]},
		{"id":"reason","type":"reasoning","text":"raw-text-sentinel","summary":["**检查端口**","**梳理授权与 TLS 边界**\n\nDisplay summary body"," "],"content":["raw-content-sentinel"]},
		{"id":"empty","type":"reasoning","summary":[],"content":["another-content-sentinel"]}
	]}`))
	if len(turn.Items) != 3 {
		t.Fatalf("reasoning with string content was dropped: %d items", len(turn.Items))
	}
	item := turn.Items[1]
	if item.Role != "reasoning" || item.Text != "" || item.ReasoningSummary != "**梳理授权与 TLS 边界**\n\nDisplay summary body" {
		t.Fatalf("display summary not kept in explicit metadata: %+v", item)
	}
	if turn.Items[2].ReasoningSummary != "" || turn.Items[2].Text != "" {
		t.Fatal("empty summary must never fall back to reasoning content")
	}
	raw, err := json.Marshal(turn)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(raw), "sentinel") {
		t.Fatal("raw reasoning content was forwarded")
	}
}

func TestActivitySnapshotsPreserveToolLifecycleAndStructuredActions(t *testing.T) {
	turn := normalizeTurn(json.RawMessage(`{"id":"turn","status":"inProgress","items":[
		{"id":"read","type":"commandExecution","status":"completed","command":"inspect files","aggregatedOutput":"private output","durationMs":1250,"commandActions":[{"type":"read","name":"main.dart","path":"/project/main.dart","command":"cat main.dart"},{"type":"search","query":"widget","path":"/project","command":"rg widget"},{"type":"listFiles","path":"/project","command":"ls"}]},
		{"id":"run","type":"commandExecution","status":"failed","command":"go test"},
		{"id":"edit","type":"fileChange","status":"inProgress","changes":[{"path":"main.go","diff":"+new line"}]},
		{"id":"mcp","type":"mcpToolCall","server":"docs","tool":"search","status":"completed","arguments":{"private":"argument"}},
		{"id":"dynamic","type":"dynamicToolCall","namespace":"custom","tool":"fetch","status":"inProgress"},
		{"id":"web","type":"webSearch","action":{"type":"openPage","url":"https://example.com"}},
		{"id":"reason","type":"reasoning","summary":[]},
		{"id":"commentary","type":"agentMessage","phase":"commentary","text":"progress"}
	]}`))
	if len(turn.Items) != 8 {
		t.Fatalf("missing items: %+v", turn)
	}
	read := turn.Items[0]
	if read.Text != "" {
		t.Fatal("invisible raw tool output competes with assistant replies")
	}
	if read.Status != "completed" || read.DurationMS == nil || *read.DurationMS != 1250 {
		t.Fatalf("item inherited turn state or lost duration: %+v", read)
	}
	if len(read.Activities) != 3 || read.Activities[0].Kind != "read" || read.Activities[1].Kind != "search" || read.Activities[2].Kind != "list" {
		t.Fatalf("command actions lost: %+v", read.Activities)
	}
	if read.Activities[0].Title != "读取 main.dart" || !strings.Contains(read.Activities[0].Detail, "/project/main.dart") {
		t.Fatal("read target lost", read.Activities[0])
	}
	if turn.Items[1].Status != "failed" || turn.Items[1].Activities[0] != (provider.Activity{Kind: "command", Title: "运行命令", Detail: "go test"}) {
		t.Fatal("failed command lost")
	}
	if turn.Items[2].Status != "inProgress" || turn.Items[2].Activities[0].Detail != "+new line" {
		t.Fatal("edit lost")
	}
	if turn.Items[3].Activities[0].Title != "调用 docs.search" || turn.Items[4].Activities[0].Title != "调用 custom.fetch" {
		t.Fatal("tool identity lost")
	}
	if turn.Items[5].Status != "" {
		t.Fatal("missing item status must not imply running")
	}
	if turn.Items[6].Role != "reasoning" || turn.Items[7].Phase != "commentary" {
		t.Fatal("reasoning marker or commentary phase lost")
	}
	for _, item := range turn.Items {
		for _, activity := range item.Activities {
			if strings.Contains(activity.Detail, "private") {
				t.Fatal("raw output or arguments leaked into activity metadata")
			}
		}
	}
}

func TestUserImageInputHasNoSyntheticTextMarker(t *testing.T) {
	turn := normalizeTurn(json.RawMessage(`{"id":"turn","status":"inProgress","items":[{"id":"u","type":"userMessage","content":[{"type":"text","text":"看看图片"},{"type":"localImage","path":"/tmp/picture.png"},{"type":"image","url":"data:image/png;base64,AAAA"}]}]}`))
	if len(turn.Items) != 1 || turn.Items[0].Text != "看看图片\n" {
		t.Fatalf("image marker leaked: %+v", turn.Items)
	}
}
