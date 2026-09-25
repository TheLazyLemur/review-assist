package github_test

import (
	"context"
	"encoding/json"
	"slices"
	"testing"

	"github.com/TheLazyLemur/review-assist/internal/adapters/github"
	"github.com/TheLazyLemur/review-assist/internal/core/diff"
	"github.com/TheLazyLemur/review-assist/internal/core/pr"
)

type recordingRunner struct {
	args  []string
	stdin []byte
}

func (r *recordingRunner) Run(_ context.Context, stdin []byte, _ string, args ...string) ([]byte, error) {
	r.args, r.stdin = args, stdin
	return nil, nil
}

func TestInlineCommentPostsAnchoredPayloadToTheRepoHost(t *testing.T) {
	// given
	// ... a client for an enterprise repo
	run := &recordingRunner{}
	c := github.NewClient(run, pr.Repo{Host: "ghe.example.com", Owner: "acme", Name: "widgets"})

	// when
	// ... a multi-line inline comment is added
	err := c.AddInlineComment(context.Background(), 41, pr.InlineComment{
		Body: "nit", CommitSHA: "abc", Path: "a.go", Line: 12, Side: diff.Right, StartLine: 10, StartSide: diff.Right,
	})

	// then
	// ... gh api targets the enterprise host and the pull comments endpoint with the JSON payload
	if err != nil {
		t.Fatal(err)
	}
	wantArgs := []string{"api", "--hostname", "ghe.example.com", "--method", "POST", "repos/acme/widgets/pulls/41/comments", "--input", "-"}
	if !slices.Equal(run.args, wantArgs) {
		t.Fatalf("args: want %v, got %v", wantArgs, run.args)
	}
	var got map[string]any
	if err := json.Unmarshal(run.stdin, &got); err != nil {
		t.Fatal(err)
	}
	want := map[string]any{"body": "nit", "commit_id": "abc", "path": "a.go", "line": 12.0, "side": "RIGHT", "start_line": 10.0, "start_side": "RIGHT"}
	for k, v := range want {
		if got[k] != v {
			t.Errorf("payload %s: want %v, got %v", k, v, got[k])
		}
	}
}
