package pr_test

import (
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
	// ... host, owner, repo and number are all kept
	if err != nil {
		t.Fatal(err)
	}
	want := pr.Repo{Host: "ghe.example.com", Owner: "acme", Name: "widgets"}
	if repo != want || number != 41 {
		t.Fatalf("want %+v #41, got %+v #%d", want, repo, number)
	}
}

func TestReviewOnYourOwnPRBecomesAMarkedCommentReview(t *testing.T) {
	// given
	// ... a PR opened by the viewer (logins differ only in case)
	author, viewer := "TheLazyLemur", "thelazylemur"

	// when
	// ... the viewer requests changes, approves, and approves with no message
	changes, changesBody := pr.ReviewFor(author, viewer, pr.RequestChanges, "fix the loop")
	approve, approveBody := pr.ReviewFor(author, viewer, pr.Approve, "ship it")
	bare, bareBody := pr.ReviewFor(author, viewer, pr.Approve, "")

	// then
	// ... each becomes a comment review whose body opens with the intended verdict
	if changes != pr.CommentReview || changesBody != "**Changes requested:**\n\nfix the loop" {
		t.Errorf("request changes: got %s %q", changes, changesBody)
	}
	if approve != pr.CommentReview || approveBody != "**Approved:**\n\nship it" {
		t.Errorf("approve: got %s %q", approve, approveBody)
	}
	if bare != pr.CommentReview || bareBody != "**Approved**" {
		t.Errorf("bare approve: got %s %q", bare, bareBody)
	}

	// ... and a review on someone else's PR is unchanged
	if e, b := pr.ReviewFor("someone", viewer, pr.RequestChanges, "fix"); e != pr.RequestChanges || b != "fix" {
		t.Errorf("other author: got %s %q", e, b)
	}
}
