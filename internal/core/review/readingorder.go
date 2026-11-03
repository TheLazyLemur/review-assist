package review

import (
	"cmp"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strings"
	"sync"

	"golang.org/x/sync/errgroup"

	"github.com/TheLazyLemur/review-assist/internal/core/pr"
)

// Tier is how much of a reviewer's attention a changed file asks for.
type Tier string

const (
	TierRead Tier = "read"
	TierSkim Tier = "skim"
	TierSkip Tier = "skip"
)

var tierRank = map[Tier]int{TierRead: 0, TierSkim: 1, TierSkip: 2}

type ReadingStep struct {
	Path   string `json:"path"`
	Tier   Tier   `json:"tier"`
	Reason string `json:"reason"`
}

// ReadingOrder holds every changed file of a pull request once, in the order
// to read them: tier by tier, read first.
type ReadingOrder struct {
	Steps []ReadingStep
	Notes []string
}

// ReadingOrder suggests an order to read the PR's changed files in. With a
// judge, Jev answers questions about each file and the rules below order
// them. Without one, an agent orders them.
func (r *Service) ReadingOrder(ctx context.Context, repo pr.Repo, d *pr.Details) (ReadingOrder, error) {
	if len(d.Files) == 0 {
		return ReadingOrder{}, errors.New("nothing to order: the PR has no changed files")
	}
	if r.judge != nil {
		return r.judgedOrder(ctx, d)
	}
	code, err := r.code.Open(ctx, repo, d.PR)
	if err != nil {
		return ReadingOrder{}, err
	}
	return r.agentOrder(ctx, newSubject(ctx, code, d))
}

// judgeConcurrency bounds requests to the judge; each is one file.
const judgeConcurrency = 8

func (r *Service) judgedOrder(ctx context.Context, d *pr.Details) (ReadingOrder, error) {
	steps := make([]ReadingStep, len(d.Files))
	central := make([]float64, len(d.Files))
	g, gctx := errgroup.WithContext(ctx)
	g.SetLimit(judgeConcurrency)
	for i, f := range d.Files {
		if f.Binary {
			steps[i] = ReadingStep{Path: f.Path(), Tier: TierSkip, Reason: "binary file"}
			continue
		}
		g.Go(func() error {
			j, err := r.judge.JudgeFile(gctx, d.PR.Title, d.PR.Body, f)
			if err != nil {
				// A missing answer is not a "no": refuse to order without it.
				return fmt.Errorf("judge %s: %w", f.Path(), err)
			}
			steps[i], central[i] = stepFor(f.Path(), j), j.Central
			return nil
		})
	}
	if err := g.Wait(); err != nil {
		return ReadingOrder{}, err
	}
	idx := make([]int, len(steps))
	for i := range idx {
		idx[i] = i
	}
	slices.SortStableFunc(idx, func(a, b int) int {
		return cmp.Or(tierRank[steps[a].Tier]-tierRank[steps[b].Tier], cmp.Compare(central[b], central[a]))
	})
	out := ReadingOrder{Steps: make([]ReadingStep, len(idx))}
	for i, k := range idx {
		out.Steps[i] = steps[k]
	}
	return out, nil
}

// A probability at or above yes reads as true, at or below no as false; the
// band between is unsettled and never sends a file to skip.
const (
	yes = 0.7
	no  = 0.3
)

var kindLabel = map[ChangeKind]string{
	KindBehaviour:  "behaviour change",
	KindRefactor:   "refactor",
	KindTest:       "test",
	KindConfig:     "config",
	KindDocs:       "docs",
	KindFormatting: "formatting",
	KindGenerated:  "generated",
}

// stepFor is the table from a file's judgement to its tier and reason.
func stepFor(path string, j FileJudgement) ReadingStep {
	label, ok := kindLabel[j.Kind]
	if !ok {
		panic(fmt.Sprintf("review: judge returned unknown change kind %q for %s", j.Kind, path))
	}
	central, contract := j.Central >= yes, j.Contract >= yes
	// Only a change that matters can be missing from the description.
	undescribed := j.Described <= no && (j.Kind == KindBehaviour || j.Kind == KindConfig || contract)

	why := []string{label}
	if j.Kind == KindGenerated || j.Kind == KindFormatting {
		return ReadingStep{Path: path, Tier: TierSkip, Reason: label}
	}
	if central {
		why = append(why, "central to the PR")
	}
	if contract {
		why = append(why, "changes what callers rely on")
	}
	if undescribed {
		why = append(why, "not in the description")
	}

	tier := TierSkim
	switch {
	case contract, j.Kind == KindBehaviour && (central || undescribed):
		tier = TierRead
	case j.Kind == KindBehaviour, j.Kind == KindConfig, central:
		tier = TierSkim
	case j.Central <= no:
		tier = TierSkip
	}
	return ReadingStep{Path: path, Tier: tier, Reason: strings.Join(why, " · ")}
}

