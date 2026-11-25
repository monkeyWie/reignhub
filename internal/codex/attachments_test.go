package codex

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/monkeyWie/reignhub/internal/assets"
)

func TestTranscriptAttachmentsAndSourceLifetime(t *testing.T) {
	cwd := t.TempDir()
	image := filepath.Join(cwd, "clipboard.png")
	png, _ := base64.StdEncoding.DecodeString("iVBORw0KGgoAAAANSUhEUgAAAAEAAAABCAQAAAC1HAwCAAAAC0lEQVR42mP8/x8AAwMCAO+jRZkAAAAASUVORK5CYII=")
	os.WriteFile(image, png, 0600)
	file := filepath.Join(cwd, "report.txt")
	os.WriteFile(file, []byte("fixture"), 0600)
	outside := filepath.Join(t.TempDir(), "private.txt")
	os.WriteFile(outside, []byte("private"), 0600)
	os.Symlink(outside, filepath.Join(cwd, "escape.txt"))
	items := []any{
		map[string]any{"id": "native", "type": "userMessage", "content": []any{map[string]string{"type": "localImage", "path": image}}},
		map[string]any{"id": "mentioned", "type": "userMessage", "content": []any{map[string]string{"type": "text", "text": "\n# Files mentioned by the user:\n\n## report.txt: " + file + "\n\n## My request:\nread file"}}},
		map[string]any{"id": "markdown", "type": "agentMessage", "text": "![image](clipboard.png)\n\n[report](report.txt)\n\n[denied](escape.txt)\n\n```\n![example](nonexistent.png)\n```"},
		map[string]any{"id": "generated", "type": "imageGeneration", "savedPath": image},
	}
	raw, _ := json.Marshal(map[string]any{"id": "t", "status": "completed", "items": items})
	p := &Provider{assets: assets.New(t.TempDir())}
	turn := normalizeTurn(raw)
	p.attachments(context.Background(), "s", cwd, &turn, raw)
	if len(turn.Items) != 4 {
		t.Fatal("items missing", len(turn.Items))
	}
	if turn.Items[0].Text != "" || len(turn.Items[0].Attachments) != 1 {
		t.Fatal("attachment-only user message lost")
	}
	if len(turn.Items[1].Attachments) != 1 || turn.Items[1].Attachments[0].Name != "report.txt" {
		t.Fatal("desktop preamble not recognized")
	}
	if len(turn.Items[2].Attachments) != 2 || !turn.Items[2].Attachments[0].Inline {
		t.Fatalf("Markdown AST export incorrect: %+v", turn.Items[2].Attachments)
	}
	if turn.Items[3].Role != "assistant" || len(turn.Items[3].Attachments) != 1 {
		t.Fatal("generated image not visible")
	}
	os.Remove(image)
	next := normalizeTurn(raw)
	p.attachments(context.Background(), "s", cwd, &next, raw)
	if next.Items[0].Attachments[0].State != "ready" {
		t.Fatal("temp image not archived")
	}
	// Build Codex's actual input payload without sending a model instruction.
	input, e := p.attachmentInput("s", "", []string{turn.Items[0].Attachments[0].ID, turn.Items[1].Attachments[0].ID})
	if e != nil || len(input) != 2 {
		t.Fatal(input, e)
	}
	encoded, _ := json.Marshal(input)
	if !strings.Contains(string(encoded), `"type":"localImage"`) || !strings.Contains(string(encoded), "report.txt") {
		t.Fatal("bad Codex input")
	}
	if _, e = p.attachmentInput("different", "", []string{turn.Items[0].Attachments[0].ID}); e == nil {
		t.Fatal("cross-session send allowed")
	}
}

func TestSourceLocationLinksStayInText(t *testing.T) {
	cwd := t.TempDir()
	readme := filepath.Join(cwd, "README.md")
	// File URLs use forward slashes and an empty authority, including on Windows.
	readmeURL := url.URL{Scheme: "file", Path: "/" + strings.TrimPrefix(filepath.ToSlash(readme), "/")}
	if err := os.WriteFile(readme, []byte("# Example\n"), 0600); err != nil {
		t.Fatal(err)
	}
	p := &Provider{assets: assets.New(t.TempDir())}
	for _, tc := range []struct {
		name, destination string
		wantAttachments   int
	}{
		{"relative line", "README.md:3", 0},
		{"absolute line", readme + ":3", 0},
		{"line and column", readme + ":3:5", 0},
		{"file URL line", readmeURL.String() + ":3", 0},
		{"line anchor", "README.md#L3", 0},
		{"line range", "README.md#L3-L8", 0},
		{"line column range", "README.md#L3C2-L8C5", 0},
		{"download", "README.md", 1},
		{"missing download", "report.pdf", 1},
	} {
		t.Run(tc.name, func(t *testing.T) {
			message := "See [README](" + tc.destination + ")."
			raw, err := json.Marshal(map[string]any{
				"id": "turn", "status": "completed", "items": []any{
					// Reasoning content is an array of strings; it must not
					// prevent attachments in other items from being parsed.
					map[string]any{"id": "reasoning", "type": "reasoning", "summary": []string{"Summary"}, "content": []string{"Internal content"}},
					map[string]any{"id": "answer", "type": "agentMessage", "text": message},
				},
			})
			if err != nil {
				t.Fatal(err)
			}
			turn := normalizeTurn(raw)
			p.attachments(context.Background(), tc.name, cwd, &turn, raw)
			item := turn.Items[1]
			if item.Text != message {
				t.Fatal("source navigation link was changed")
			}
			if len(item.Attachments) != tc.wantAttachments {
				t.Fatalf("got %d attachments, want %d", len(item.Attachments), tc.wantAttachments)
			}
			if tc.wantAttachments == 0 {
				return
			}
			if tc.name != "missing download" && item.Attachments[0].State != "ready" {
				t.Fatal("ordinary file download lost")
			}
			if tc.name == "missing download" && item.Attachments[0].State != "missing" {
				t.Fatal("genuinely missing attachment must retain its state")
			}
		})
	}
}

func TestFileLinkAliasesKeepDownloadableReferences(t *testing.T) {
	cwd := t.TempDir()
	path := filepath.Join(cwd, "report.md")
	if err := os.WriteFile(path, []byte("fixture"), 0600); err != nil {
		t.Fatal(err)
	}
	p := &Provider{assets: assets.New(t.TempDir())}
	message := "[absolute](<" + filepath.ToSlash(path) + ">) [relative](report.md) [line](report.md:2)"
	raw, _ := json.Marshal(map[string]any{"id": "turn", "items": []any{map[string]any{"id": "a", "type": "agentMessage", "text": message}}})
	turn := normalizeTurn(raw)
	p.attachments(context.Background(), "aliases", cwd, &turn, raw)
	if len(turn.Items[0].Attachments) != 2 {
		t.Fatalf("file aliases lost their download mapping: %+v", turn.Items[0].Attachments)
	}
	for _, a := range turn.Items[0].Attachments {
		if a.State != "ready" {
			t.Fatalf("unreadable alias: %+v", a)
		}
	}
}
