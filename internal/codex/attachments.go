package codex

import (
	"context"
	"encoding/json"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"strings"

	"github.com/monkeyWie/reignhub/internal/assets"
	"github.com/monkeyWie/reignhub/internal/provider"
	"github.com/monkeyWie/reignhub/pkg/filelink"
	"github.com/yuin/goldmark"
	"github.com/yuin/goldmark/ast"
	"github.com/yuin/goldmark/text"
)

var fileMention = regexp.MustCompile(`(?m)^## [^\n]+?: (.+)$`)

func (p *Provider) providerID() string {
	return "codex-app"
}

func (p *Provider) attachments(ctx context.Context, session, cwd string, turn *provider.Turn, raw json.RawMessage) {
	var native struct {
		Items []struct {
			ID, Type, SavedPath string
			Content, Input      json.RawMessage
		}
	}
	if json.Unmarshal(raw, &native) != nil {
		return
	}
	byID := map[string]int{}
	for i := range turn.Items {
		byID[turn.Items[i].ID] = i
	}
	scope := assets.Scope(p.providerID(), session)
	for _, n := range native.Items {
		idx, ok := byID[n.ID]
		if !ok {
			continue
		}
		item := &turn.Items[idx]
		add := func(ref, source string, inline bool) {
			if ctx.Err() != nil || len(item.Attachments) >= 32 {
				return
			}
			if strings.HasPrefix(source, "file://") {
				if u, e := url.Parse(source); e == nil && (u.Host == "" || u.Host == "localhost") {
					source = u.Path
				}
			}
			item.Attachments = append(item.Attachments, p.assets.Import(ctx, scope, turn.ID+"/"+n.ID+"/"+ref, source, inline))
		}
		if n.Type == "userMessage" || n.Type == "steeringUserMessage" {
			if n.Type == "steeringUserMessage" {
				n.Content = n.Input
			}
			var contents []struct{ Type, Text, Path, URL, FileID string }
			if json.Unmarshal(n.Content, &contents) != nil {
				continue
			}
			var body strings.Builder
			for i, content := range contents {
				switch content.Type {
				case "text":
					if content.Text != "" {
						body.WriteString(content.Text)
						body.WriteByte('\n')
					}
				case "localImage":
					if content.Path != "" {
						add(fmt.Sprintf("input-%d", i), content.Path, false)
					}
				case "image":
					if strings.HasPrefix(content.URL, "data:image/") {
						add(fmt.Sprintf("input-%d", i), content.URL, false)
					} else {
						// Remote opaque file IDs have no local bytes to export.
						item.Attachments = append(item.Attachments, provider.Attachment{ID: "external-" + n.ID + fmt.Sprint(i), Name: "图片", Kind: "image", State: "unavailable"})
					}
				}
			}
			item.Text = body.String()
			// Desktop attachments can be represented in the recorded text preamble
			// rather than a native localImage input. Only recognize that exact envelope.
			if header, _, ok := strings.Cut(strings.TrimLeft(item.Text, "\r\n "), "## My request:"); ok && strings.HasPrefix(header, "# Files mentioned by the user:") {
				for i, entry := range fileMention.FindAllStringSubmatch(header, 32) {
					path := strings.TrimSpace(entry[1])
					if !filepath.IsAbs(path) {
						continue
					}
					duplicate := false
					for _, a := range item.Attachments {
						if a.Source == path {
							duplicate = true
							break
						}
					}
					if !duplicate {
						add(fmt.Sprintf("mentioned-%d", i), path, false)
					}
				}
			}
		}
		if n.Type == "imageGeneration" && n.SavedPath != "" {
			add("generated", n.SavedPath, false)
			item.Role, item.Text = "assistant", ""
		}
		if (item.Role != "assistant" && item.Role != "user") || len(item.Text) > 1<<20 {
			continue
		}
		root := goldmark.DefaultParser().Parse(text.NewReader([]byte(item.Text)))
		_ = ast.Walk(root, func(node ast.Node, entering bool) (ast.WalkStatus, error) {
			if !entering {
				return ast.WalkContinue, nil
			}
			var source string
			inline := false
			switch link := node.(type) {
			case *ast.Image:
				source, inline = string(link.Destination), true
			case *ast.Link:
				source = string(link.Destination)
				if _, location := filelink.Path(source); location {
					return ast.WalkContinue, nil
				}
			default:
				return ast.WalkContinue, nil
			}
			path := filelink.Resolve(source, cwd)
			if path == "" {
				return ast.WalkContinue, nil
			}
			// Only export Markdown file references beneath this workspace or the
			// Hub attachment directory, not arbitrary paths sent by a controller.
			if !beneath(cwd, path) && !beneath(p.assets.Root, path) {
				return ast.WalkContinue, nil
			}
			for _, a := range item.Attachments {
				if a.Source == source {
					return ast.WalkContinue, nil
				}
			}
			a := p.assets.Import(ctx, scope, turn.ID+"/"+n.ID+"/link/"+source, path, inline)
			a.Source = source
			item.Attachments = append(item.Attachments, a)
			if len(item.Attachments) >= 32 {
				return ast.WalkStop, nil
			}
			return ast.WalkContinue, nil
		})
	}
}

