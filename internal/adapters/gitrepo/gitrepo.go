// Package gitrepo implements review.CodeSource and review.Code with git.
// Code runs only read commands (cat-file, ls-tree, grep, log) against commit
// SHAs. Open may fetch, which adds objects but never touches branches, the
// index or the working tree.
package gitrepo

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"

	"github.com/TheLazyLemur/review-assist/internal/core/pr"
	"github.com/TheLazyLemur/review-assist/internal/core/review"
)

// Source picks the repository to read: the checkout at Cwd when it is a clone
// of the PR's repo, otherwise a bare mirror under CacheDir.
type Source struct {
	Cwd      string
	CacheDir string // e.g. os.UserCacheDir()/review-assist
	// BitbucketToken is the API token git gives a mirror of a bitbucket.org
	// repository. A clone at Cwd keeps the auth of its own remote.
	BitbucketToken string
}

var _ review.CodeSource = Source{}

func (s Source) Open(ctx context.Context, repo pr.Repo, p *pr.PR) (review.Code, error) {
	if p.HeadSHA == "" || p.BaseSHA == "" {
		return nil, fmt.Errorf("gitrepo: need head and base SHAs")
	}
	switch repo.Platform {
	case pr.GitHub:
	case pr.Bitbucket:
		// The names go into a fetch, so one that git would read as a
		// refspec or an option must stop here. Not --branch: inside a
		// repository it expands @{-1}.
		for _, b := range []struct{ side, name string }{{"base", p.BaseRef}, {"head", p.HeadRef}} {
			if b.name == "" || strings.HasPrefix(b.name, "-") || exec.CommandContext(ctx, "git", "check-ref-format", "refs/heads/"+b.name).Run() != nil {
				return nil, fmt.Errorf("gitrepo: bad %s branch %q: want a branch name", b.side, b.name)
			}
		}
	default:
		return nil, fmt.Errorf("gitrepo: no fetch for platform %q", repo.Platform)
	}
	r, err := s.resolve(ctx, repo)
	if err != nil {
		return nil, err
	}
	if r.HasCommit(ctx, p.HeadSHA) && r.HasCommit(ctx, p.BaseSHA) {
		return r, nil
	}
	remote := r.remoteFor(ctx, repo)
	fetch := func(ref string) {
		// Errors are tolerated: the review reports missing commits and the
		// agents still work from the diff. An empty --refmap keeps the
		// remote's refspec from moving its remote-tracking branches.
		_, _ = r.fetch(ctx, "--quiet", "--no-tags", "--no-write-fetch-head", "--refmap=", remote, ref)
	}
	if repo.Platform == pr.GitHub {
		fetch(fmt.Sprintf("refs/pull/%d/head", p.Number))
		if !r.HasCommit(ctx, p.BaseSHA) {
			fetch(p.BaseSHA)
		}
		return r, nil
	}
	// Bitbucket serves no pull request ref, and its 12-character hashes are
	// too short to fetch by. Base and head go in separate fetches because one
	// missing ref fails the whole fetch, and a fork's source branch lives in
	// another repository.
	fetch("refs/heads/" + p.BaseRef)
	fetch("refs/heads/" + p.HeadRef)
	return r, nil
}

