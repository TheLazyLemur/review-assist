package review

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/TheLazyLemur/review-assist/internal/core/diff"
)

const maxToolOutput = 24_000

var refProp = map[string]any{
	"type":        "string",
	"enum":        []string{"head", "base"},
	"description": "head = the PR's version (default), base = the target branch before the PR",
}

type toolSpec struct {
	Name        string
	Description string
	Properties  map[string]any
	Required    []string
}

// readTools are the exploration tools. All are read-only.
var readTools = []toolSpec{
	{
		Name:        "list_changed_files",
		Description: "List the files the PR changes with their status and added/removed line counts.",
		Properties:  map[string]any{},
	},
	{
		Name: "get_diff",
		Description: "Get the PR diff for one changed file (or all files if path is omitted). " +
			"Each line is prefixed with its anchor: H<n> for lines on the head side (added '+' or context ' '), " +
			"B<n> for removed '-' lines on the base side. Use these anchors for findings.",
		Properties: map[string]any{
			"path": map[string]any{"type": "string", "description": "changed file path; omit for the whole PR"},
		},
	},
	{
		Name:        "read_file",
		Description: "Read a file of the repository at the PR head or base, with line numbers. Returns at most 400 lines per call.",
		Properties: map[string]any{
			"path":       map[string]any{"type": "string"},
			"ref":        refProp,
			"start_line": map[string]any{"type": "integer", "description": "1-based, default 1"},
			"end_line":   map[string]any{"type": "integer", "description": "inclusive, default start_line+399"},
		},
		Required: []string{"path"},
	},
	{
		Name:        "list_dir",
		Description: "List entries of a directory of the repository at the PR head or base. Use \"\" or \".\" for the root.",
		Properties: map[string]any{
			"path": map[string]any{"type": "string"},
			"ref":  refProp,
		},
	},
	{
		Name:        "grep",
		Description: "Search the repository at the PR head or base with an extended regular expression. Returns file:line:text matches (max 200).",
		Properties: map[string]any{
			"pattern":     map[string]any{"type": "string"},
			"ref":         refProp,
			"path":        map[string]any{"type": "string", "description": "optional directory or file to limit the search, e.g. internal/ or *.go"},
			"ignore_case": map[string]any{"type": "boolean"},
		},
		Required: []string{"pattern"},
	},
	{
		Name:        "git_log",
		Description: "Recent commit history (subject lines) at the PR head, optionally for one path. Use to understand intent and recent changes.",
		Properties: map[string]any{
			"path":  map[string]any{"type": "string"},
			"limit": map[string]any{"type": "integer", "description": "default 20, max 50"},
		},
	},
	{
		Name:        "pr_description",
		Description: "The PR title and description written by the author.",
		Properties:  map[string]any{},
	},
}

var findingProps = map[string]any{
	"path":              map[string]any{"type": "string", "description": "changed file path"},
	"line":              map[string]any{"type": "integer", "description": "line number from the H<n>/B<n> anchor in get_diff"},
	"side":              map[string]any{"type": "string", "enum": []string{"head", "base"}, "description": "head for H<n> anchors, base for B<n> anchors"},
	"severity":          map[string]any{"type": "string", "enum": []string{"blocking", "major", "minor"}},
	"confidence":        map[string]any{"type": "string", "enum": []string{"high", "medium", "low"}},
	"lens":              map[string]any{"type": "string"},
	"title":             map[string]any{"type": "string", "description": "one short line: what is wrong"},
	"explanation":       map[string]any{"type": "string", "description": "2-4 plain sentences a busy reviewer can digest: the concrete scenario that triggers it and what goes wrong"},
	"suggested_comment": map[string]any{"type": "string", "description": "a draft review comment the human MAY post themselves; polite, specific, with a fix direction"},
}

