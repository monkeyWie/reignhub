// Package provider is the application boundary for agent integrations.
// Native protocols and application launch details stay inside each provider.
package provider

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"time"
)

var ErrUnavailable = errors.New("provider unavailable")

type Capabilities struct {
	ComposerSettings bool `json:"composerSettings,omitempty"`
	// Terminal is supplied by the desktop host, independently of the Agent.
	Terminal         bool `json:"terminal,omitempty"`
	TerminalProfiles bool `json:"terminalProfiles,omitempty"`
	// SessionRemoval describes the implemented outcome: archive, detach or delete.
	// Empty means the Provider does not support removing a session.
	SessionRemoval     string `json:"sessionRemoval,omitempty"`
	MaxAttachmentBytes int64  `json:"maxAttachmentBytes,omitempty"`
	Streaming          bool   `json:"streaming,omitempty"`
	History            bool   `json:"history"`
	Create             bool   `json:"create"`
	Send               bool   `json:"send"`
	Steer              bool   `json:"steer"`
	Interrupt          bool   `json:"interrupt"`
	Approvals          bool   `json:"approvals"`
	Desktop            bool   `json:"desktop"`
	ImageInput         bool   `json:"imageInput"`
	FileInput          bool   `json:"fileInput"`
}

type Instance struct {
	PID       int    `json:"pid"`
	StartedAt string `json:"startedAt"`
	Path      string `json:"path"`
}

type Status struct {
	ID             string       `json:"id"`
	Name           string       `json:"name"`
	State          string       `json:"state"`
	Detail         string       `json:"detail"`
	Capabilities   Capabilities `json:"capabilities"`
	Instances      []Instance   `json:"instances"`
	DesktopClients int          `json:"desktopClients"`
	Endpoint       string       `json:"endpoint,omitempty"`
	Version        string       `json:"version,omitempty"`
	LastError      string       `json:"lastError,omitempty"`
	Controllable   bool         `json:"controllable"`
	BackendID      string       `json:"backendId,omitempty"`
	Installed      bool         `json:"installed"`
	Enabled        bool         `json:"enabled"`
	Running        bool         `json:"running"`
	InstallPath    string       `json:"installPath,omitempty"`
}

// Ready requires a live protocol connection, not merely an installed binary or process.
func (s Status) Ready() bool { return s.State == "ready" || s.State == "connected" }

// AutoConnector prepares an installed, configured Agent without user prompts,
// model execution, or restarting the user's application. The host owns enablement.
type AutoConnector interface {
	AutoConnect(context.Context) error
}

// Project retains an Agent's native project metadata. Its ID is Agent-local;
// controllers group by Session.ProjectDir, never by this ID or project name.
type Project struct {
	ID    string   `json:"id,omitempty"`
	Name  string   `json:"name,omitempty"`
	Root  string   `json:"root,omitempty"`
	Roots []string `json:"roots,omitempty"`
}

// GitInfo describes the checkout at the execution directory, resolved by its host.
// Repository is a credential-free display name; Commit is used for detached HEAD.
type GitInfo struct {
	Repository string `json:"repository,omitempty"`
	Branch     string `json:"branch,omitempty"`
	Commit     string `json:"commit,omitempty"`
}

type Session struct {
	ID         string   `json:"id"`
	ProviderID string   `json:"providerId"`
	Title      string   `json:"title"`
	Preview    string   `json:"preview"`
	CWD        string   `json:"cwd"`
	Project    *Project `json:"project,omitempty"`
	GitInfo    *GitInfo `json:"gitInfo,omitempty"`
	// ProjectDir is the host-resolved project directory across all Agents.
	// CWD remains the session's actual execution directory, including worktrees.
	ProjectDir string `json:"projectDir,omitempty"`
	// ProjectKey correlates the same repository/project across environments.
	// An absent key means the controller must keep device-local paths separate.
	ProjectKey string `json:"projectKey,omitempty"`
	Status     string `json:"status"`
	Source     string `json:"source"`
	// SourceKind describes the recorded creation client, independently of ProviderID
	// (the connection used to read/control this session). Source retains the raw value.
	SourceKind string `json:"sourceKind,omitempty"`
	Originator string `json:"originator,omitempty"`
	// RecencyAt is the latest user submission or successful reply completion, in Unix milliseconds.
	// Zero means unknown; content/metadata updates must never advance it.
	RecencyAt    int64  `json:"recencyAt,omitempty"`
	UpdatedAt    int64  `json:"updatedAt"`
	CreatedAt    int64  `json:"createdAt"`
	ActiveTurnID string `json:"activeTurnId,omitempty"`
	Model        string `json:"model,omitempty"`
	// CodexOwnerUnreachable identifies a held session whose IPC owner could not be reached.
	CodexOwnerUnreachable bool `json:"codexOwnerUnreachable,omitempty"`
}

