package gitrepo_test

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/TheLazyLemur/review-assist/internal/adapters/gitrepo"
	"github.com/TheLazyLemur/review-assist/internal/core/pr"
)

var (
	bitbucketRepo = pr.Repo{Platform: pr.Bitbucket, Hostname: "bitbucket.org", Owner: "acme", Name: "scheduler"}
	githubRepo    = pr.Repo{Platform: pr.GitHub, Hostname: "github.com", Owner: "acme", Name: "scheduler"}
)

func git(t *testing.T, dir string, args ...string) string {
	t.Helper()
	out, err := exec.Command("git", append([]string{"-C", dir}, args...)...).CombinedOutput()
	if err != nil {
		t.Fatalf("git %v: %v %s", args, err, out)
	}
	return strings.TrimSpace(string(out))
}

// codeHost makes a bare repository at a path that gitrepo matches to repo,
// with one commit on main, and a working clone that pushes to it. The user's
// git config stays out, so signing or hooks cannot break the commits.
func codeHost(t *testing.T, repo pr.Repo) (bare, work string) {
	t.Helper()
	t.Setenv("GIT_CONFIG_GLOBAL", os.DevNull)
	t.Setenv("GIT_CONFIG_NOSYSTEM", "1")
	t.Setenv("GIT_AUTHOR_NAME", "test")
	t.Setenv("GIT_AUTHOR_EMAIL", "test@example.com")
	t.Setenv("GIT_COMMITTER_NAME", "test")
	t.Setenv("GIT_COMMITTER_EMAIL", "test@example.com")
	root := t.TempDir()
	bare = filepath.Join(root, repo.Hostname, repo.Owner, repo.Name+".git")
	work = filepath.Join(root, "work")
	git(t, root, "init", "--quiet", "--bare", "--initial-branch=main", bare)
	git(t, root, "clone", "--quiet", bare, work)
	commit(t, work, "a.go", "package a\n")
	git(t, work, "push", "--quiet", "origin", "HEAD:main")
	return bare, work
}

func cloneOf(t *testing.T, bare string) string {
	t.Helper()
	dir := filepath.Join(t.TempDir(), "clone")
	git(t, filepath.Dir(dir), "clone", "--quiet", bare, dir)
	return dir
}