var submitTool = toolSpec{
	Name: "submit_findings",
	Description: "Submit your final findings and end the review. Call exactly once. " +
		"Submit an empty list if you found nothing real.",
	Properties: map[string]any{
		"findings": map[string]any{
			"type": "array",
			"items": map[string]any{
				"type":       "object",
				"properties": findingProps,
				"required":   []string{"path", "line", "side", "severity", "confidence", "title", "explanation", "suggested_comment"},
			},
		},
		"assessment": map[string]any{"type": "string", "description": "verifier only: 2-3 lines on whether the PR is ready to merge, and what blocks it if not"},
	},
	Required: []string{"findings"},
}

type submission struct {
	Findings   []Finding `json:"findings"`
	Assessment string    `json:"assessment"`
}

// exec runs one read tool. Errors are returned as text for the model.
func (w *Subject) exec(ctx context.Context, name string, raw json.RawMessage) (string, error) {
	var in struct {
		Path       string `json:"path"`
		Ref        string `json:"ref"`
		StartLine  int    `json:"start_line"`
		EndLine    int    `json:"end_line"`
		Pattern    string `json:"pattern"`
		IgnoreCase bool   `json:"ignore_case"`
		Limit      int    `json:"limit"`
	}
	if len(raw) > 0 {
		if err := json.Unmarshal(raw, &in); err != nil {
			return "", fmt.Errorf("invalid arguments: %w", err)
		}
	}
	switch name {
	case "list_changed_files":
		return w.listChanged(), nil
	case "get_diff":
		return w.renderDiff(in.Path)
	case "pr_description":
		return fmt.Sprintf("# %s\n\n%s", w.Title, w.Body), nil
	case "read_file":
		return w.readFile(ctx, in.Path, in.Ref, in.StartLine, in.EndLine)
	case "list_dir":
		return w.listDir(ctx, in.Path, in.Ref)
	case "grep":
		return w.grep(ctx, in.Pattern, in.Ref, in.Path, in.IgnoreCase)
	case "git_log":
		return w.log(ctx, in.Path, in.Limit)
	default:
		return "", fmt.Errorf("unknown tool %q", name)
	}
}

func (w *Subject) listChanged() string {
	var b strings.Builder
	for _, f := range w.Files {
		status := "modified"
		switch {
		case f.Deleted:
			status = "deleted"
		case f.OldPath != f.NewPath:
			status = "renamed from " + f.OldPath
		case f.Added:
			status = "added"
		}
		if f.Binary {
			status += ", binary"
		}
		fmt.Fprintf(&b, "%s (%s) +%d -%d\n", f.Path(), status, f.Additions, f.Deletions)
	}
	return b.String()
}

// RenderAnnotated renders one file's diff with H<n>/B<n> anchors.
func RenderAnnotated(f diff.File) string {
	var b strings.Builder
	fmt.Fprintf(&b, "=== %s\n", f.Path())
	if f.Binary {
		b.WriteString("(binary file)\n")
	}
	for _, h := range f.Hunks {
		b.WriteString(h.Header + "\n")
		for _, l := range h.Lines {
			side, n := l.Target()
			marker := map[diff.Kind]string{diff.Context: " ", diff.Add: "+", diff.Del: "-"}[l.Kind]
			fmt.Fprintf(&b, "%s%-5d %s%s\n", strings.ToUpper(string(side[:1])), n, marker, l.Text)
		}
	}
	return b.String()
}

func (w *Subject) renderDiff(path string) (string, error) {
	if path != "" {
		f, ok := w.file(path)
		if !ok {
			return "", fmt.Errorf("%q is not a changed file; call list_changed_files", path)
		}
		return truncate(RenderAnnotated(f)), nil
	}
	var b strings.Builder
	for _, f := range w.Files {
		b.WriteString(RenderAnnotated(f))
	}
	return truncate(b.String()), nil
}

func (w *Subject) commit(ref string) (string, error) {
	switch ref {
	case "", "head":
		if !w.headOK {
			return "", fmt.Errorf("the PR head commit is not available locally; use get_diff")
		}
		return w.HeadSHA, nil
	case "base":
		if !w.baseOK {
			return "", fmt.Errorf("the base commit is not available locally")
		}
		return w.BaseSHA, nil
	default:
		return "", fmt.Errorf("ref must be head or base")
	}
}

