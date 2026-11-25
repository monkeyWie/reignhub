package codex

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/monkeyWie/reignhub/internal/provider"
	"github.com/monkeyWie/reignhub/internal/rpc"
)

const asyncQuestionMethod = "question/async"
const serverQuestionRoute = "service:questions"
const questionReplyOpen = "<send_user_message_question_reply>"
const questionReplyClose = "</send_user_message_question_reply>"

type asyncQuestionReply struct {
	QuestionItemID string `json:"questionItemId"`
	Question       string `json:"question"`
	Answer         string `json:"answer"`
}

func validateQuestionDecision(a provider.Approval, d provider.Decision) error {
	if d.Decision == "skip" && a.AllowSkip && len(d.Answers) == 0 {
		return nil
	}
	if d.Decision != "input" {
		return errors.New("请选择或填写回答")
	}
	known := map[string]bool{}
	for _, q := range a.Questions {
		known[q.ID] = true
		answer := d.Answers[q.ID].Answers
		if len(answer) > 1 || (!a.AllowSkip && (len(answer) != 1 || strings.TrimSpace(answer[0]) == "")) {
			return errors.New("请回答所有问题")
		}
	}
	count := 0
	for id, answer := range d.Answers {
		if !known[id] || len(answer.Answers) != 1 || strings.TrimSpace(answer.Answers[0]) == "" {
			return errors.New("问题已变化，请重新选择回答")
		}
		count++
	}
	if count == 0 {
		return errors.New("请选择或填写回答")
	}
	return nil
}

// Mid-turn async prompts are agentMessage metadata, not app-server requests.
// Only the latest turn is actionable. A new turn expires its predecessor's
// unanswered prompts; an accepted reply hides exactly the matching question ID.
func asyncQuestionRequests(id string, turns []map[string]any, dismissed map[string]bool) []any {
	if len(turns) == 0 {
		return nil
	}
	turn := turns[len(turns)-1]
	if turn["status"] != "inProgress" && turn["status"] != "completed" {
		return nil
	}
	items := array(turn["items"])
	answered := map[string]bool{}
	for _, raw := range items {
		i := object(raw)
		content := i["content"]
		if i["type"] == "steeringUserMessage" && (i["status"] == "accepted" || i["steerAcknowledged"] == true) {
			content = i["input"]
		} else if i["type"] != "userMessage" {
			continue
		}
		text := []string{}
		for _, value := range array(content) {
			if c := object(value); c["type"] == "text" {
				text = append(text, stringValue(c["text"]))
			}
		}
		value := strings.TrimSpace(strings.Join(text, "\n"))
		if !strings.HasPrefix(value, questionReplyOpen) || !strings.HasSuffix(value, questionReplyClose) {
			continue
		}
		var replies []asyncQuestionReply
		if json.Unmarshal([]byte(strings.TrimSuffix(strings.TrimPrefix(value, questionReplyOpen), questionReplyClose)), &replies) == nil {
			for _, reply := range replies {
				if strings.TrimSpace(reply.Answer) != "" {
					answered[reply.QuestionItemID] = true
				}
			}
		}
	}
	requests := []any{}
	for _, raw := range items {
		i := object(raw)
		itemID := stringValue(i["id"])
		if i["type"] != "agentMessage" || itemID == "" {
			continue
		}
		questions := []provider.Question{}
		for index, raw := range array(i["questions"]) {
			q := object(raw)
			id := string(rawJSON([]any{"request_user_input_async", itemID, index}))
			if answered[id] || dismissed[id] || strings.TrimSpace(stringValue(q["title"])) == "" {
				continue
			}
			question := provider.Question{ID: id, Question: stringValue(q["title"])}
			for _, option := range array(q["options"]) {
				if value := stringValue(option); value != "" {
					question.Options = append(question.Options, provider.QuestionOption{Label: value})
				}
			}
			questions = append(questions, question)
		}
		if len(questions) == 0 {
			continue
		}
		params := map[string]any{"threadId": id, "turnId": turn["id"], "questions": questions}
		// Updating/answering part of a form invalidates stale drafts and decisions.
		key := fmt.Sprintf("async:%s:%s:%x", stringValue(turn["id"]), itemID, sha256.Sum256(rawJSON(questions)))
		requests = append(requests, map[string]any{"method": asyncQuestionMethod, "requestId": key, "params": decodeObject(rawJSON(params))})
	}
	return requests
}

func (p *Provider) dismissedQuestions(id, owner, except string) map[string]bool {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.dismissedQuestionsLocked(id, "ipc:"+id+":"+owner, "", except)
}

func (p *Provider) dismissedQuestionsLocked(id, route, turn, except string) map[string]bool {
	dismissed := map[string]bool{}
	for _, entry := range p.pending {
		if entry.claimed && entry.route == route && entry.approval.SessionID == id &&
			(turn == "" || entry.approval.TurnID == turn) && entry.requestID != except && entry.approval.Method == asyncQuestionMethod {
			for _, q := range entry.approval.Questions {
				dismissed[q.ID] = true
			}
		}
	}
	return dismissed
}

