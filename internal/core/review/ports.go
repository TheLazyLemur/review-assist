package review

import (
	"context"
	"encoding/json"
	"slices"

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
	// Open fetches the pull request's head and base commits where it can. A
	// missing commit is not an error; Code.HasCommit reports it.
	Open(ctx context.Context, repo pr.Repo, p *pr.PR) (Code, error)
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

// Effort is how hard each model reasons. Empty leaves it to the backend.
type Effort string

const (
	EffortNone   Effort = "none"
	EffortLow    Effort = "low"
	EffortMedium Effort = "medium"
	EffortHigh   Effort = "high"
	EffortXhigh  Effort = "xhigh"
	EffortMax    Effort = "max"
)

var Efforts = []Effort{EffortNone, EffortLow, EffortMedium, EffortHigh, EffortXhigh, EffortMax}

func (e Effort) Valid() bool { return e == "" || slices.Contains(Efforts, e) }