type SessionPage struct {
	Sessions   []Session `json:"sessions"`
	NextCursor string    `json:"nextCursor,omitempty"`
}

type Item struct {
	ID   string `json:"id"`
	Type string `json:"type"`
	Role string `json:"role"`
	Text string `json:"text"`
	// TextOffset is a UTF-8 byte offset used only in progressive history parts.
	// Assembled transcripts contain complete text and omit this field.
	TextOffset       *int         `json:"textOffset,omitempty"`
	Status           string       `json:"status,omitempty"`
	Phase            string       `json:"phase,omitempty"`
	ReasoningSummary string       `json:"reasoningSummary,omitempty"`
	Activities       []Activity   `json:"activities,omitempty"`
	DurationMS       *int64       `json:"durationMs,omitempty"`
	TimestampMS      *int64       `json:"timestampMs,omitempty"`
	TimestampSource  string       `json:"timestampSource,omitempty"`
	Attachments      []Attachment `json:"attachments,omitempty"`
}

// Attachment is a session-scoped resource, never a remotely supplied file path.
type Attachment struct {
	ID        string `json:"id"`
	Name      string `json:"name"`
	MIME      string `json:"mime"`
	Size      int64  `json:"size"`
	SHA256    string `json:"sha256,omitempty"`
	Kind      string `json:"kind"`
	State     string `json:"state"`
	Width     int    `json:"width,omitempty"`
	Height    int    `json:"height,omitempty"`
	Thumbnail bool   `json:"thumbnail,omitempty"`
	Source    string `json:"source,omitempty"`
	Inline    bool   `json:"inline,omitempty"`
}

type AttachmentParams struct {
	ID        string `json:"id"`
	Name      string `json:"name"`
	Size      int64  `json:"size"`
	SHA256    string `json:"sha256"`
	Offset    int64  `json:"offset"`
	Data      string `json:"data"`
	Thumbnail bool   `json:"thumbnail"`
}

// AttachmentSender is optional; providers map stored resources to native input.
type AttachmentSender interface {
	SendAttachments(context.Context, string, string, string, []string) error
	SteerAttachments(context.Context, string, string, string, []string) error
}

// Activity is presentation metadata, not a tool's raw output or model reasoning.
// Providers map their native tool actions into this common vocabulary.
type Activity struct {
	Kind   string `json:"kind"`
	Title  string `json:"title"`
	Detail string `json:"detail,omitempty"`
}

type Turn struct {
	RecencyAt int64  `json:"recencyAt,omitempty"`
	ID        string `json:"id"`
	Status    string `json:"status"`
	Items     []Item `json:"items"`
	// DurationMS is the Provider's recorded elapsed time; nil means unknown.
	DurationMS *int64 `json:"durationMs,omitempty"`
}

type Transcript struct {
	PartialTurn  string        `json:"partialTurn,omitempty"` // First turn begins partway through its item list.
	StreamEpoch  string        `json:"streamEpoch,omitempty"`
	StreamCursor int64         `json:"streamCursor,omitempty"`
	Session      Session       `json:"session"`
	Turns        []Turn        `json:"turns"`
	NextCursor   string        `json:"nextCursor,omitempty"`
	Queue        *MessageQueue `json:"queue,omitempty"`
}

