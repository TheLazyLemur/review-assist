// Package typesafe is the Jev judge: it implements review.Judge with
// TypeSafe's System One API, one request per changed file. Every question
// Jev is asked is worded here.
package typesafe

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"slices"
	"strings"
	"time"

	"github.com/TheLazyLemur/review-assist/internal/core/diff"
	"github.com/TheLazyLemur/review-assist/internal/core/review"
)

const (
	model = "jev-latest"
	// maxDiff keeps one file's state well inside Jev's 32k-token context per
	// question. A diff that long is usually generated, which the kind question
	// still tells apart from its head.
	maxDiff = 60_000
)

type Config struct {
	APIKey  string
	BaseURL string // https://api.typesafe.ai in production
}

type Judge struct {
	cfg    Config
	client *http.Client
}

var _ review.Judge = (*Judge)(nil)

func New(cfg Config) *Judge {
	if cfg.APIKey == "" || cfg.BaseURL == "" {
		panic(fmt.Sprintf("typesafe.New: an API key and a base URL are required (url=%q key set=%v)", cfg.BaseURL, cfg.APIKey != ""))
	}
	return &Judge{cfg: cfg, client: &http.Client{Timeout: 60 * time.Second}}
}

var kindCriteria = map[review.ChangeKind]string{
	review.KindBehaviour:  "The diff changes what the code does when it runs: new logic, a fixed bug, changed rules or outputs.",
	review.KindRefactor:   "The diff restructures, moves or renames code without changing what it does.",
	review.KindTest:       "The diff adds or changes tests, test fixtures or test helpers.",
	review.KindConfig:     "The diff changes configuration, build scripts, CI pipelines or dependency versions.",
	review.KindDocs:       "The diff changes documentation or code comments only.",
	review.KindFormatting: "The diff changes whitespace, import order or code style only.",
	review.KindGenerated:  "The file is written by a tool, such as an API client generated from a schema, a lockfile or a migration snapshot.",
}

var questions = map[string]any{
	"kind": map[string]any{
		"type":         "choice",
		"instructions": "What kind of change does the diff in `file` make?",
		"criteria":     kindCriteria,
	},
	"central": map[string]any{
		"type":         "noul",
		"instructions": "Is the diff in `file` part of the change `pull_request` is about?",
		"criteria": map[string]string{
			"true":  "The diff makes the change the pull request is about, or tests that change.",
			"false": "The diff is a side change the pull request's point does not need, such as a cleanup or rename elsewhere.",
		},
	},
	"contract": map[string]any{
		"type":         "noul",
		"instructions": "Does the diff in `file` change something other code or users depend on?",
		"criteria": map[string]string{
			"true":  "It changes a public function or type signature, an API route or payload, a database schema, a message format or a config key.",
			"false": "Its changes stay inside the file's own implementation, where no caller can see them.",
		},
	},
	"described": map[string]any{
		"type":         "noul",
		"instructions": "Does `pull_request.description` mention or imply the change in `file`?",
		"criteria": map[string]string{
			"true":  "The description names this change, or the behaviour this change alters.",
			"false": "The description is about other changes and says nothing that covers this one.",
		},
	},
}

func (j *Judge) JudgeFile(ctx context.Context, title, description string, f diff.File) (review.FileJudgement, error) {
	if description == "" {
		description = "(the author wrote no description)"
	}
	body, err := json.Marshal(map[string]any{
		"model": model,
		"state": map[string]any{
			"pull_request": map[string]string{"title": title, "description": description},
			"file":         map[string]string{"path": f.Path(), "status": status(f), "diff": render(f)},
		},
		"questions": questions,
	})
	if err != nil {
		return review.FileJudgement{}, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, strings.TrimRight(j.cfg.BaseURL, "/")+"/v1/systemone", bytes.NewReader(body))
	if err != nil {
		return review.FileJudgement{}, err
	}
	req.Header.Set("Authorization", "Bearer "+j.cfg.APIKey)
	req.Header.Set("Content-Type", "application/json")
	resp, err := j.client.Do(req)
	if err != nil {
		return review.FileJudgement{}, fmt.Errorf("typesafe: %w", err)
	}
	defer resp.Body.Close()
	raw, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return review.FileJudgement{}, fmt.Errorf("typesafe: %w", err)
	}
	if resp.StatusCode != http.StatusOK {
		return review.FileJudgement{}, fmt.Errorf("typesafe: %s: %s", resp.Status, bytes.TrimSpace(raw))
	}
	return parse(raw)
}

func parse(raw []byte) (review.FileJudgement, error) {
	var got struct {
		Answers map[string]struct {
			Choice string   `json:"choice"`
			Noul   *float64 `json:"noul"`
		} `json:"answers"`
	}
	if err := json.Unmarshal(raw, &got); err != nil {
		return review.FileJudgement{}, fmt.Errorf("typesafe: decode answers: %w", err)
	}
	kind := review.ChangeKind(got.Answers["kind"].Choice)
	if !slices.Contains(review.ChangeKinds, kind) {
		return review.FileJudgement{}, fmt.Errorf("typesafe: kind answer %q is not one of the options", kind)
	}
	j := review.FileJudgement{Kind: kind}
	for name, dst := range map[string]*float64{"central": &j.Central, "contract": &j.Contract, "described": &j.Described} {
		n := got.Answers[name].Noul
		if n == nil {
			return review.FileJudgement{}, fmt.Errorf("typesafe: no answer to %s", name)
		}
		*dst = *n
	}
	return j, nil
}

func status(f diff.File) string {
	switch {
	case f.Deleted:
		return "deleted"
	case f.Added:
		return "added"
	case f.OldPath != "" && f.OldPath != f.NewPath:
		return "renamed from " + f.OldPath
	}
	return "modified"
}

// render is the plain unified diff: Jev needs no line anchors.
func render(f diff.File) string {
	var b strings.Builder
	for _, h := range f.Hunks {
		b.WriteString(h.Header + "\n")
		for _, l := range h.Lines {
			b.WriteString(map[diff.Kind]string{diff.Context: " ", diff.Add: "+", diff.Del: "-"}[l.Kind] + l.Text + "\n")
		}
		if b.Len() > maxDiff {
			return b.String()[:maxDiff] + "\n… (diff truncated)"
		}
	}
	return b.String()
}
