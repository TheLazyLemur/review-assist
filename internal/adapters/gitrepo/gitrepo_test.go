package gitrepo_test

import (
	"context"
	"encoding/base64"
	"fmt"
	"net/http"
	"net/http/cgi"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"sync"
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

// apiToken holds a backslash so a helper that lets the shell read escapes in
// it hands git a different password.
const apiToken = `bb-api\token-7f3a`

// serve puts the code host behind git http-backend on a local server and
// points https://<host>/ at it, so a mirror's URL stays the real one. The
// server answers 401 until a request carries user and password, as a code
// host does for a private repository. Any git run in the test sees the
// rewrite, through the environment.
func serve(t *testing.T, bare string, repo pr.Repo, user, password string) string {
	t.Helper()
	gitPath, err := exec.LookPath("git")
	if err != nil {
		t.Fatal(err)
	}
	backend := &cgi.Handler{
		Path:       gitPath,
		Args:       []string{"http-backend"},
		Env:        []string{"GIT_PROJECT_ROOT=" + strings.TrimSuffix(bare, "/"+repo.Qualified()+".git"), "GIT_HTTP_EXPORT_ALL=1"},
		InheritEnv: []string{"GIT_CONFIG_GLOBAL", "GIT_CONFIG_NOSYSTEM"},
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if u, p, _ := r.BasicAuth(); u != user || p != password {
			w.Header().Set("WWW-Authenticate", `Basic realm="test"`)
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		backend.ServeHTTP(w, r)
	}))
	t.Cleanup(srv.Close)
	t.Setenv("GIT_CONFIG_COUNT", "1")
	t.Setenv("GIT_CONFIG_KEY_0", "url."+srv.URL+"/"+repo.Hostname+"/.insteadOf")
	t.Setenv("GIT_CONFIG_VALUE_0", "https://"+repo.Hostname+"/")
	return srv.URL
}

// traceGit records the command line of every git process from here on,
// including the helpers and transports git starts.
func traceGit(t *testing.T) string {
	t.Helper()
	trace := filepath.Join(t.TempDir(), "trace")
	t.Setenv("GIT_TRACE", trace)
	return trace
}

