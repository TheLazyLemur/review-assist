package review

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"
	"strings"
	"sync"

	"github.com/TheLazyLemur/review-assist/internal/core/diff"
	"github.com/TheLazyLemur/review-assist/internal/core/pr"
)

// Finding is a suggestion for where the human might comment. Agents never
// post anything; the human decides whether to turn a finding into a comment.
type Finding struct {
	Path             string `json:"path"`
	Line             int    `json:"line"`
	Side             string `json:"side"`
	Severity         string `json:"severity"`
	Confidence       string `json:"confidence"`
	Lens             string `json:"lens"`
	Title            string `json:"title"`
	Explanation      string `json:"explanation"`
	SuggestedComment string `json:"suggested_comment"`

	// Anchored is set by the orchestrator: true when Path/Side/Line is a line
	// of the diff, so an inline comment can be placed there.
	Anchored bool `json:"-"`
}

type Result struct {
	Level    Level
	Verdict  string
	Findings []Finding
	Failures []string // agents that failed; their scope is unreviewed
	Notes    []string
}

// Reviewer runs agent reviews. It depends only on its ports, and neither port
// can post to the code host: agents suggest, the human posts.
type Reviewer struct {
	agent       Agent
	code        CodeSource
	maxTurns    int
	concurrency int
}

func NewReviewer(agent Agent, code CodeSource, maxTurns, concurrency int) *Reviewer {
	if agent == nil || code == nil || maxTurns < 4 || concurrency < 1 {
		panic(fmt.Sprintf("review.NewReviewer: bad wiring (agent=%v code=%v maxTurns=%d concurrency=%d)",
			agent != nil, code != nil, maxTurns, concurrency))
	}
	return &Reviewer{agent: agent, code: code, maxTurns: maxTurns, concurrency: concurrency}
}

// Review opens the PR's code and runs the review at the given level.
func (r *Reviewer) Review(ctx context.Context, repo pr.Repo, d *pr.Details, level Level, emit func(Event)) (Result, error) {
	const setup = "Setup"
	emit(Event{Agent: setup, Kind: EventStarted, Detail: "fetching PR commits"})
	code, err := r.code.Open(ctx, repo, d.PR.Number, d.PR.HeadSHA, d.PR.BaseSHA)
	if err != nil {
		emit(Event{Agent: setup, Kind: EventFailed, Detail: err.Error()})
		return Result{Level: level}, err
	}
	ws := newWorkspace(ctx, code, d)
	detail := "repo: " + code.Location()
	if dg := ws.Degraded(); dg != "" {
		detail = dg
	}
	emit(Event{Agent: setup, Kind: EventDone, Detail: detail})
	return r.run(ctx, ws, level, emit)
}

// run fans the review out across specialists, then (above LevelQuick) has a
// verifier dedupe, re-check and rank their findings.
func (r *Reviewer) run(ctx context.Context, ws *Workspace, level Level, emit func(Event)) (Result, error) {
	units := Plan(level, ws.Files, ws.HasRules)
	if len(units) == 0 {
		return Result{}, fmt.Errorf("nothing to review: the PR has no changed files")
	}
	res := Result{Level: level}
	if d := ws.Degraded(); d != "" {
		res.Notes = append(res.Notes, d)
	}

	var (
		mu    sync.Mutex
		union []Finding
		wg    sync.WaitGroup
		sem   = make(chan struct{}, r.concurrency)
	)
	for _, u := range units {
		wg.Add(1)
		go func(u Unit) {
			defer wg.Done()
			select {
			case sem <- struct{}{}:
			case <-ctx.Done():
				return
			}
			defer func() { <-sem }()

			name := u.Name()
			emit(Event{Agent: name, Kind: EventStarted})
			sub, err := r.runAgent(ctx, ws, name, specialistSystem, specialistPrompt(ws, u), emit)
			mu.Lock()
			defer mu.Unlock()
			if err != nil {
				res.Failures = append(res.Failures, fmt.Sprintf("%s: %v", name, err))
				emit(Event{Agent: name, Kind: EventFailed, Detail: err.Error()})
				return
			}
			for i := range sub.Findings {
				if sub.Findings[i].Lens == "" {
					sub.Findings[i].Lens = string(u.Lens.ID)
				}
			}
			union = append(union, sub.Findings...)
			emit(Event{Agent: name, Kind: EventDone, Detail: fmt.Sprintf("%d findings", len(sub.Findings))})
		}(u)
	}
	wg.Wait()
	if err := ctx.Err(); err != nil {
		return res, err
	}
	if len(res.Failures) == len(units) {
		return res, fmt.Errorf("every agent failed:\n%s", strings.Join(res.Failures, "\n"))
	}

	final := union
	if level.HasVerifier() && len(union) > 0 {
		const name = "Verifier"
		emit(Event{Agent: name, Kind: EventStarted})
		sub, err := r.runAgent(ctx, ws, name, verifierSystem, verifierPrompt(ws, units, union), emit)
		if err != nil {
			// Keep the unverified union rather than losing the work, but say so.
			res.Failures = append(res.Failures, fmt.Sprintf("%s: %v", name, err))
			res.Notes = append(res.Notes, "verifier failed: findings below are unverified")
			emit(Event{Agent: name, Kind: EventFailed, Detail: err.Error()})
		} else {
			final = sub.Findings
			res.Verdict = sub.Verdict
			emit(Event{Agent: name, Kind: EventDone, Detail: fmt.Sprintf("%d findings kept of %d", len(final), len(union))})
		}
	}

	for i := range final {
		f := &final[i]
		f.Side = strings.ToUpper(f.Side)
		if f.Side != string(diff.Left) {
			f.Side = string(diff.Right)
		}
		f.Anchored = ws.Anchored(f.Path, diff.Side(f.Side), f.Line)
	}
	rank(final)
	res.Findings = final
	return res, nil
}