// commit commits one file on the checked-out branch and returns the hash.
func commit(t *testing.T, dir, file, content string) string {
	t.Helper()
	if err := os.WriteFile(filepath.Join(dir, file), []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	git(t, dir, "add", file)
	git(t, dir, "commit", "--quiet", "-m", "change "+file)
	return git(t, dir, "rev-parse", "HEAD")
}

// bitbucketPullRequest pushes a new commit to main and a feature branch off
// it, neither of which the clone has.
func bitbucketPullRequest(t *testing.T) (clone, head, base string) {
	t.Helper()
	bare, work := codeHost(t, bitbucketRepo)
	clone = cloneOf(t, bare)
	base = commit(t, work, "a.go", "package a\n\nconst Base = 1\n")
	git(t, work, "push", "--quiet", "origin", "HEAD:main")
	git(t, work, "checkout", "--quiet", "-b", "feature")
	head = commit(t, work, "b.go", "package a\n\nconst Head = 2\n")
	git(t, work, "push", "--quiet", "origin", "feature")
	return clone, head, base
}

func TestOpenFetchesTheBranchesOfABitbucketPullRequest(t *testing.T) {
	// given
	// ... a Bitbucket pull request from feature into main, whose commits the clone lacks
	clone, head, base := bitbucketPullRequest(t)
	p := &pr.PR{Summary: pr.Summary{Number: 7, HeadRef: "feature", BaseRef: "main"}, HeadSHA: head, BaseSHA: base}

	// when
	// ... the clone is opened for the pull request
	code, err := gitrepo.Source{Cwd: clone}.Open(context.Background(), bitbucketRepo, p)

	// then
	// ... the head and base commits are readable
	if err != nil {
		t.Fatal(err)
	}
	hasHead, hasBase := code.HasCommit(context.Background(), head), code.HasCommit(context.Background(), base)
	if !hasHead || !hasBase {
		t.Fatalf("head %v, base %v", hasHead, hasBase)
	}
}

func TestOpenFetchesTheBaseOfABitbucketPullRequestWhoseSourceBranchIsGone(t *testing.T) {
	// given
	// ... a Bitbucket pull request whose source branch is not on the repository, as from a fork
	clone, head, base := bitbucketPullRequest(t)
	p := &pr.PR{Summary: pr.Summary{Number: 7, HeadRef: "gone", BaseRef: "main"}, HeadSHA: head, BaseSHA: base}

	// when
	// ... the clone is opened for the pull request
	code, err := gitrepo.Source{Cwd: clone}.Open(context.Background(), bitbucketRepo, p)

	// then
	// ... the base commit is readable and the head is missing
	if err != nil {
		t.Fatal(err)
	}
	hasHead, hasBase := code.HasCommit(context.Background(), head), code.HasCommit(context.Background(), base)
	if hasHead || !hasBase {
		t.Fatalf("head %v, base %v", hasHead, hasBase)
	}
}

func TestOpenLeavesRemoteTrackingBranchesAlone(t *testing.T) {
	// given
	// ... a Bitbucket pull request into main, which moved on since the clone's origin/main
	clone, head, base := bitbucketPullRequest(t)
	tracked := git(t, clone, "rev-parse", "refs/remotes/origin/main")
	p := &pr.PR{Summary: pr.Summary{Number: 7, HeadRef: "feature", BaseRef: "main"}, HeadSHA: head, BaseSHA: base}

	// when
	// ... the clone is opened for the pull request
	_, err := gitrepo.Source{Cwd: clone}.Open(context.Background(), bitbucketRepo, p)

	// then
	// ... origin/main has not moved
	if err != nil {
		t.Fatal(err)
	}
	if got := git(t, clone, "rev-parse", "refs/remotes/origin/main"); got != tracked {
		t.Fatalf("origin/main moved from %s to %s", tracked, got)
	}
}

func TestOpenFindsABitbucketPullRequestByTwelveCharacterHashes(t *testing.T) {
	// given
	// ... a Bitbucket pull request whose hashes are abbreviated to 12 characters, as Bitbucket gives them
	clone, head, base := bitbucketPullRequest(t)
	head, base = head[:12], base[:12]
	p := &pr.PR{Summary: pr.Summary{Number: 7, HeadRef: "feature", BaseRef: "main"}, HeadSHA: head, BaseSHA: base}

	// when
	// ... the clone is opened for the pull request
	code, err := gitrepo.Source{Cwd: clone}.Open(context.Background(), bitbucketRepo, p)

	// then
	// ... both commits are found by their short hashes
	if err != nil {
		t.Fatal(err)
	}
	hasHead, hasBase := code.HasCommit(context.Background(), head), code.HasCommit(context.Background(), base)
	if !hasHead || !hasBase {
		t.Fatalf("head %v, base %v", hasHead, hasBase)
	}
}

func TestGrepAtATwelveCharacterHashNamesTheFileWithoutTheHash(t *testing.T) {
	// given
	// ... a Bitbucket pull request opened by its 12-character hashes
	clone, head, base := bitbucketPullRequest(t)
	head, base = head[:12], base[:12]
	p := &pr.PR{Summary: pr.Summary{Number: 7, HeadRef: "feature", BaseRef: "main"}, HeadSHA: head, BaseSHA: base}
	code, err := gitrepo.Source{Cwd: clone}.Open(context.Background(), bitbucketRepo, p)
	if err != nil {
		t.Fatal(err)
	}

	// when
	// ... the head is searched
	lines, err := code.Grep(context.Background(), head, "Head", "", false)

	// then
	// ... the match names the file and line without the hash
	if err != nil {
		t.Fatal(err)
	}
	if len(lines) != 1 || lines[0] != "b.go:3:const Head = 2" {
		t.Fatalf("got %q", lines)
	}
}

func TestOpenRefusesABitbucketPullRequestWithNoSourceBranch(t *testing.T) {
	// given
	// ... a Bitbucket pull request with no head branch
	p := &pr.PR{Summary: pr.Summary{Number: 7, BaseRef: "main"}, HeadSHA: "a1b2c3d4e5f6", BaseSHA: "f6e5d4c3b2a1"}

	// when
	// ... it is opened
	_, err := gitrepo.Source{Cwd: t.TempDir()}.Open(context.Background(), bitbucketRepo, p)

	// then
	// ... it is refused for the missing branch
	if errText(err) != `gitrepo: bad head branch "": want a branch name` {
		t.Fatalf("got %q", errText(err))
	}
}

func TestOpenRefusesABitbucketBranchThatIsNotABranchName(t *testing.T) {
	cases := []struct{ name, branch string }{
		{"read as a refspec", "feature:main"},
		{"read as the previous branch", "@{-1}"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			// given
			// ... a Bitbucket pull request whose source branch git would not take as a branch name
			p := &pr.PR{Summary: pr.Summary{Number: 7, HeadRef: tc.branch, BaseRef: "main"}, HeadSHA: "a1b2c3d4e5f6", BaseSHA: "f6e5d4c3b2a1"}

			// when
			// ... it is opened
			_, err := gitrepo.Source{Cwd: t.TempDir()}.Open(context.Background(), bitbucketRepo, p)

			// then
			// ... it is refused before any fetch
			want := fmt.Sprintf("gitrepo: bad head branch %q: want a branch name", tc.branch)
			if errText(err) != want {
				t.Fatalf("want %q, got %q", want, errText(err))
			}
		})
	}
}

