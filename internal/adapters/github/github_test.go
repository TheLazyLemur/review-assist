package github_test

import (
	"context"
	"encoding/json"
	"slices"
	"strings"
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

func TestFailedCommandErrorLeadsWithTheReason(t *testing.T) {
	// given
	// ... a command that fails with a reason on stderr after long arguments
	run := github.ExecRunner{Dir: t.TempDir()}
	long := strings.Repeat("x", 200)

	// when
	// ... it runs
	_, err := run.Run(context.Background(), nil, "sh", "-c", "echo 'Can not request changes on your own pull request' >&2; exit 1", long)

	// then
	// ... the error starts with the reason, so a status line cut to the terminal width still shows it
	if err == nil || !strings.HasPrefix(err.Error(), "Can not request changes on your own pull request") {
		t.Fatalf("got %v", err)
	}
	if strings.Contains(err.Error(), long) {
		t.Errorf("error repeats the full arguments: %v", err)
	}
}
