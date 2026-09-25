package gitrepo

import (
	"context"
	"errors"
	"fmt"
	"net/url"
	"os/exec"
	"strings"

	"github.com/TheLazyLemur/review-assist/internal/core/pr"
)

// Remote is a git remote and its fetch URL.
type Remote struct{ Name, URL string }

// PlatformOf says which platform a hostname is on; false when it is on no
// supported code host. An error means it could not tell.
type PlatformOf func(hostname string) (pr.Platform, bool, error)

// FindRemote picks the repository of the clone at dir from its git remotes,
// by PickRemote.
func FindRemote(ctx context.Context, dir string, platformOf PlatformOf) (pr.Repo, error) {
	r := &Repo{dir: dir}
	_, err := r.git(ctx, "rev-parse", "--git-dir")
	var exit *exec.ExitError
	if errors.As(err, &exit) && exit.ExitCode() == 128 {
		return pr.Repo{}, fmt.Errorf("%s is not in a git repository", dir)
	}
	if err != nil {
		return pr.Repo{}, fmt.Errorf("git rev-parse: %w", err)
	}
	out, err := r.git(ctx, "remote", "-v")
	if err != nil {
		return pr.Repo{}, fmt.Errorf("git remote: %w", err)
	}
	var remotes []Remote
	for _, line := range strings.Split(string(out), "\n") {
		f := strings.Fields(line)
		if len(f) == 3 && f[2] == "(fetch)" {
			remotes = append(remotes, Remote{Name: f[0], URL: f[1]})
		}
	}
	tracked, err := r.trackedRemote(ctx)
	if err != nil {
		return pr.Repo{}, err
	}
	return PickRemote(remotes, tracked, platformOf)
}

// trackedRemote is "" on a detached HEAD or a branch that tracks nothing.
func (r *Repo) trackedRemote(ctx context.Context) (string, error) {
	out, err := r.git(ctx, "branch", "--show-current")
	if err != nil {
		return "", fmt.Errorf("git branch: %w", err)
	}
	branch := strings.TrimSpace(string(out))
	if branch == "" {
		return "", nil
	}
	out, err = r.git(ctx, "config", "--get", "branch."+branch+".remote")
	var exit *exec.ExitError
	if errors.As(err, &exit) && exit.ExitCode() == 1 {
		return "", nil // git config exits 1 when the key is not set
	}
	if err != nil {
		return "", fmt.Errorf("git config: %w", err)
	}
	return strings.TrimSpace(string(out)), nil
}

// PickRemote applies the rule for the remote in CONTEXT.md: the first of the
// tracked remote, origin, and the only remote on a supported code host.
// remotes are in `git remote` order, which the refusal keeps. tracked is ""
// when the current branch tracks nothing. A hostname is looked up only when the
// rule reaches its remote, so a broken lookup for a remote the rule never needs
// does not stop it.
func PickRemote(remotes []Remote, tracked string, platformOf PlatformOf) (pr.Repo, error) {
	for _, want := range []string{tracked, "origin"} {
		for _, r := range remotes {
			if r.Name != want {
				continue
			}
			repo, ok, err := lookUp(r, platformOf)
			if err != nil || ok {
				return repo, err
			}
		}
	}
	var candidates []pr.Repo
	var onHosts, ignored []listed
	for _, r := range remotes {
		repo, ok, err := lookUp(r, platformOf)
		if err != nil {
			return pr.Repo{}, err
		}
		if ok {
			candidates = append(candidates, repo)
			onHosts = append(onHosts, listed{r.Name, repo.Qualified()})
		} else {
			ignored = append(ignored, listed{r.Name, shownURL(r.URL)})
		}
	}
	switch len(candidates) {
	case 0:
		return pr.Repo{}, refusal("no git remote points at a supported code host (GitHub, Bitbucket)", ignored)
	case 1:
		return candidates[0], nil
	}
	return pr.Repo{}, refusal("several git remotes point at code hosts; check out a branch that tracks one", onHosts)
}

type listed struct{ remote, where string }

// refusal lists the remotes under the reason, names padded to one column.
func refusal(reason string, remotes []listed) error {
	width := 0
	for _, r := range remotes {
		width = max(width, len(r.remote))
	}
	var b strings.Builder
	b.WriteString(reason)
	for _, r := range remotes {
		fmt.Fprintf(&b, "\n  %-*s  %s", width, r.remote, r.where)
	}
	return errors.New(b.String())
}

// shownURL is HOSTNAME/OWNER/NAME as parsed, so an SSH host alias shows up as
// the hostname review-assist saw, or the raw URL when it does not parse.
func shownURL(raw string) string {
	hostname, owner, name, ok := parseRemoteURL(raw)
	if !ok {
		return raw
	}
	return hostname + "/" + owner + "/" + name
}

// lookUp is false for a remote that is not on a supported code host.
func lookUp(r Remote, platformOf PlatformOf) (pr.Repo, bool, error) {
	hostname, owner, name, ok := parseRemoteURL(r.URL)
	if !ok {
		return pr.Repo{}, false, nil
	}
	platform, ok, err := platformOf(hostname)
	if err != nil {
		return pr.Repo{}, false, fmt.Errorf("%s: %w", hostname, err)
	}
	if !ok {
		return pr.Repo{}, false, nil
	}
	return pr.Repo{Platform: platform, Hostname: hostname, Owner: owner, Name: name}, true, nil
}

// parseRemoteURL reads git@host:owner/name.git, ssh://git@host:port/owner/name.git
// and https://[user@]host/owner/name.git. Anything else, such as a local path
// or a path deeper than owner/name, is not on a code host.
func parseRemoteURL(raw string) (hostname, owner, name string, ok bool) {
	var path string
	if strings.Contains(raw, "://") {
		u, err := url.Parse(raw)
		if err != nil {
			return "", "", "", false
		}
		hostname, path = u.Hostname(), u.Path
	} else {
		host, p, found := strings.Cut(raw, ":")
		// A slash before the colon makes it a local path, not scp-like syntax.
		if !found || strings.Contains(host, "/") {
			return "", "", "", false
		}
		hostname, path = host[strings.LastIndex(host, "@")+1:], p
	}
	path = strings.TrimSuffix(strings.Trim(path, "/"), ".git")
	owner, name, found := strings.Cut(path, "/")
	if hostname == "" || !found || owner == "" || name == "" || strings.Contains(name, "/") {
		return "", "", "", false
	}
	return strings.ToLower(hostname), owner, name, true
}
