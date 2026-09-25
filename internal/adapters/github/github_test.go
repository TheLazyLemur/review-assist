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

func TestAnchoredCommentPostsGitHubsPayloadToTheServer(t *testing.T) {
	// given
	// ... a client for an enterprise repo
	run := &recordingRunner{}
	c := github.NewClient(run, pr.Repo{Platform: pr.GitHub, Hostname: "ghe.example.com", Owner: "acme", Name: "widgets"})

	// when
	// ... a comment anchored to a range of head lines is posted
	err := c.PostComment(context.Background(), 41, pr.NewComment{
		Body: "nit", HeadSHA: "abc",
		Anchor: &pr.Anchor{Path: "a.go", Line: 12, Side: diff.Head, StartLine: 10, StartSide: diff.Head},
	})

	// then
	// ... gh api targets the enterprise server with GitHub's payload, sides spelled RIGHT
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

type cannedRunner struct{ out string }

func (r cannedRunner) Run(context.Context, []byte, string, ...string) ([]byte, error) {
	return []byte(r.out), nil
}

func TestPlatformsCountsHostnamesGhIsLoggedInToAsGitHub(t *testing.T) {
	// given
	// ... gh logged in to github.com and an enterprise server
	run := cannedRunner{out: `{"hosts":{"github.com":[{"state":"success","active":true,"host":"github.com"}],"ghe.example.com":[{"state":"success","active":true,"host":"ghe.example.com"}]}}`}
	platformOf := github.Platforms(context.Background(), run)

	// when
	// ... the enterprise server, bitbucket.org and an unknown hostname are looked up
	ghe, gheOK := platformOf("ghe.example.com")
	bitbucket, bitbucketOK := platformOf("bitbucket.org")
	_, gitlabOK := platformOf("gitlab.com")

	// then
	// ... the enterprise server is GitHub, bitbucket.org is Bitbucket, and the unknown one is on no code host
	if ghe != pr.GitHub || !gheOK {
		t.Errorf("ghe.example.com: got %q %v", ghe, gheOK)
	}
	if bitbucket != pr.Bitbucket || !bitbucketOK {
		t.Errorf("bitbucket.org: got %q %v", bitbucket, bitbucketOK)
	}
	if gitlabOK {
		t.Error("gitlab.com counted as a code host")
	}
}