func beneath(root, path string) bool {
	if root == "" {
		return false
	}
	root, err := filepath.Abs(root)
	if err != nil {
		return false
	}
	path, err = filepath.Abs(path)
	if err != nil {
		return false
	}
	rel, err := filepath.Rel(root, path)
	if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(os.PathSeparator)) {
		return false
	}
	// Also check resolved targets when present, so a workspace symlink cannot
	// turn a Markdown attachment link into an arbitrary file export.
	resolved, e := filepath.EvalSymlinks(path)
	if e != nil {
		return true
	}
	resolvedRoot, e := filepath.EvalSymlinks(root)
	if e != nil {
		return false
	}
	rel, e = filepath.Rel(resolvedRoot, resolved)
	return e == nil && rel != ".." && !strings.HasPrefix(rel, ".."+string(os.PathSeparator))
}

func (p *Provider) attachmentInput(session, message string, ids []string) ([]any, error) {
	if len(ids) > 16 {
		return nil, fmt.Errorf("每条消息最多 16 个附件")
	}
	input := []any{}
	if strings.TrimSpace(message) != "" {
		input = append(input, map[string]string{"type": "text", "text": message})
	}
	files := []string{}
	for _, id := range ids {
		meta, path, e := p.assets.Input(assets.Scope(p.providerID(), session), id)
		if e != nil {
			return nil, e
		}
		if meta.Kind == "image" {
			input = append(input, map[string]string{"type": "localImage", "path": path})
		} else {
			// Codex has native image input; ordinary files are local references
			// that the Agent can read with its normal filesystem tools.
			files = append(files, fmt.Sprintf("- [%s](<%s>)", strings.ReplaceAll(meta.Name, "]", "\\]"), filepath.ToSlash(path)))
		}
	}
	if len(files) > 0 {
		input = append(input, map[string]string{"type": "text", "text": "\nReignHub 上传的附件（已保存在本机，可按需读取）：\n" + strings.Join(files, "\n")})
	}
	if len(input) == 0 {
		return nil, fmt.Errorf("消息不能为空")
	}
	return input, nil
}

func (p *Provider) SendAttachments(ctx context.Context, session, message, model string, ids []string) error {
	input, e := p.attachmentInput(session, message, ids)
	if e != nil {
		return e
	}
	return p.sendInput(ctx, session, input, model)
}
func (p *Provider) SteerAttachments(ctx context.Context, session, turn, message string, ids []string) error {
	input, e := p.attachmentInput(session, message, ids)
	if e != nil {
		return e
	}
	return p.steerInput(ctx, session, turn, input)
}