func readFile(t *testing.T, path string) string {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

// privateBitbucketRepo serves a Bitbucket repository that needs the API token,
// as the Bitbucket gitrepo gives the token to, and returns the pull request of
// its one commit and the server's URL.
func privateBitbucketRepo(t *testing.T) (p *pr.PR, work, url string) {
	t.Helper()
	bare, work := codeHost(t, bitbucketRepo)
	url = serve(t, bare, bitbucketRepo, "x-bitbucket-api-token-auth", apiToken)
	gitrepo.SetBitbucketURL(t, url)
	sha := git(t, work, "rev-parse", "HEAD")
	return &pr.PR{Summary: pr.Summary{Number: 7, HeadRef: "main", BaseRef: "main"}, HeadSHA: sha, BaseSHA: sha}, work, url
}

// userHelper adds a credential helper to the user's git config, after the
// URL rewrite serve sets.
func userHelper(t *testing.T, helper string) {
	t.Helper()
	t.Setenv("GIT_CONFIG_COUNT", "2")
	t.Setenv("GIT_CONFIG_KEY_1", "credential.helper")
	t.Setenv("GIT_CONFIG_VALUE_1", helper)
}

func TestOpenMirrorsAPrivateBitbucketRepositoryWithTheAPIToken(t *testing.T) {
	// given
	// ... a private Bitbucket repository, no clone of it here, and the API token
	p, _, _ := privateBitbucketRepo(t)
	src := gitrepo.Source{Cwd: t.TempDir(), CacheDir: t.TempDir(), BitbucketToken: apiToken}

	// when
	// ... it is opened for a pull request
	code, err := src.Open(context.Background(), bitbucketRepo, p)

	// then
	// ... the mirror is cloned and holds the head commit
	if err != nil {
		t.Fatal(err)
	}
	if !code.HasCommit(context.Background(), p.HeadSHA) {
		t.Fatal("head commit missing")
	}
}

func TestOpenFetchesIntoAPrivateBitbucketMirrorWithTheAPIToken(t *testing.T) {
	// given
	// ... a mirror of a private Bitbucket repository, which then gets a feature branch the mirror lacks
	p, work, _ := privateBitbucketRepo(t)
	src := gitrepo.Source{Cwd: t.TempDir(), CacheDir: t.TempDir(), BitbucketToken: apiToken}
	if _, err := src.Open(context.Background(), bitbucketRepo, p); err != nil {
		t.Fatal(err)
	}
	git(t, work, "checkout", "--quiet", "-b", "feature")
	head := commit(t, work, "b.go", "package a\n")
	git(t, work, "push", "--quiet", "origin", "feature")
	p = &pr.PR{Summary: pr.Summary{Number: 8, HeadRef: "feature", BaseRef: "main"}, HeadSHA: head, BaseSHA: p.BaseSHA}

	// when
	// ... it is opened for a pull request from feature
	code, err := src.Open(context.Background(), bitbucketRepo, p)

	// then
	// ... the head commit was fetched into the mirror
	if err != nil {
		t.Fatal(err)
	}
	if !code.HasCommit(context.Background(), head) {
		t.Fatal("head commit missing")
	}
}

func TestOpenKeepsTheAPITokenOutOfTheMirrorsConfig(t *testing.T) {
	// given
	// ... a private Bitbucket repository and the API token
	p, _, _ := privateBitbucketRepo(t)
	src := gitrepo.Source{Cwd: t.TempDir(), CacheDir: t.TempDir(), BitbucketToken: apiToken}

	// when
	// ... it is mirrored
	code, err := src.Open(context.Background(), bitbucketRepo, p)

	// then
	// ... the mirror's config holds neither the token nor a credential helper
	if err != nil {
		t.Fatal(err)
	}
	if config := readFile(t, filepath.Join(code.Location(), "config")); strings.Contains(config, apiToken) || strings.Contains(config, "credential") {
		t.Fatalf("credentials in mirror config:\n%s", config)
	}
}

func TestOpenKeepsTheAPITokenOutOfTheMirrorsRemoteURL(t *testing.T) {
	// given
	// ... a private Bitbucket repository and the API token
	p, _, _ := privateBitbucketRepo(t)
	src := gitrepo.Source{Cwd: t.TempDir(), CacheDir: t.TempDir(), BitbucketToken: apiToken}

	// when
	// ... it is mirrored
	code, err := src.Open(context.Background(), bitbucketRepo, p)

	// then
	// ... the mirror's remote is the plain repository URL
	if err != nil {
		t.Fatal(err)
	}
	if got := git(t, code.Location(), "config", "remote.origin.url"); got != "https://bitbucket.org/acme/scheduler.git" {
		t.Fatalf("got %q", got)
	}
}

func TestOpenKeepsTheAPITokenOffEveryGitCommandLine(t *testing.T) {
	// given
	// ... a private Bitbucket repository, every git command line recorded, and a mirror of it made with the API token
	p, work, _ := privateBitbucketRepo(t)
	trace := traceGit(t)
	src := gitrepo.Source{Cwd: t.TempDir(), CacheDir: t.TempDir(), BitbucketToken: apiToken}
	if _, err := src.Open(context.Background(), bitbucketRepo, p); err != nil {
		t.Fatal(err)
	}

	// given
	// ... a new commit the mirror lacks
	head := commit(t, work, "b.go", "package a\n")
	git(t, work, "push", "--quiet", "origin", "HEAD:main")

	// when
	// ... the mirror is opened for a pull request at that commit
	code, err := src.Open(context.Background(), bitbucketRepo, &pr.PR{Summary: p.Summary, HeadSHA: head, BaseSHA: head})

	// then
	// ... the commit was fetched, and no command line of the clone or the fetch held the token
	if err != nil || !code.HasCommit(context.Background(), head) {
		t.Fatalf("fetch %v", err)
	}
	lines := readFile(t, trace)
	if !strings.Contains(lines, "x-bitbucket-api-token-auth") || strings.Contains(lines, apiToken) {
		t.Fatalf("want a credential helper and no token in:\n%s", lines)
	}
}

func TestOpenKeepsTheAPITokenOutOfTheUsersCurlTrace(t *testing.T) {
	// given
	// ... a private Bitbucket repository, and the user tracing git's HTTP traffic with redaction off
	p, _, _ := privateBitbucketRepo(t)
	curlTrace := filepath.Join(t.TempDir(), "curl")
	t.Setenv("GIT_TRACE_CURL", curlTrace)
	t.Setenv("GIT_TRACE_REDACT", "0")
	src := gitrepo.Source{Cwd: t.TempDir(), CacheDir: t.TempDir(), BitbucketToken: apiToken}

	// when
	// ... it is mirrored
	_, err := src.Open(context.Background(), bitbucketRepo, p)

	// then
	// ... the mirror is cloned and no Authorization header was traced
	if err != nil {
		t.Fatal(err)
	}
	traced, _ := os.ReadFile(curlTrace)
	if basic := base64.StdEncoding.EncodeToString([]byte("x-bitbucket-api-token-auth:" + apiToken)); strings.Contains(string(traced), basic) {
		t.Fatalf("credentials in curl trace:\n%s", traced)
	}
}

func TestOpenRefusesToMirrorABitbucketRepositoryWithNoAPIToken(t *testing.T) {
	// given
	// ... a Bitbucket repository with no clone here and no API token
	p := &pr.PR{Summary: pr.Summary{Number: 7, HeadRef: "main", BaseRef: "main"}, HeadSHA: "a1b2c3d4e5f6", BaseSHA: "a1b2c3d4e5f6"}
	src := gitrepo.Source{Cwd: t.TempDir(), CacheDir: t.TempDir()}

	// when
	// ... it is opened
	_, err := src.Open(context.Background(), bitbucketRepo, p)

	// then
	// ... it is refused for the missing token
	if errText(err) != "gitrepo: bitbucket.org/acme/scheduler is not checked out here and no Bitbucket API token is set to mirror it" {
		t.Fatalf("got %q", errText(err))
	}
}

func TestOpenMirrorsAGitHubRepositoryWithTheUsersCredentialsAndNoBitbucketOnes(t *testing.T) {
	// given
	// ... a private GitHub repository, the user's credential helper for it as gh sets one up, and a Bitbucket API token
	bare, work := codeHost(t, githubRepo)
	serve(t, bare, githubRepo, "octocat", "gh-token")
	userHelper(t, "!f() { echo username=octocat; echo password=gh-token; }; f")
	sha := git(t, work, "rev-parse", "HEAD")
	p := &pr.PR{Summary: pr.Summary{Number: 7, HeadRef: "main", BaseRef: "main"}, HeadSHA: sha, BaseSHA: sha}
	src := gitrepo.Source{Cwd: t.TempDir(), CacheDir: t.TempDir(), BitbucketToken: apiToken}

	// when
	// ... it is opened for a pull request
	code, err := src.Open(context.Background(), githubRepo, p)

	// then
	// ... the mirror is cloned with the user's credentials
	if err != nil {
		t.Fatal(err)
	}
	if !code.HasCommit(context.Background(), sha) {
		t.Fatal("head commit missing")
	}
}

func TestOpenMirrorsAPrivateBitbucketRepositoryPastTheUsersStaleHelper(t *testing.T) {
	// given
	// ... a private Bitbucket repository, and the user's credential store holding an old password for it
	p, _, url := privateBitbucketRepo(t)
	store := filepath.Join(t.TempDir(), "credentials")
	stale := strings.Replace(url, "http://", "http://x-bitbucket-api-token-auth:revoked@", 1) + "\n"
	if err := os.WriteFile(store, []byte(stale), 0o600); err != nil {
		t.Fatal(err)
	}
	userHelper(t, "store --file="+store)
	src := gitrepo.Source{Cwd: t.TempDir(), CacheDir: t.TempDir(), BitbucketToken: apiToken}

	// when
	// ... it is mirrored
	_, err := src.Open(context.Background(), bitbucketRepo, p)

	// then
	// ... the mirror is cloned with the API token, which the user's store never receives
	if err != nil {
		t.Fatal(err)
	}
	if got := readFile(t, store); got != stale {
		t.Fatalf("store changed to %q", got)
	}
}

func TestOpenFetchesIntoALocalBitbucketCloneWithTheUsersCredentials(t *testing.T) {
	// given
	// ... a Bitbucket repository served for the user's own credentials, a clone of it here, and a feature branch the clone lacks
	bare, work := codeHost(t, bitbucketRepo)
	url := serve(t, bare, bitbucketRepo, "dan", "app-password")
	gitrepo.SetBitbucketURL(t, url)
	userHelper(t, "!f() { echo username=dan; echo password=app-password; }; f")
	clone := filepath.Join(t.TempDir(), "clone")
	git(t, filepath.Dir(clone), "clone", "--quiet", "https://bitbucket.org/acme/scheduler.git", clone)
	base := git(t, work, "rev-parse", "HEAD")
	git(t, work, "checkout", "--quiet", "-b", "feature")
	head := commit(t, work, "b.go", "package a\n")
	git(t, work, "push", "--quiet", "origin", "feature")
	trace := traceGit(t)
	p := &pr.PR{Summary: pr.Summary{Number: 7, HeadRef: "feature", BaseRef: "main"}, HeadSHA: head, BaseSHA: base}

	// when
	// ... the clone is opened for the pull request, with an API token configured
	code, err := gitrepo.Source{Cwd: clone, CacheDir: t.TempDir(), BitbucketToken: apiToken}.Open(context.Background(), bitbucketRepo, p)

	// then
	// ... the clone fetched the head with the user's credentials and no Bitbucket token helper
	if err != nil {
		t.Fatal(err)
	}
	if !code.HasCommit(context.Background(), head) {
		t.Fatal("head commit missing")
	}
	if lines := readFile(t, trace); strings.Contains(lines, "x-bitbucket-api-token-auth") {
		t.Fatalf("token helper in:\n%s", lines)
	}
}

func TestOpenOffersTheAPITokenToNoHostABitbucketMirrorRedirectsTo(t *testing.T) {
	// given
	// ... a Bitbucket server that redirects every request to another host, which asks for credentials and records them
	var mu sync.Mutex
	var offered []string
	other := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, pass, _ := r.BasicAuth()
		mu.Lock()
		defer mu.Unlock()
		offered = append(offered, pass)
		w.Header().Set("WWW-Authenticate", `Basic realm="test"`)
		http.Error(w, "unauthorized", http.StatusUnauthorized)
	}))
	t.Cleanup(other.Close)
	bitbucket := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, other.URL+r.URL.RequestURI(), http.StatusFound)
	}))
	t.Cleanup(bitbucket.Close)
	t.Setenv("GIT_CONFIG_GLOBAL", os.DevNull)
	t.Setenv("GIT_CONFIG_NOSYSTEM", "1")
	t.Setenv("GIT_CONFIG_COUNT", "1")
	t.Setenv("GIT_CONFIG_KEY_0", "url."+bitbucket.URL+"/bitbucket.org/.insteadOf")
	t.Setenv("GIT_CONFIG_VALUE_0", "https://bitbucket.org/")
	gitrepo.SetBitbucketURL(t, bitbucket.URL)
	p := &pr.PR{Summary: pr.Summary{Number: 7, HeadRef: "main", BaseRef: "main"}, HeadSHA: "a1b2c3d4e5f6", BaseSHA: "a1b2c3d4e5f6"}
	src := gitrepo.Source{Cwd: t.TempDir(), CacheDir: t.TempDir(), BitbucketToken: apiToken}

	// when
	// ... it is mirrored
	_, err := src.Open(context.Background(), bitbucketRepo, p)

	// then
	// ... the mirror fails, and the other host was asked but never offered the token
	if err == nil {
		t.Fatal("mirrored through a host that refuses every request")
	}
	mu.Lock()
	defer mu.Unlock()
	if len(offered) == 0 || slices.Contains(offered, apiToken) {
		t.Fatalf("other host got passwords %q", offered)
	}
}
