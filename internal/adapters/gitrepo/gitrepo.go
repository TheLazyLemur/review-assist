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
	"strings"

	"github.com/TheLazyLemur/review-assist/internal/core/pr"
	"github.com/TheLazyLemur/review-assist/internal/core/review"
)

// Source picks the repository to read: the checkout at Cwd when it is a clone
// of the PR's repo, otherwise a bare mirror under CacheDir.
type Source struct {
	Cwd      string
	CacheDir string // e.g. os.UserCacheDir()/review-assist
}

var _ review.CodeSource = Source{}

func (s Source) Open(ctx context.Context, repo pr.Repo, number int, headSHA, baseSHA string) (review.Code, error) {
	if headSHA == "" || baseSHA == "" {
		return nil, fmt.Errorf("gitrepo: need head and base SHAs")
	}
	r, err := s.resolve(ctx, repo)
	if err != nil {
		return nil, err
	}
	if !r.HasCommit(ctx, headSHA) || !r.HasCommit(ctx, baseSHA) {
		remote := r.remoteFor(ctx, repo)
		// Errors are tolerated: the review reports missing commits and the
		// agents still work from the diff.
		_, _ = r.git(ctx, "fetch", "--quiet", "--no-tags", "--no-write-fetch-head", remote, fmt.Sprintf("refs/pull/%d/head", number))
		if !r.HasCommit(ctx, baseSHA) {
			_, _ = r.git(ctx, "fetch", "--quiet", "--no-tags", "--no-write-fetch-head", remote, baseSHA)
		}
	}
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
	dir := filepath.Join(s.CacheDir, repo.Qualified()+".git")
	if _, err := os.Stat(filepath.Join(dir, "HEAD")); err == nil {
		return &Repo{dir: dir}, nil
	}
	if err := os.MkdirAll(filepath.Dir(dir), 0o755); err != nil {
		return nil, err
	}
	// A full clone: grep over a blobless clone fetches blob by blob.
	// Clone beside the target and rename, so a cancelled clone never looks complete.
	tmp := dir + ".partial"
	_ = os.RemoveAll(tmp)
	clone := exec.CommandContext(ctx, "git", "clone", "--quiet", "--bare", cloneURL(repo), tmp)
	if out, err := clone.CombinedOutput(); err != nil {
		_ = os.RemoveAll(tmp)
		return nil, fmt.Errorf("mirror %s: %s", repo.Qualified(), strings.TrimSpace(string(out)))
	}
	if err := os.Rename(tmp, dir); err != nil {
		return nil, fmt.Errorf("mirror %s: %w", repo.Qualified(), err)
	}
	return &Repo{dir: dir}, nil
}

func cloneURL(repo pr.Repo) string { return repo.URL() + ".git" }

// Repo is a git repository on disk.
type Repo struct{ dir string }

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
	out, err := r.git(ctx, "ls-tree", treeish)
	if err != nil {
		return nil, err
	}
	var names []string
	for _, line := range strings.Split(strings.TrimSpace(string(out)), "\n") {
		meta, name, ok := strings.Cut(line, "\t")
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
	args := []string{"grep", "-n", "-I", "-E", "--max-count", "50"}
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
	lines := strings.Split(strings.TrimSpace(string(out)), "\n")
	for i, l := range lines {
		lines[i] = strings.TrimPrefix(l, sha+":")
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
		u := strings.TrimSuffix(strings.TrimSuffix(f[1], "/"), ".git")
		slug := repo.FullName()
		if strings.Contains(u, repo.Host) && (strings.HasSuffix(u, "/"+slug) || strings.HasSuffix(u, ":"+slug)) {
			return f[0]
		}
	}
	return cloneURL(repo)
}

type gitError struct {
	stderr string
	err    error
}

func (e *gitError) Error() string { return strings.TrimSpace(e.stderr + " " + e.err.Error()) }
func (e *gitError) Unwrap() error { return e.err }

func (r *Repo) git(ctx context.Context, args ...string) ([]byte, error) {
	cmd := exec.CommandContext(ctx, "git", append([]string{"-C", r.dir}, args...)...)
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		return stdout.Bytes(), &gitError{stderr: stderr.String(), err: err}
	}
	return stdout.Bytes(), nil
}