var severityRank = map[string]int{"blocking": 0, "major": 1, "minor": 2}
var confidenceRank = map[string]int{"high": 0, "medium": 1, "low": 2}

func rank(fs []Finding) {
	sort.SliceStable(fs, func(i, j int) bool {
		a, b := fs[i], fs[j]
		if rankOf(severityRank, a.Severity) != rankOf(severityRank, b.Severity) {
			return rankOf(severityRank, a.Severity) < rankOf(severityRank, b.Severity)
		}
		return rankOf(confidenceRank, a.Confidence) < rankOf(confidenceRank, b.Confidence)
	})
}

func rankOf(m map[string]int, k string) int {
	if v, ok := m[k]; ok {
		return v
	}
	return len(m)
}

const specialistSystem = `You are a Specialist Reviewer helping a human review a pull request.
You review exactly ONE scope through exactly ONE lens, both given in the task. Be thorough and adversarial within your lens, silent outside it.

You are strictly read-only. You cannot and must not post, edit, approve or comment on anything. Your output is private advice to the human reviewer, who decides what to post.

How to work:
- Start with get_diff for your scope. Then explore: read_file for surrounding code at head, read_file with ref=base for the old version, grep for callers/callees and related tests, list_dir, git_log.
- For every checklist item, ask: under what input, state or timing does this code do the wrong thing, and is that case handled or proven impossible? Trace concrete scenarios.
- Confirm the other side of any boundary (callers, called functions, tests) before asserting. If you cannot construct a concrete failing scenario it is NOT a finding. Do not pad; no style nits.
- Anchor every finding to a line of the diff using the R<n>/L<n> anchors from get_diff (R -> side RIGHT, L -> side LEFT). Pick the single most relevant changed line.
- title: one short line. explanation: 2-4 plain sentences a busy reviewer can digest in seconds: the scenario, then the consequence. suggested_comment: a short, polite draft the human could post, with a fix direction.

Finish by calling submit_findings exactly once (an empty list is fine). Never answer in prose.`

func specialistPrompt(ws *Workspace, u Unit) string {
	return fmt.Sprintf(`PR: %s

Scope (files you own):
%s

Boundary: other changed files and other lenses are owned by other reviewers. You may READ anything for context, but only report findings in your scope through your lens.

Lens: %s %s
Checklist:
%s

Changed files in the whole PR:
%s`, ws.Title, bullet(u.Files), u.Lens.ID, u.Lens.Name, u.Lens.Checklist, ws.listChanged())
}

const verifierSystem = `You are the Verifier for a multi-agent pull request review. You are the gate that keeps false positives away from a busy human reviewer.

You are strictly read-only. You cannot and must not post, edit, approve or comment on anything. Your output is private advice to the human.

Process:
1. Dedupe: merge findings describing the same defect, even across lenses. Keep the clearest framing.
2. Verify: re-check each finding against the real code with the tools (get_diff, read_file, grep). Confirm the triggering scenario is real and reachable. If you cannot reproduce it, downgrade confidence or drop it.
3. Fix anchors: each finding must point at a line of the diff (R<n> -> RIGHT, L<n> -> LEFT from get_diff).
4. Classify severity: blocking (wrong results, data loss, security hole, reachable crash, broken compatibility), major (real bug on an uncommon path, or missing test for risky behaviour), minor (robustness nit).
5. Keep explanations short and plain: 2-4 sentences, scenario then consequence. Keep suggested_comment polite and specific.

Finish by calling submit_findings once with the final findings and a 2-3 line verdict (mergeable? if not, blocked on what?). Never answer in prose.`

func verifierPrompt(ws *Workspace, units []Unit, union []Finding) string {
	js, _ := json.MarshalIndent(union, "", "  ")
	var scopes []string
	for _, u := range units {
		scopes = append(scopes, u.Name())
	}
	return fmt.Sprintf(`Intent: the PR is titled %q. Call pr_description for the author's description; if the diff contradicts the stated intent, that is a finding.

Specialists that ran:
%s

Union of specialist findings (unverified):
%s`, ws.Title, bullet(scopes), js)
}

func bullet(items []string) string {
	var b strings.Builder
	for _, it := range items {
		b.WriteString("- " + it + "\n")
	}
	return b.String()
}
