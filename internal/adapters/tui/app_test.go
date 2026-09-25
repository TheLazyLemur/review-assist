package tui

import (
	"strings"
	"testing"

	"github.com/charmbracelet/x/ansi"

	"github.com/TheLazyLemur/review-assist/internal/core/pr"
)

func TestHeaderShowsTheRemoteNextToTheRepository(t *testing.T) {
	scheduler := pr.Repo{Platform: pr.Bitbucket, Hostname: "bitbucket.org", Owner: "acme", Name: "scheduler"}
	cases := []struct {
		name   string
		remote string
		want   string
	}{
		{"a clone of the repository shows the remote picked", "acme", "review-assist bitbucket.org/acme/scheduler  remote acme"},
		{"no local clone shows the repository alone", "", "review-assist bitbucket.org/acme/scheduler"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			// given
			// ... the app on a repository, with the case's remote
			m := New(Deps{PRs: pr.NewService(&failingHost{}, scheduler), Remote: tc.remote})

			// when
			// ... the header is drawn
			header := m.headerView()

			// then
			// ... it reads the case's text
			if got := strings.TrimSpace(ansi.Strip(header)); got != tc.want {
				t.Errorf("want %q, got %q", tc.want, got)
			}
		})
	}
}