// MessageQueue is the Agent's ordered, not-yet-consumed input, separate from
// transcript turns and the controller's unacknowledged outgoing messages.
// Unknown state must not be interpreted as an empty queue.
type MessageQueue struct {
	State    string          `json:"state"` // ready or unknown
	Messages []QueuedMessage `json:"messages"`
}

type QueuedMessage struct {
	ID          string             `json:"id"`
	Text        string             `json:"text"`
	Status      string             `json:"status"` // queued, preparing, paused, uncertain
	Attachments []QueuedAttachment `json:"attachments,omitempty"`
}

// QueuedAttachment is a preview only. Private composer paths and execution
// context remain in the Provider and are not downloadable attachment IDs.
type QueuedAttachment struct {
	Name string `json:"name"`
	Kind string `json:"kind"`
}

type CreateOptions struct {
	CWD   string `json:"cwd"`
	Model string `json:"model,omitempty"`
}

type Model struct {
	ImageInput bool   `json:"imageInput,omitempty"`
	ID         string `json:"id"`
	Name       string `json:"name"`
	Default    bool   `json:"default"`
}

// SessionModelProvider discovers models in the selected workspace/session.
// Agents may advertise different choices per session; do not borrow another
// session's list when composing a prompt.
type SessionModelProvider interface {
	SessionModels(context.Context, string) ([]Model, error)
}

// ManagedProvider owns a subprocess runtime and remembers explicit enablement.
type ManagedProvider interface{ Managed() bool }

// ModelProvider is optional: providers without model selection retain Send.
type ModelProvider interface {
	Models(context.Context) ([]Model, error)
	SendWithModel(context.Context, string, string, string) error
}

type Approval struct {
	ID         string          `json:"id"`
	ProviderID string          `json:"providerId"`
	SessionID  string          `json:"sessionId"`
	TurnID     string          `json:"turnId"`
	Method     string          `json:"method"`
	Params     json.RawMessage `json:"params"`
	CreatedAt  time.Time       `json:"createdAt"`
	Questions  []Question      `json:"questions,omitempty"`
	AllowSkip  bool            `json:"allowSkip,omitempty"`
}

// Question is an Agent-owned prompt. Answers are keyed by its opaque ID;
// selecting an option is a draft, never an implicit submission.
type Question struct {
	ID       string           `json:"id"`
	Header   string           `json:"header,omitempty"`
	Question string           `json:"question"`
	IsSecret bool             `json:"isSecret,omitempty"`
	Options  []QuestionOption `json:"options,omitempty"`
}

type QuestionOption struct {
	Label       string `json:"label"`
	Description string `json:"description,omitempty"`
}

type Decision struct {
	Decision string `json:"decision"`
	Answers  map[string]struct {
		Answers []string `json:"answers"`
	} `json:"answers,omitempty"`
}

type Provider interface {
	Status(context.Context) Status
	Start(context.Context) error
	Close() error
	Sessions(context.Context, string) (SessionPage, error)
	Transcript(context.Context, string, string) (Transcript, error)
	Create(context.Context, CreateOptions) (Session, error)
	Send(context.Context, string, string) error
	Steer(context.Context, string, string, string) error
	Interrupt(context.Context, string, string) error
	Approvals() []Approval
	Decide(context.Context, string, Decision) error
	// Expected contains the exact instances the user saw when confirming.
	ConnectDesktop(context.Context, []Instance, bool) error
}

// SessionApprovalProvider refreshes requests that the Agent exposes through
// session state before returning the current approvals for that session.
type SessionApprovalProvider interface {
	SessionApprovals(context.Context, string) ([]Approval, error)
}

// SessionRemover owns the Agent-specific removal semantics. It must reject busy
// sessions and never silently interrupt work or remove another session's data.
type SessionRemover interface {
	RemoveSession(context.Context, string) error
}

// SessionBusy is conservative for removal: waiting work still belongs to a turn.
func SessionBusy(s Session) bool {
	if s.ActiveTurnID != "" {
		return true
	}
	switch strings.ToLower(s.Status) {
	case "active", "running", "inprogress", "waiting", "waitingforinput", "waitingforapproval", "needsinput":
		return true
	}
	return false
}
