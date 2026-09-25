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
		"github.com":      pr.GitHub,
		"ghe.example.com": pr.GitHub,
		"bitbucket.org":   pr.Bitbucket,
	}[hostname]
	return platform, ok, nil
}

func errText(err error) string {
	if err == nil {
		return ""
	}
	return err.Error()
}

const severalRemotesRefusal = `several git remotes point at code hosts; check out a branch that tracks one
  acme    bitbucket.org/acme/scheduler
  mirror  bitbucket.org/acme-mirror/scheduler`

func TestPickRemote(t *testing.T) {
	twoBitbucket := []remote{
		{"acme", "bitbucket.org", "acme/scheduler"},
		{"mirror", "bitbucket.org", "acme-mirror/scheduler"},
	}
	cases := []struct {
		name    string
		remotes []remote
		tracked string
		want    pr.Repo
		picked  string
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
			picked:  "upstream",
		},
		{
			name: "origin when nothing is tracked",
			remotes: []remote{
				{"fork", "github.com", "someone/fork"},
				{"origin", "github.com", "team/app"},
			},
			want:   pr.Repo{Platform: pr.GitHub, Hostname: "github.com", Owner: "team", Name: "app"},
			picked: "origin",
		},
		{
			name: "the only remote on a code host when there is no origin",
			remotes: []remote{
				{"mirror", "gitlab.com", "team/app"},
				{"upstream", "github.com", "team/app"},
			},
			want:   pr.Repo{Platform: pr.GitHub, Hostname: "github.com", Owner: "team", Name: "app"},
			picked: "upstream",
		},
		{
			name:    "scheduler on develop tracking acme/develop",
			remotes: twoBitbucket,
			tracked: "acme",
			want:    pr.Repo{Platform: pr.Bitbucket, Hostname: "bitbucket.org", Owner: "acme", Name: "scheduler"},
			picked:  "acme",
		},
		{
			name:    "scheduler on a branch that tracks nothing",
			remotes: twoBitbucket,
			err:     severalRemotesRefusal,
		},
		{
			name:    "no remote on a supported code host",
			remotes: []remote{{"origin", "gitlab.com", "team/app"}, {"mirror", "gitlab.com", "team/app-mirror"}},
			err: `no git remote points at a supported code host (GitHub, Bitbucket)
  origin  gitlab.com/team/app
  mirror  gitlab.com/team/app-mirror`,
		},
		{
			// Every URL form parses to the alias, so the refusal is the same in each.
			name:    "an SSH host alias is not a supported code host",
			remotes: []remote{{"origin", "github-work", "TheLazyLemur/review-assist"}},
			err: `no git remote points at a supported code host (GitHub, Bitbucket)
  origin  github-work/TheLazyLemur/review-assist`,
		},
		{
			name: "no remotes at all",
			err:  "no git remote points at a supported code host (GitHub, Bitbucket)",
		},
		{
			name:    "github.com is GitHub",
			remotes: []remote{{"origin", "github.com", "TheLazyLemur/review-assist"}},
			want:    pr.Repo{Platform: pr.GitHub, Hostname: "github.com", Owner: "TheLazyLemur", Name: "review-assist"},
			picked:  "origin",
		},
		{
			name:    "a GitHub Enterprise hostname keeps its hostname",
			remotes: []remote{{"origin", "ghe.example.com", "acme/widgets"}},
			want:    pr.Repo{Platform: pr.GitHub, Hostname: "ghe.example.com", Owner: "acme", Name: "widgets"},
			picked:  "origin",
		},
		{
			name:    "bitbucket.org is Bitbucket",
			remotes: []remote{{"origin", "bitbucket.org", "acme/storefront"}},
			want:    pr.Repo{Platform: pr.Bitbucket, Hostname: "bitbucket.org", Owner: "acme", Name: "storefront"},
			picked:  "origin",
		},
		{
			name:    "any other hostname is not a supported code host",
			remotes: []remote{{"origin", "gitlab.com", "team/app"}, {"github", "github.com", "team/app"}},
			want:    pr.Repo{Platform: pr.GitHub, Hostname: "github.com", Owner: "team", Name: "app"},
			picked:  "github",
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
				repo, picked, err := gitrepo.PickRemote(remotes, tc.tracked, fakePlatforms)

				// then
				// ... the rule gives the case's repository and remote, or refuses with its message
				if errText(err) != tc.err {
					t.Fatalf("error: want %q, got %q", tc.err, errText(err))
				}
				if repo != tc.want {
					t.Fatalf("repo: want %+v, got %+v", tc.want, repo)
				}
				if picked != tc.picked {
					t.Fatalf("remote: want %q, got %q", tc.picked, picked)
				}
			})
		}
	}
}

