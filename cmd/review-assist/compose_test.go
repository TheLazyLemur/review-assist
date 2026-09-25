package main

import (
	"context"
	"os/exec"
	"reflect"
	"testing"

	"github.com/TheLazyLemur/review-assist/internal/adapters/bitbucket"
	"github.com/TheLazyLemur/review-assist/internal/adapters/github"
	"github.com/TheLazyLemur/review-assist/internal/core/pr"
)

func TestPastedPRURLInOtherCaseIsTheLocalRepository(t *testing.T) {
	// given
	// ... a clone whose origin spells owner and name in mixed case
	dir := t.TempDir()
	for _, args := range [][]string{
		{"init", "--quiet"},
		{"remote", "add", "origin", "git@github.com:TheLazyLemur/Review-Assist.git"},
	} {
		if out, err := exec.Command("git", append([]string{"-C", dir}, args...)...).CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v %s", args, err, out)
		}
	}

	// when
	// ... a PR URL spelled in lower case is the target
	_, number, remote, err := resolveTarget(context.Background(), dir, github.ExecRunner{Dir: dir}, "https://github.com/thelazylemur/review-assist/pull/3")

	// then
	// ... it is the repository checked out here
	if err != nil {
		t.Fatal(err)
	}
	if number != 3 || remote != "origin" {
		t.Fatalf("want #3 from origin, got #%d from %q", number, remote)
	}
}

var scheduler = pr.Repo{Platform: pr.Bitbucket, Hostname: "bitbucket.org", Owner: "acme", Name: "scheduler"}

func TestCodeHostFollowsTheRepositorysPlatform(t *testing.T) {
	cases := []struct {
		name      string
		repo      pr.Repo
		bitbucket bitbucketConfig
		want      pr.CodeHost
	}{
		{"bitbucket.org gets Bitbucket", scheduler, bitbucketConfig{email: "me@example.com", apiToken: "t"}, &bitbucket.Client{}},
		{"github.com gets GitHub, with no Bitbucket credentials", pr.Repo{Platform: pr.GitHub, Hostname: "github.com", Owner: "o", Name: "r"}, bitbucketConfig{}, &github.Client{}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			// given
			// ... a config with the case's Bitbucket credentials
			cfg := config{bitbucket: tc.bitbucket}

			// when
			// ... the code host is built for the case's repository
			host, err := newCodeHost(cfg, tc.repo, "origin", t.TempDir(), github.ExecRunner{})

			// then
			// ... it is the code host of that platform
			if err != nil {
				t.Fatal(err)
			}
			if reflect.TypeOf(host) != reflect.TypeOf(tc.want) {
				t.Errorf("want %T, got %T", tc.want, host)
			}
		})
	}
}

func TestBitbucketRefusesToStartWithoutAnEmailAndAnAPIToken(t *testing.T) {
	cases := []struct {
		name      string
		bitbucket bitbucketConfig
	}{
		{"no email", bitbucketConfig{apiToken: "t"}},
		{"no API token", bitbucketConfig{email: "me@example.com"}},
		{"neither", bitbucketConfig{}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			// given
			// ... a config missing the case's Bitbucket credentials
			cfg := config{bitbucket: tc.bitbucket, file: "/home/u/.config/review-assist/config.json"}

			// when
			// ... the code host is built for a bitbucket.org repository
			_, err := newCodeHost(cfg, scheduler, "origin", t.TempDir(), github.ExecRunner{})

			// then
			// ... it refuses, naming both settings by config key and env var
			want := "bitbucket.org/acme/scheduler is on Bitbucket, which needs an Atlassian account email and an API token: " +
				"set bitbucket.email and bitbucket.api_token in /home/u/.config/review-assist/config.json, " +
				"or REVIEW_ASSIST_BITBUCKET_EMAIL and REVIEW_ASSIST_BITBUCKET_API_TOKEN"
			if err == nil || err.Error() != want {
				t.Errorf("want %q, got %v", want, err)
			}
		})
	}
}

func TestPastedPRURLOfAnotherRepositoryHasNoRemote(t *testing.T) {
	// given
	// ... a clone of one repository
	dir := t.TempDir()
	for _, args := range [][]string{
		{"init", "--quiet"},
		{"remote", "add", "origin", "git@github.com:TheLazyLemur/review-assist.git"},
	} {
		if out, err := exec.Command("git", append([]string{"-C", dir}, args...)...).CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v %s", args, err, out)
		}
	}

	// when
	// ... a PR URL of a different repository is the target
	repo, number, remote, err := resolveTarget(context.Background(), dir, github.ExecRunner{Dir: dir}, "https://bitbucket.org/acme/scheduler/pull-requests/7")

	// then
	// ... it is that repository with no remote, so checkout stays hidden
	if err != nil {
		t.Fatal(err)
	}
	if repo != scheduler || number != 7 || remote != "" {
		t.Fatalf("want %+v #7 with no remote, got %+v #%d remote %q", scheduler, repo, number, remote)
	}
}