// cleanPath rejects anything that could be read as an option or escape the tree.
func cleanPath(p string) (string, error) {
	p = strings.TrimPrefix(strings.TrimSpace(p), "./")
	if p == "." {
		p = ""
	}
	if strings.HasPrefix(p, "-") || strings.HasPrefix(p, "/") || strings.Contains(p, "..") || strings.ContainsAny(p, "\x00\n") {
		return "", fmt.Errorf("invalid path %q: use a repository-relative path", p)
	}
	return p, nil
}

func (w *Subject) readFile(ctx context.Context, path, ref string, start, end int) (string, error) {
	sha, err := w.commit(ref)
	if err != nil {
		return "", err
	}
	path, err = cleanPath(path)
	if err != nil || path == "" {
		return "", fmt.Errorf("read_file needs a file path")
	}
	out, err := w.code.ReadFile(ctx, sha, path)
	if err != nil {
		return "", fmt.Errorf("cannot read %s at %s: %v", path, refName(ref), err)
	}
	lines := strings.Split(string(out), "\n")
	if start < 1 {
		start = 1
	}
	if end < start || end-start >= 400 {
		end = start + 399
	}
	if end > len(lines) {
		end = len(lines)
	}
	if start > len(lines) {
		return "", fmt.Errorf("%s has only %d lines", path, len(lines))
	}
	var b strings.Builder
	for i := start; i <= end; i++ {
		fmt.Fprintf(&b, "%5d  %s\n", i, lines[i-1])
	}
	if end < len(lines) {
		fmt.Fprintf(&b, "... (%d more lines; call again with start_line=%d)\n", len(lines)-end, end+1)
	}
	return truncate(b.String()), nil
}

func (w *Subject) listDir(ctx context.Context, path, ref string) (string, error) {
	sha, err := w.commit(ref)
	if err != nil {
		return "", err
	}
	path, err = cleanPath(path)
	if err != nil {
		return "", err
	}
	names, err := w.code.ListDir(ctx, sha, strings.TrimSuffix(path, "/"))
	if err != nil {
		return "", fmt.Errorf("cannot list %q: %v", path, err)
	}
	return truncate(strings.Join(names, "\n")), nil
}

func (w *Subject) grep(ctx context.Context, pattern, ref, path string, ignoreCase bool) (string, error) {
	sha, err := w.commit(ref)
	if err != nil {
		return "", err
	}
	if pattern == "" {
		return "", fmt.Errorf("pattern is required")
	}
	path, err = cleanPath(path)
	if err != nil {
		return "", err
	}
	lines, err := w.code.Grep(ctx, sha, pattern, path, ignoreCase)
	if err != nil {
		return "", fmt.Errorf("grep failed: %v", err)
	}
	if len(lines) == 0 {
		return "no matches", nil
	}
	if len(lines) > 200 {
		lines = append(lines[:200], fmt.Sprintf("... (%d more matches; narrow the pattern or path)", len(lines)-200))
	}
	return truncate(strings.Join(lines, "\n")), nil
}

func (w *Subject) log(ctx context.Context, path string, limit int) (string, error) {
	sha, err := w.commit("head")
	if err != nil {
		return "", err
	}
	path, err = cleanPath(path)
	if err != nil {
		return "", err
	}
	if limit <= 0 {
		limit = 20
	}
	if limit > 50 {
		limit = 50
	}
	out, err := w.code.Log(ctx, sha, path, limit)
	if err != nil {
		return "", fmt.Errorf("git log failed: %v", err)
	}
	return truncate(out), nil
}

func refName(ref string) string {
	if ref == "" {
		return "head"
	}
	return ref
}

func truncate(s string) string {
	if len(s) <= maxToolOutput {
		return s
	}
	return s[:maxToolOutput] + "\n... (output truncated; narrow the request)"
}
