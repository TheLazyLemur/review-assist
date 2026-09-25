package review

import (
	"context"
	"encoding/json"

	"github.com/TheLazyLemur/review-assist/internal/core/pr"
)

// Agent is the port to whatever runs an agent loop: a Messages API backend
// that the adapter loops over, or a CLI agent such as Claude Code that loops
// itself. The backend calls the task's tools; it must offer no others.
type Agent interface {
	// Run drives the task until task.Done reports true, the backend gives up,
	// or MaxTurns model round trips have passed. Not finishing is not an error
	// here: the caller checks Done.
	Run(ctx context.Context, task Task) error
}

type Task struct {
	Name     string // names the agent in logs
	System   string
	Prompt   string
	Tools    []Tool
	MaxTurns int
	// FinishTool is the tool that ends the task. Backends use it to remind a
	// model that stops without calling it.
	FinishTool string
	Done       func() bool
}

type Tool struct {
	Name        string
	Description string
	Properties  map[string]any // JSON Schema properties of the input object
	Required    []string
	// Call runs the tool. An error is shown to the model as a failed call.
	Call func(ctx context.Context, input json.RawMessage) (string, error)
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
