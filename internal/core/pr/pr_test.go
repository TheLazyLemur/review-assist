package pr_test

import (
	"context"
	"slices"
	"testing"

	"github.com/TheLazyLemur/review-assist/internal/core/pr"
)

func TestParseRefKeepsTheEnterpriseHost(t *testing.T) {
	// given
	// ... a PR URL on a GitHub Enterprise host
	ref := "https://ghe.example.com/acme/widgets/pull/41"

	// when
	// ... it is parsed
	repo, number, err := pr.ParseRef(ref)

	// then
	// ... platform, hostname, owner, repo and number are all kept
	if err != nil {
		t.Fatal(err)
	}
	want := pr.Repo{Platform: pr.GitHub, Hostname: "ghe.example.com", Owner: "acme", Name: "widgets"}
	if repo != want || number != 41 {
		t.Fatalf("want %+v #41, got %+v #%d", want, repo, number)
	}
}

func TestParseRefPutsBitbucketOrgOnTheBitbucketPlatform(t *testing.T) {
	want := pr.Repo{Platform: pr.Bitbucket, Hostname: "bitbucket.org", Owner: "acme", Name: "storefront"}
	for _, ref := range []string{
		"https://bitbucket.org/acme/storefront/pull-requests/12",
		"bitbucket.org/acme/storefront#12",
		"https://bitbucket.org/acme/storefront/pull/12",
	} {
		t.Run(ref, func(t *testing.T) {
			// given
			// ... a reference to a pull request on bitbucket.org

			// when
			// ... it is parsed
			repo, number, err := pr.ParseRef(ref)

			// then
			// ... it is on the Bitbucket platform, with the workspace as owner
			if err != nil {
				t.Fatal(err)
			}
			if repo != want || number != 12 {
				t.Fatalf("want %+v #12, got %+v #%d", want, repo, number)
			}
		})
	}
}

// recordingHost records posts. Other CodeHost methods are not reached.
type recordingHost struct {
	pr.CodeHost
	verdicts []pr.Decision
	comments []string
}

func (h *recordingHost) SubmitVerdict(_ context.Context, _ int, d pr.Decision, _ string) error {
	h.verdicts = append(h.verdicts, d)
	return nil
}

func (h *recordingHost) PostComment(_ context.Context, _ int, c pr.NewComment) error {
	h.comments = append(h.comments, c.Body)
	return nil
}

func TestVerdictOnYourOwnPRPostsAsAHeadedComment(t *testing.T) {
	// given
	// ... a PR opened by the viewer (logins differ only in case), and one opened by someone else
	host := &recordingHost{}
	svc := pr.NewService(host, pr.Repo{Platform: pr.GitHub, Hostname: "github.com", Owner: "o", Name: "r"})
	own := &pr.PR{Summary: pr.Summary{Number: 1, Author: "TheLazyLemur"}}
	other := &pr.PR{Summary: pr.Summary{Number: 2, Author: "someone"}}
	ctx := context.Background()

	// when
	// ... the viewer requests changes and approves (with and without a message) on their own PR, then requests changes on the other
	var asComment [4]bool
	var errs [4]error
	asComment[0], errs[0] = svc.SubmitVerdict(ctx, own, "thelazylemur", pr.RequestChanges, "fix the loop")
	asComment[1], errs[1] = svc.SubmitVerdict(ctx, own, "thelazylemur", pr.Approve, "ship it")
	asComment[2], errs[2] = svc.SubmitVerdict(ctx, own, "thelazylemur", pr.Approve, "")
	asComment[3], errs[3] = svc.SubmitVerdict(ctx, other, "thelazylemur", pr.RequestChanges, "fix")

	// then
	// ... every call succeeds, and only the own-PR verdicts turn into comments
	for i, err := range errs {
		if err != nil {
			t.Fatalf("call %d: %v", i, err)
		}
	}
	if asComment != [4]bool{true, true, true, false} {
		t.Errorf("posted as comment: %v", asComment)
	}

	// ... each comment opens with the verdict, and the other PR gets a real verdict
	want := []string{"**Changes requested:**\n\nfix the loop", "**Approved:**\n\nship it", "**Approved**"}
	if !slices.Equal(host.comments, want) {
		t.Errorf("comments: got %q", host.comments)
	}
	if !slices.Equal(host.verdicts, []pr.Decision{pr.RequestChanges}) {
		t.Errorf("verdicts: got %v", host.verdicts)
	}
}