func (s Source) resolve(ctx context.Context, repo pr.Repo) (*Repo, error) {
	local := &Repo{dir: s.Cwd}
	if top, err := local.git(ctx, "rev-parse", "--show-toplevel"); err == nil {
		local.dir = strings.TrimSpace(string(top))
		if local.remoteFor(ctx, repo) != cloneURL(repo) {
			return local, nil
		}
	}
	if s.CacheDir == "" {
		return nil, fmt.Errorf("gitrepo: %s is not checked out here and no cache dir is set", repo.Qualified())
	}
	mirror := &Repo{dir: filepath.Join(s.CacheDir, repo.Qualified()+".git")}
	if repo.Platform == pr.Bitbucket {
		// Without the token git would fall back to the user's helpers and
		// fail later with a less telling error, or succeed by accident.
		if s.BitbucketToken == "" {
			return nil, fmt.Errorf("gitrepo: %s is not checked out here and no Bitbucket API token is set to mirror it", repo.Qualified())
		}
		mirror.token = s.BitbucketToken
	}
	dir := mirror.dir
	if _, err := os.Stat(filepath.Join(dir, "HEAD")); err == nil {
		return mirror, nil
	}
	if err := os.MkdirAll(filepath.Dir(dir), 0o755); err != nil {
		return nil, err
	}
	// A full clone: grep over a blobless clone fetches blob by blob.
	// Clone beside the target and rename, so a cancelled clone never looks complete.
	tmp := dir + ".partial"
	_ = os.RemoveAll(tmp)
	if out, err := gitCommand(ctx, mirror.token, "clone", "--quiet", "--bare", cloneURL(repo), tmp).CombinedOutput(); err != nil {
		_ = os.RemoveAll(tmp)
		return nil, fmt.Errorf("mirror %s: %s", repo.Qualified(), strings.TrimSpace(string(out)))
	}
	if err := os.Rename(tmp, dir); err != nil {
		return nil, fmt.Errorf("mirror %s: %w", repo.Qualified(), err)
	}
	return mirror, nil
}

func cloneURL(repo pr.Repo) string { return repo.URL() + ".git" }

// Repo is a git repository on disk.
type Repo struct {
	dir   string
	token string // the Bitbucket API token, set only on a mirror of bitbucket.org
}

var _ review.Code = (*Repo)(nil)

func (r *Repo) Location() string { return r.dir }

func (r *Repo) HasCommit(ctx context.Context, sha string) bool {
	_, err := r.git(ctx, "cat-file", "-e", sha+"^{commit}")
	return err == nil
}

func (r *Repo) ReadFile(ctx context.Context, sha, path string) ([]byte, error) {
	return r.git(ctx, "cat-file", "blob", sha+":"+path)
}

func (r *Repo) ListDir(ctx context.Context, sha, path string) ([]string, error) {
	treeish := sha
	if path != "" {
		treeish = sha + ":" + path
	}
	// -z: without it git quotes a name with non-ASCII or special characters,
	// and the quoted name is not one ReadFile can open.
	out, err := r.git(ctx, "ls-tree", "-z", treeish)
	if err != nil {
		return nil, err
	}
	var names []string
	for _, entry := range strings.Split(strings.TrimSuffix(string(out), "\x00"), "\x00") {
		meta, name, ok := strings.Cut(entry, "\t")
		if !ok {
			continue
		}
		if strings.Contains(meta, " tree ") {
			name += "/"
		}
		names = append(names, name)
	}
	return names, nil
}

func (r *Repo) Grep(ctx context.Context, sha, pattern, path string, ignoreCase bool) ([]string, error) {
	// -z for the unquoted path, as in ListDir. It also ends the path and
	// the line number with NUL, so a match reads path NUL line NUL text LF.
	args := []string{"grep", "-z", "-n", "-I", "-E", "--max-count", "50"}
	if ignoreCase {
		args = append(args, "-i")
	}
	args = append(args, "-e", pattern, sha, "--")
	if path != "" {
		args = append(args, path)
	}
	out, err := r.git(ctx, args...)
	var exit *exec.ExitError
	if err != nil && errors.As(err, &exit) && exit.ExitCode() == 1 && len(out) == 0 {
		return nil, nil // git grep exits 1 when nothing matches
	}
	if err != nil {
		return nil, err
	}
	var lines []string
	for rest := string(out); rest != ""; {
		var file, n, text string
		file, rest, _ = strings.Cut(rest, "\x00")
		n, rest, _ = strings.Cut(rest, "\x00")
		text, rest, _ = strings.Cut(rest, "\n")
		lines = append(lines, strings.TrimPrefix(file, sha+":")+":"+n+":"+text)
	}
	return lines, nil
}

func (r *Repo) Log(ctx context.Context, sha, path string, limit int) (string, error) {
	args := []string{"log", "--no-color", "--format=%h %ad %an: %s", "--date=short", "-n", fmt.Sprint(limit), sha, "--"}
	if path != "" {
		args = append(args, path)
	}
	out, err := r.git(ctx, args...)
	return string(out), err
}