var orderTool = toolSpec{
	Name: "submit_reading_order",
	Description: "Submit the order to read the PR's changed files in and end your work. List every changed file exactly once, " +
		"the one to read first first. A rejected order says which file it got wrong; fix it and call again.",
	Properties: map[string]any{
		"steps": map[string]any{
			"type": "array",
			"items": map[string]any{
				"type": "object",
				"properties": map[string]any{
					"path": map[string]any{"type": "string", "description": "changed file path, as list_changed_files gives it"},
					"tier": map[string]any{"type": "string", "enum": []string{string(TierRead), string(TierSkim), string(TierSkip)},
						"description": "read: needs a careful read; skim: a glance is enough; skip: generated, formatting or noise"},
					"reason": map[string]any{"type": "string", "description": "one short line: why this file sits here"},
				},
				"required": []string{"path", "tier", "reason"},
			},
		},
	},
	Required: []string{"steps"},
}

const orderSystem = `You help a human review a pull request by saying what order to read its changed files in, so they do not have to read every line.
You are strictly read-only.

Start with pr_description and list_changed_files, then get_diff for the files you need to understand. Put first the files that carry the PR's point and the changes callers depend on, then the tests that cover them, then supporting changes. Mark generated files, formatting and pure renames skip.

Finish by calling submit_reading_order with every changed file exactly once. Never answer in prose.`

func (r *Service) agentOrder(ctx context.Context, subject *Subject) (ReadingOrder, error) {
	const name = "Reading order"
	var (
		mu   sync.Mutex
		done []ReadingStep
	)
	tools := append(subject.readTools(name, func(Event) {}), Tool{
		Name: orderTool.Name, Description: orderTool.Description, Properties: orderTool.Properties, Required: orderTool.Required,
		Call: func(_ context.Context, input json.RawMessage) (string, error) {
			var in struct {
				Steps []ReadingStep `json:"steps"`
			}
			if err := json.Unmarshal(input, &in); err != nil {
				return "", fmt.Errorf("invalid reading order JSON: %v; call %s again", err, orderTool.Name)
			}
			if err := subject.checkOrder(in.Steps); err != nil {
				return "", fmt.Errorf("%v; call %s again with every changed file once", err, orderTool.Name)
			}
			mu.Lock()
			defer mu.Unlock()
			done = in.Steps
			return "Reading order recorded. You are done: stop now.", nil
		},
	})
	err := r.backend.Run(ctx, AgentRun{
		Name: name, System: orderSystem, Prompt: "PR: " + subject.Title, Tools: tools, MaxTurns: r.maxTurns,
		FinishTool: orderTool.Name,
		Done: func() bool {
			mu.Lock()
			defer mu.Unlock()
			return done != nil
		},
	})
	mu.Lock()
	defer mu.Unlock()
	if done == nil {
		if err != nil {
			return ReadingOrder{}, err
		}
		return ReadingOrder{}, errors.New("the agent finished without a valid reading order")
	}
	slices.SortStableFunc(done, func(a, b ReadingStep) int { return tierRank[a.Tier] - tierRank[b.Tier] })
	out := ReadingOrder{Steps: done}
	if dg := subject.Degraded(); dg != "" {
		out.Notes = append(out.Notes, dg)
	}
	return out, nil
}

// checkOrder names the first file an order misses, invents or repeats.
func (w *Subject) checkOrder(steps []ReadingStep) error {
	seen := map[string]bool{}
	for _, s := range steps {
		if _, ok := w.file(s.Path); !ok {
			return fmt.Errorf("%s is not a changed file", s.Path)
		}
		if seen[s.Path] {
			return fmt.Errorf("the order lists %s twice", s.Path)
		}
		if _, ok := tierRank[s.Tier]; !ok {
			return fmt.Errorf("%s has tier %q; use read, skim or skip", s.Path, s.Tier)
		}
		seen[s.Path] = true
	}
	for _, f := range w.Files {
		if !seen[f.Path()] {
			return fmt.Errorf("the order leaves out %s", f.Path())
		}
	}
	return nil
}
