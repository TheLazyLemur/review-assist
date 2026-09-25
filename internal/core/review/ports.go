package review

import (
	"context"
	"encoding/json"

	"github.com/TheLazyLemur/review-assist/internal/core/pr"
)

// Model is the port to the language model the agents run on.
type Model interface {
	// Start opens a conversation. label names the agent (for logs only).
	Start(label, system string, tools []ToolSpec) Conversation
}

// Conversation keeps its own history; the core only sends the next user turn.
type Conversation interface {
	// Send adds one user turn (tool results first, then text) and returns
	// the model's reply.
	Send(ctx context.Context, in Input) (Reply, error)
}

type ToolSpec struct {
	Name        string
	Description string
	Properties  map[string]any // JSON Schema properties of the input object
	Required    []string
}

type Input struct {
	Results []ToolResult
	Text    []string
}

type ToolResult struct {
	CallID  string
	Content string
	IsError bool
}

type Reply struct {
	Calls     []ToolCall
	Truncated bool // the model hit its output limit
}

type ToolCall struct {
	ID    string
	Name  string
	Input json.RawMessage
}

// CodeSource is the port that makes a PR's code readable.
type CodeSource interface {
	// Open returns the repository with the PR's head and base commits
	// fetched where possible. A commit that cannot be fetched is not an
	// error: Code.HasCommit reports it.
	Open(ctx context.Context, repo pr.Repo, number int, headSHA, baseSHA string) (Code, error)
}

// Code is read-only access to a repository at fixed commits. It has no write
// operations, and the agents reach it only through the tools in tools.go.
type Code interface {
	Location() string
	HasCommit(ctx context.Context, sha string) bool
	ReadFile(ctx context.Context, sha, path string) ([]byte, error)
	// ListDir returns entry names; directories end in "/".
	ListDir(ctx context.Context, sha, path string) ([]string, error)
	// Grep returns "path:line:text" matches, or none without error.
	Grep(ctx context.Context, sha, pattern, path string, ignoreCase bool) ([]string, error)
	// Log returns one line per commit, newest first.
	Log(ctx context.Context, sha, path string, limit int) (string, error)
}