// remoteFor prefers a configured remote pointing at the repo (keeps the user's
// auth setup) and falls back to the clone URL.
func (r *Repo) remoteFor(ctx context.Context, repo pr.Repo) string {
	out, err := r.git(ctx, "remote", "-v")
	if err != nil {
		return cloneURL(repo)
	}
	for _, line := range strings.Split(string(out), "\n") {
		f := strings.Fields(line)
		if len(f) < 2 {
			continue
		}
		// Code hosts ignore case in owner and name, as sameRepo does.
		u := strings.ToLower(strings.TrimSuffix(strings.TrimSuffix(f[1], "/"), ".git"))
		slug := strings.ToLower(repo.FullName())
		if strings.Contains(u, strings.ToLower(repo.Hostname)) && (strings.HasSuffix(u, "/"+slug) || strings.HasSuffix(u, ":"+slug)) {
			return f[0]
		}
	}
	return cloneURL(repo)
}

// noPrompt stops git asking for credentials: a prompt would hang behind the
// TUI or draw over it.
func noPrompt() []string { return append(os.Environ(), "GIT_TERMINAL_PROMPT=0") }

type gitError struct {
	stderr string
	err    error
}

func (e *gitError) Error() string { return strings.TrimSpace(e.stderr + " " + e.err.Error()) }
func (e *gitError) Unwrap() error { return e.err }

// bitbucketURL scopes tokenHelper, so a redirect to another host is not
// offered the token. Tests point it at a local server.
var bitbucketURL = "https://bitbucket.org"

// tokenHelper answers git's credential requests with Bitbucket's git
// username for API tokens and the token in tokenEnv. It names the variable,
// so the token stays off every command line. printf, not echo: sh's echo may
// read backslashes in the token as escapes.
const (
	tokenEnv    = "REVIEW_ASSIST_GIT_TOKEN"
	tokenHelper = `!f() { printf 'username=x-bitbucket-api-token-auth\npassword=%s\n' "$` + tokenEnv + `"; }; f`
)

// gitCommand runs git with no prompt and, given a token, with tokenHelper as
// the only credential helper for bitbucketURL. -c lasts one invocation, so
// nothing is written to config. The empty helper first drops the user's and
// system's helpers for that URL, scoped or not, as the more specific key
// resets the list: otherwise one such as osxkeychain could answer first with
// a stale password, and git would hand the token to every helper to store.
func gitCommand(ctx context.Context, token string, args ...string) *exec.Cmd {
	env := noPrompt()
	if token != "" {
		key := "credential." + bitbucketURL + ".helper"
		args = append([]string{"-c", key + "=", "-c", key + "=" + tokenHelper}, args...)
		// The user's curl tracing would print the Authorization header, and
		// stderr ends up in errors shown in the TUI.
		env = slices.DeleteFunc(env, func(kv string) bool {
			name, _, _ := strings.Cut(kv, "=")
			return name == "GIT_TRACE_CURL" || name == "GIT_CURL_VERBOSE" || name == "GIT_TRACE_REDACT"
		})
		env = append(env, tokenEnv+"="+token)
	}
	cmd := exec.CommandContext(ctx, "git", args...)
	cmd.Env = env
	return cmd
}

func (r *Repo) git(ctx context.Context, args ...string) ([]byte, error) {
	return r.run(ctx, "", args...)
}

// fetch is the one command of Repo that talks to the code host.
func (r *Repo) fetch(ctx context.Context, args ...string) ([]byte, error) {
	return r.run(ctx, r.token, append([]string{"fetch"}, args...)...)
}

func (r *Repo) run(ctx context.Context, token string, args ...string) ([]byte, error) {
	cmd := gitCommand(ctx, token, append([]string{"-C", r.dir}, args...)...)
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		return stdout.Bytes(), &gitError{stderr: stderr.String(), err: err}
	}
	return stdout.Bytes(), nil
}
