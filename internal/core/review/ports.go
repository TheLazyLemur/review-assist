package review

import (
	"context"
	"encoding/json"
	"slices"

	"github.com/TheLazyLemur/review-assist/internal/core/diff"
	"github.com/TheLazyLemur/review-assist/internal/core/pr"
)

// Backend runs a whole agent run, so a backend may loop itself (a CLI agent) or
// be looped by its adapter (a Messages API). It must offer only run.Tools.
type Backend interface {
	// Run returns nil when the backend stops without finishing; callers
	// check run.Done.
	Run(ctx context.Context, run AgentRun) error
}

type AgentRun struct {
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

// Judge answers typed questions about one changed file, each with a
// probability. It judges; the rules in readingorder.go decide what follows.
type Judge interface {
	JudgeFile(ctx context.Context, title, description string, f diff.File) (FileJudgement, error)
}

// ChangeKind is what a file's diff does.
type ChangeKind string

const (
	KindBehaviour  ChangeKind = "behaviour_change"
	KindRefactor   ChangeKind = "refactor"
	KindTest       ChangeKind = "test"
	KindConfig     ChangeKind = "config"
	KindDocs       ChangeKind = "docs"
	KindFormatting ChangeKind = "formatting"
	KindGenerated  ChangeKind = "generated"
)

var ChangeKinds = []ChangeKind{KindBehaviour, KindRefactor, KindTest, KindConfig, KindDocs, KindFormatting, KindGenerated}

// FileJudgement is a judge's answers about one changed file. The numbers are
// the probability that each statement is true.
type FileJudgement struct {
	Kind      ChangeKind
	Central   float64 // the file is central to what the description says the PR does
	Contract  float64 // the diff changes something callers depend on: an API, a schema, a contract
	Described float64 // the change is mentioned or implied by the description
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
