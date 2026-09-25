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
