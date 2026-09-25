package review

import (
	"context"
	"encoding/json"

	"github.com/TheLazyLemur/review-assist/internal/core/pr"
)

// Backend runs a whole task, so a backend may loop itself (a CLI agent) or
// be looped by its adapter (a Messages API). It must offer only task.Tools.
type Backend interface {
	// Run returns nil when the backend stops without finishing; callers
	// check task.Done.
	Run(ctx context.Context, task Task) error
}

type Task struct {
	Name     string
	System   string
	Prompt   string
	Tools    []Tool
	MaxTurns int
	// FinishTool is named in reminders to a model that stops without it.
	FinishTool string
	Done       func() bool
}

type Tool struct {
	Name        string
	Description string
	Properties  map[string]any
	Required    []string
	// An error from Call goes back to the model as a failed call.
	Call func(ctx context.Context, input json.RawMessage) (string, error)
}

type CodeSource interface {
	// Open fetches the commits where it can. A missing commit is not an
	// error; Code.HasCommit reports it.
	Open(ctx context.Context, repo pr.Repo, number int, headSHA, baseSHA string) (Code, error)
}

// Code is read-only by design: agents reach it only through tools.go.
type Code interface {
	Location() string
	HasCommit(ctx context.Context, sha string) bool
	ReadFile(ctx context.Context, sha, path string) ([]byte, error)
	// Directory names end in "/".
	ListDir(ctx context.Context, sha, path string) ([]string, error)
	// No match is an empty result, not an error.
	Grep(ctx context.Context, sha, pattern, path string, ignoreCase bool) ([]string, error)
	Log(ctx context.Context, sha, path string, limit int) (string, error)
}
