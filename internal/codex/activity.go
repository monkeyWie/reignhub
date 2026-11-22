package codex

import (
	"encoding/json"
	"strings"

	"github.com/monkeyWie/reignhub/internal/provider"
)

func toolActivities(raw json.RawMessage) []provider.Activity {
	var i struct {
		Type, Command, Server, Tool, Namespace, Path, Query, Text string
		CommandActions                                            []struct{ Type, Command, Name, Path, Query string }
		Changes                                                   []struct{ Path, Diff string }
		Action                                                    struct {
			Type, Query, URL, Pattern string
			Queries                   []string
		}
	}
	if json.Unmarshal(raw, &i) != nil {
		return nil
	}
	activity := func(kind, title, detail string) []provider.Activity {
		return []provider.Activity{{Kind: kind, Title: title, Detail: detail}}
	}
	switch i.Type {
	case "commandExecution":
		var out []provider.Activity
		for _, a := range i.CommandActions {
			command := a.Command
			if command == "" {
				command = i.Command
			}
			switch a.Type {
			case "read":
				name := a.Name
				if name == "" {
					name = a.Path
				}
				out = append(out, provider.Activity{Kind: "read", Title: strings.TrimSpace("读取 " + name), Detail: strings.TrimSpace(a.Path + "\n" + command)})
			case "listFiles":
				out = append(out, provider.Activity{Kind: "list", Title: strings.TrimSpace("列出文件 " + a.Path), Detail: command})
			case "search":
				out = append(out, provider.Activity{Kind: "search", Title: strings.TrimSpace("搜索 " + a.Query), Detail: strings.TrimSpace(a.Path + "\n" + command)})
			default:
				out = append(out, provider.Activity{Kind: "command", Title: "运行命令", Detail: command})
			}
		}
		if len(out) == 0 {
			return activity("command", "运行命令", i.Command)
		}
		return out
	case "fileChange":
		var out []provider.Activity
		for _, c := range i.Changes {
			out = append(out, provider.Activity{Kind: "edit", Title: "编辑 " + c.Path, Detail: c.Diff})
		}
		if len(out) == 0 {
			return activity("edit", "修改文件", "")
		}
		return out
	case "mcpToolCall":
		return activity("tool", "调用 "+strings.Trim(i.Server+"."+i.Tool, "."), "")
	case "dynamicToolCall":
		return activity("tool", "调用 "+strings.Trim(i.Namespace+"."+i.Tool, "."), "")
	case "collabAgentToolCall":
		return activity("agent", "调用子 Agent "+i.Tool, "")
	case "webSearch":
		switch i.Action.Type {
		case "openPage":
			return activity("web", "打开网页 "+i.Action.URL, "")
		case "findInPage":
			return activity("web", "查找网页内容 "+i.Action.Pattern, i.Action.URL)
		}
		query := i.Query
		if query == "" {
			query = i.Action.Query
		}
		if query == "" {
			query = strings.Join(i.Action.Queries, ", ")
		}
		return activity("web", strings.TrimSpace("搜索网页 "+query), "")
	case "imageView":
		return activity("image", "查看图片 "+i.Path, "")
	case "imageGeneration":
		return activity("image", "生成图片", "")
	case "plan":
		return activity("plan", "更新计划", i.Text)
	default:
		return nil
	}
}