func TestPickRemoteReportsAFailedPlatformLookupWithItsHostname(t *testing.T) {
	// given
	// ... a remote on a hostname whose platform lookup fails
	remotes := []gitrepo.Remote{{Name: "origin", URL: "git@ghe.example.com:acme/widgets.git"}}
	failing := func(string) (pr.Platform, bool, error) {
		return "", false, errors.New("not installed (gh auth status)")
	}

	// when
	// ... the remote is picked
	_, _, err := gitrepo.PickRemote(remotes, "", failing)

	// then
	// ... the lookup's error comes back, led by the hostname
	if errText(err) != "ghe.example.com: not installed (gh auth status)" {
		t.Fatalf("got %q", errText(err))
	}
}

// onlyGitHubDotCom fails for every hostname but github.com, as a broken gh does.
func onlyGitHubDotCom(hostname string) (pr.Platform, bool, error) {
	if hostname != "github.com" {
		return "", false, errors.New("gh broke")
	}
	return pr.GitHub, true, nil
}

func TestPickRemoteDoesNotLookUpRemotesTheRuleDoesNotReach(t *testing.T) {
	// given
	// ... origin on github.com and a mirror on gitlab.com, with a lookup that fails for any hostname but github.com
	remotes := []gitrepo.Remote{
		{Name: "mirror", URL: "git@gitlab.com:team/app.git"},
		{Name: "origin", URL: "git@github.com:team/app.git"},
	}

	// when
	// ... the remote is picked
	repo, _, err := gitrepo.PickRemote(remotes, "", onlyGitHubDotCom)

	// then
	// ... origin is picked without looking up the mirror
	if err != nil {
		t.Fatal(err)
	}
	want := pr.Repo{Platform: pr.GitHub, Hostname: "github.com", Owner: "team", Name: "app"}
	if repo != want {
		t.Fatalf("want %+v, got %+v", want, repo)
	}
}

func TestNoCodeHostRefusalShowsARemoteItCannotParseByItsURL(t *testing.T) {
	// given
	// ... a remote on a local path
	remotes := []gitrepo.Remote{{Name: "backup", URL: "/srv/git/app.git"}}

	// when
	// ... the remote is picked
	_, _, err := gitrepo.PickRemote(remotes, "", fakePlatforms)

	// then
	// ... the local path is listed as it is
	want := "no git remote points at a supported code host (GitHub, Bitbucket)\n  backup  /srv/git/app.git"
	if errText(err) != want {
		t.Errorf("want %q, got %q", want, errText(err))
	}
}

func TestNoCodeHostRefusalLeavesOutCredentialsInAURL(t *testing.T) {
	// given
	// ... a remote whose URL carries a token and does not parse as owner/name
	remotes := []gitrepo.Remote{{Name: "origin", URL: "https://user:secret@example.com/a/b/c"}}

	// when
	// ... the remote is picked
	_, _, err := gitrepo.PickRemote(remotes, "", fakePlatforms)

	// then
	// ... the URL is listed without its user or token
	want := "no git remote points at a supported code host (GitHub, Bitbucket)\n  origin  https://example.com/a/b/c"
	if errText(err) != want {
		t.Errorf("want %q, got %q", want, errText(err))
	}
}