func TestOpenUsesACloneWhoseRemoteSpellsTheRepositoryInOtherCase(t *testing.T) {
	// given
	// ... a clone whose remote spells owner and name in mixed case, and no cache dir to mirror into
	bare, work := codeHost(t, pr.Repo{Platform: pr.Bitbucket, Hostname: "bitbucket.org", Owner: "Acme", Name: "Scheduler"})
	clone := cloneOf(t, bare)
	sha := git(t, work, "rev-parse", "HEAD")
	p := &pr.PR{Summary: pr.Summary{Number: 7, HeadRef: "main", BaseRef: "main"}, HeadSHA: sha, BaseSHA: sha}

	// when
	// ... it is opened for the repository spelled in lower case
	code, err := gitrepo.Source{Cwd: clone}.Open(context.Background(), bitbucketRepo, p)

	// then
	// ... the clone is used
	if err != nil {
		t.Fatal(err)
	}
	if want := git(t, clone, "rev-parse", "--show-toplevel"); code.Location() != want {
		t.Fatalf("want %s, got %s", want, code.Location())
	}
}

func TestOpenRefusesAPlatformItCannotFetchFrom(t *testing.T) {
	// given
	// ... a pull request on a platform gitrepo has no fetch for
	repo := pr.Repo{Platform: "gitlab", Hostname: "gitlab.com", Owner: "acme", Name: "scheduler"}
	p := &pr.PR{Summary: pr.Summary{Number: 7, HeadRef: "feature", BaseRef: "main"}, HeadSHA: "a1b2c3d4e5f6", BaseSHA: "f6e5d4c3b2a1"}

	// when
	// ... it is opened
	_, err := gitrepo.Source{Cwd: t.TempDir()}.Open(context.Background(), repo, p)

	// then
	// ... it is refused for the platform
	if errText(err) != `gitrepo: no fetch for platform "gitlab"` {
		t.Fatalf("got %q", errText(err))
	}
}

func TestOpenFetchesTheHeadOfAGitHubPullRequestFromItsPullRef(t *testing.T) {
	// given
	// ... a GitHub pull request whose head is only on refs/pull/7/head, not on its branch
	bare, work := codeHost(t, githubRepo)
	clone := cloneOf(t, bare)
	base := git(t, work, "rev-parse", "HEAD")
	git(t, work, "checkout", "--quiet", "-b", "feature")
	head := commit(t, work, "b.go", "package a\n")
	git(t, work, "push", "--quiet", "origin", "HEAD:refs/pull/7/head")
	p := &pr.PR{Summary: pr.Summary{Number: 7, HeadRef: "feature", BaseRef: "main"}, HeadSHA: head, BaseSHA: base}

	// when
	// ... the clone is opened for the pull request
	code, err := gitrepo.Source{Cwd: clone}.Open(context.Background(), githubRepo, p)

	// then
	// ... the head commit is readable
	if err != nil {
		t.Fatal(err)
	}
	if !code.HasCommit(context.Background(), head) {
		t.Fatal("head commit missing")
	}
}
