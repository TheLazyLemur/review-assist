package gitrepo_test

import (
	"errors"
	"testing"

	"github.com/TheLazyLemur/review-assist/internal/adapters/gitrepo"
	"github.com/TheLazyLemur/review-assist/internal/core/pr"
)

type remote struct{ name, hostname, path string }

var urlForms = []struct {
	name string
	url  func(hostname, path string) string
}{
	{"ssh", func(h, p string) string { return "git@" + h + ":" + p + ".git" }},
	{"https", func(h, p string) string { return "https://" + h + "/" + p + ".git" }},
	{"ssh-scheme", func(h, p string) string { return "ssh://git@" + h + ":22/" + p + ".git" }},
	{"https-user", func(h, p string) string { return "https://someone@" + h + "/" + p }},
}

func fakePlatforms(hostname string) (pr.Platform, bool, error) {
	platform, ok := map[string]pr.Platform{
		"github.com":    pr.GitHub,
		"ghe.example.com":   pr.GitHub,
		"bitbucket.org": pr.Bitbucket,
	}[hostname]
	return platform, ok, nil
}

func errText(err error) string {
	if err == nil {
		return ""
	}
	return err.Error()
}

const acmeRefusal = `several git remotes point at code hosts; check out a branch that tracks one
  acme   bitbucket.org/acme/scheduler
  mirror  bitbucket.org/acme-mirror/scheduler`

func TestPickRemote(t *testing.T) {
	acme := []remote{
		{"acme", "bitbucket.org", "acme/scheduler"},
		{"mirror", "bitbucket.org", "acme-mirror/scheduler"},
	}
	cases := []struct {
		name    string
		remotes []remote
		tracked string
		want    pr.Repo
		err     string
	}{
		{
			name: "the tracked remote wins over origin",
			remotes: []remote{
				{"origin", "github.com", "someone/fork"},
				{"upstream", "github.com", "team/app"},
			},
			tracked: "upstream",
			want:    pr.Repo{Platform: pr.GitHub, Hostname: "github.com", Owner: "team", Name: "app"},
		},
		{
			name: "origin when nothing is tracked",
			remotes: []remote{
				{"fork", "github.com", "someone/fork"},
				{"origin", "github.com", "team/app"},
			},
			want: pr.Repo{Platform: pr.GitHub, Hostname: "github.com", Owner: "team", Name: "app"},
		},
		{
			name: "the only remote on a code host when there is no origin",
			remotes: []remote{
				{"mirror", "gitlab.com", "team/app"},
				{"upstream", "github.com", "team/app"},
			},
			want: pr.Repo{Platform: pr.GitHub, Hostname: "github.com", Owner: "team", Name: "app"},
		},
		{
			name:    "the scheduler clone on develop tracking acme/develop",
			remotes: acme,
			tracked: "acme",
			want:    pr.Repo{Platform: pr.Bitbucket, Hostname: "bitbucket.org", Owner: "acme", Name: "scheduler"},
		},
		{
			name:    "the scheduler clone on a branch that tracks nothing",
			remotes: acme,
			err:     acmeRefusal,
		},
		{
			name:    "no remote on a supported code host",
			remotes: []remote{{"origin", "gitlab.com", "team/app"}},
			err:     "no git remote points at a supported code host (GitHub, Bitbucket)",
		},
		{
			name:    "github.com is GitHub",
			remotes: []remote{{"origin", "github.com", "TheLazyLemur/review-assist"}},
			want:    pr.Repo{Platform: pr.GitHub, Hostname: "github.com", Owner: "TheLazyLemur", Name: "review-assist"},
		},
		{
			name:    "a GitHub Enterprise hostname keeps its hostname",
			remotes: []remote{{"origin", "ghe.example.com", "acme/widgets"}},
			want:    pr.Repo{Platform: pr.GitHub, Hostname: "ghe.example.com", Owner: "acme", Name: "widgets"},
		},
		{
			name:    "bitbucket.org is Bitbucket",
			remotes: []remote{{"origin", "bitbucket.org", "acme/storefront"}},
			want:    pr.Repo{Platform: pr.Bitbucket, Hostname: "bitbucket.org", Owner: "acme", Name: "storefront"},
		},
		{
			name:    "any other hostname is not a supported code host",
			remotes: []remote{{"origin", "gitlab.com", "team/app"}, {"github", "github.com", "team/app"}},
			want:    pr.Repo{Platform: pr.GitHub, Hostname: "github.com", Owner: "team", Name: "app"},
		},
	}
	for _, form := range urlForms {
		for _, tc := range cases {
			t.Run(form.name+"/"+tc.name, func(t *testing.T) {
				// given
				// ... the case's remotes, with URLs in this form
				remotes := make([]gitrepo.Remote, len(tc.remotes))
				for i, r := range tc.remotes {
					remotes[i] = gitrepo.Remote{Name: r.name, URL: form.url(r.hostname, r.path)}
				}

				// when
				// ... the remote is picked
				repo, err := gitrepo.PickRemote(remotes, tc.tracked, fakePlatforms)

				// then
				// ... the rule gives the case's repository, or refuses with its message
				if errText(err) != tc.err {
					t.Fatalf("error: want %q, got %q", tc.err, errText(err))
				}
				if repo != tc.want {
					t.Fatalf("repo: want %+v, got %+v", tc.want, repo)
				}
			})
		}
	}
}

func TestPickRemoteReportsAFailedPlatformLookupWithItsHostname(t *testing.T) {
	// given
	// ... a remote on a hostname whose platform lookup fails
	remotes := []gitrepo.Remote{{Name: "origin", URL: "git@ghe.example.com:acme/widgets.git"}}
	failing := func(string) (pr.Platform, bool, error) { return "", false, errors.New("gh auth status: not installed") }

	// when
	// ... the remote is picked
	_, err := gitrepo.PickRemote(remotes, "", failing)

	// then
	// ... the lookup's error comes back, led by the hostname
	if errText(err) != "ghe.example.com: gh auth status: not installed" {
		t.Fatalf("got %q", errText(err))
	}
}