func (b *appIPC) answerAsyncQuestions(ctx context.Context, id, owner string, request map[string]any, result any) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	raw, err := b.waitState(ctx, id, owner)
	if err != nil {
		return err
	}
	state := decodeObject(raw)
	current := false
	turns, _, err := stateTurns(state)
	if err != nil {
		return err
	}
	for _, value := range asyncQuestionRequests(id, turns, b.p.dismissedQuestions(id, owner, string(rawJSON(request["requestId"])))) {
		if object(value)["requestId"] == request["requestId"] {
			current = true
			break
		}
	}
	if !current {
		return errors.New("问题已回答或已过期，请刷新会话")
	}
	var input struct{ Questions []provider.Question }
	_ = json.Unmarshal(rawJSON(request["params"]), &input)
	message := asyncQuestionInput(input.Questions, result)
	method := "thread-follower-start-turn"
	params := map[string]any{"conversationId": id, "turnStart": map[string]any{"request": ipcTurnStart(state, id, message, ""), "context": map[string]any{"useAppServerPermissionDefault": true}}}
	if turns[len(turns)-1]["status"] == "inProgress" {
		method = "thread-follower-steer-turn"
		params = ipcSteerParams(state, id, message)
	}
	_, err = b.request(ctx, method, params, owner)
	if err == nil {
		b.markDirty(id)
	}
	return ipcWriteError(method, err)
}

func asyncQuestionInput(questions []provider.Question, result any) []any {
	var decision provider.Decision
	_ = json.Unmarshal(rawJSON(result), &decision)
	replies := make([]asyncQuestionReply, 0, len(questions))
	for _, q := range questions {
		if answer := decision.Answers[q.ID].Answers; len(answer) == 1 {
			replies = append(replies, asyncQuestionReply{q.ID, q.Question, answer[0]})
		}
	}
	text := questionReplyOpen + "\n" + string(rawJSON(replies)) + "\n" + questionReplyClose
	return ipcInput([]any{map[string]any{"type": "text", "text": text}})
}

// SessionApprovals reads the newest turn on the shared backend. Other routes
// get their requests from their existing IPC follower or server notifications.
func (p *Provider) SessionApprovals(ctx context.Context, id string) ([]provider.Approval, error) {
	p.mu.Lock()
	client, shared := p.client, p.shared
	p.mu.Unlock()
	if shared != nil && client != nil && client.Alive() {
		key := struct {
			client *rpc.Client
			id     string
		}{client, id}
		if _, reading := p.questionReads.LoadOrStore(key, struct{}{}); !reading {
			defer p.questionReads.Delete(key)
			if turns, err := readServerQuestionTurn(ctx, client, id); err == nil && ctx.Err() == nil {
				p.syncServerQuestions(client, id, turns)
			}
		}
	}
	return p.approvals(id), ctx.Err()
}

func readServerQuestionTurn(ctx context.Context, client *rpc.Client, id string) ([]map[string]any, error) {
	ctx, cancel := context.WithTimeout(ctx, 2*time.Second)
	defer cancel()
	var page struct{ Data []map[string]any }
	if err := client.Call(ctx, "thread/turns/list", map[string]any{
		"threadId": id, "limit": 1, "sortDirection": "desc", "itemsView": "full",
	}, &page); err != nil {
		return nil, err
	}
	if page.Data == nil || len(page.Data) > 1 || len(page.Data) == 1 && stringValue(page.Data[0]["id"]) == "" {
		return nil, errors.New("无法确认 Codex 最新消息")
	}
	return page.Data, nil
}

func (p *Provider) syncServerQuestions(client *rpc.Client, id string, turns []map[string]any) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.client != client || p.shared == nil || !client.Alive() {
		return
	}
	latest := ""
	if len(turns) > 0 {
		latest = stringValue(turns[len(turns)-1]["id"])
	}
	existing := map[string]string{}
	for key, entry := range p.pending {
		if entry.route != serverQuestionRoute || entry.approval.SessionID != id {
			continue
		}
		if !entry.claimed || entry.approval.TurnID != latest {
			existing[entry.requestID] = key
		}
	}
	dismissed := p.dismissedQuestionsLocked(id, serverQuestionRoute, latest, "")
	for _, raw := range asyncQuestionRequests(id, turns, dismissed) {
		request := object(raw)
		key := stringValue(request["requestId"])
		if _, ok := existing[key]; ok {
			delete(existing, key)
			continue
		}
		params := rawJSON(request["params"])
		var input struct{ Questions []provider.Question }
		_ = json.Unmarshal(params, &input)
		a := provider.Approval{ProviderID: "codex", SessionID: id, TurnID: latest, Method: asyncQuestionMethod,
			Params: params, CreatedAt: time.Now(), Questions: input.Questions, AllowSkip: true}
		p.addPendingLocked(a, func(ctx context.Context, result any) error {
			return p.answerServerQuestions(ctx, client, key, a, result)
		}, serverQuestionRoute, key)
	}
	for _, key := range existing {
		delete(p.pending, key)
	}
}

func (p *Provider) answerServerQuestions(ctx context.Context, client *rpc.Client, key string, a provider.Approval, result any) error {
	turns, err := readServerQuestionTurn(ctx, client, a.SessionID)
	if err != nil {
		return err
	}
	p.mu.Lock()
	current := p.client == client && p.shared != nil
	dismissed := p.dismissedQuestionsLocked(a.SessionID, serverQuestionRoute, a.TurnID, key)
	p.mu.Unlock()
	if !current {
		return provider.ErrUnavailable
	}
	for _, raw := range asyncQuestionRequests(a.SessionID, turns, dismissed) {
		if object(raw)["requestId"] != key {
			continue
		}
		if err := ctx.Err(); err != nil {
			return err
		}
		params := serverInputParams(a.SessionID, asyncQuestionInput(a.Questions, result))
		method := "turn/start"
		if turns[len(turns)-1]["status"] == "inProgress" {
			method = "turn/steer"
			params["expectedTurnId"] = a.TurnID
		}
		return sharedWriteError(method, client.Call(ctx, method, params, nil))
	}
	return errors.New("问题已回答或已过期，请刷新会话")
}
