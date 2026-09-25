// Package diff parses unified diffs (as produced by `gh pr diff`) into files,
// hunks and lines that know which side and line number GitHub uses for
// inline review comments.
package diff

import (
	"fmt"
	"strconv"
	"strings"
)

type Side string

const (
	Left  Side = "LEFT"  // the base version: removed lines
	Right Side = "RIGHT" // the head version: added and context lines
)

type Kind int

const (
	Context Kind = iota
	Add
	Del
)

type Line struct {
	Kind  Kind
	Text  string // without the leading +/-/space marker
	OldNo int    // 0 when the line does not exist on the base side
	NewNo int    // 0 when the line does not exist on the head side
}

// Target is where GitHub anchors an inline comment on this line.
func (l Line) Target() (Side, int) {
	if l.Kind == Del {
		return Left, l.OldNo
	}
	return Right, l.NewNo
}

type Hunk struct {
	Header string
	Lines  []Line
}

type File struct {
	OldPath   string
	NewPath   string
	Added     bool
	Deleted   bool
	Binary    bool
	Hunks     []Hunk
	Additions int
	Deletions int
}

// Path is the path to show and to comment against.
func (f File) Path() string {
	if f.Deleted {
		return f.OldPath
	}
	return f.NewPath
}

func Parse(s string) ([]File, error) {
	var files []File
	var cur *File
	var hunk *Hunk
	oldNo, newNo := 0, 0

	flushHunk := func() {
		if cur != nil && hunk != nil {
			cur.Hunks = append(cur.Hunks, *hunk)
		}
		hunk = nil
	}
	flushFile := func() {
		flushHunk()
		if cur != nil {
			files = append(files, *cur)
		}
		cur = nil
	}

	for n, raw := range strings.Split(s, "\n") {
		switch {
		case strings.HasPrefix(raw, "diff --git "):
			flushFile()
			oldP, newP := splitGitHeader(strings.TrimPrefix(raw, "diff --git "))
			cur = &File{OldPath: oldP, NewPath: newP}
			continue
		case cur == nil:
			continue
		}

		if hunk == nil || strings.HasPrefix(raw, "@@") {
			switch {
			case strings.HasPrefix(raw, "@@"):
				flushHunk()
				o, nn, err := parseHunkHeader(raw)
				if err != nil {
					return nil, fmt.Errorf("line %d: %w", n+1, err)
				}
				oldNo, newNo = o, nn
				hunk = &Hunk{Header: raw}
			case strings.HasPrefix(raw, "--- "):
				if p := strings.TrimPrefix(raw, "--- "); p == "/dev/null" {
					cur.Added = true
				} else {
					cur.OldPath = strings.TrimPrefix(p, "a/")
				}
			case strings.HasPrefix(raw, "+++ "):
				if p := strings.TrimPrefix(raw, "+++ "); p == "/dev/null" {
					cur.Deleted = true
				} else {
					cur.NewPath = strings.TrimPrefix(p, "b/")
				}
			case strings.HasPrefix(raw, "new file mode"):
				cur.Added = true
			case strings.HasPrefix(raw, "deleted file mode"):
				cur.Deleted = true
			case strings.HasPrefix(raw, "rename from "):
				cur.OldPath = strings.TrimPrefix(raw, "rename from ")
			case strings.HasPrefix(raw, "rename to "):
				cur.NewPath = strings.TrimPrefix(raw, "rename to ")
			case strings.HasPrefix(raw, "Binary files "):
				cur.Binary = true
			}
			continue
		}

		if raw == "" {
			// Trailing newline of the whole diff; a genuine blank context
			// line is " " (a single space).
			continue
		}
		switch raw[0] {
		case ' ':
			hunk.Lines = append(hunk.Lines, Line{Kind: Context, Text: raw[1:], OldNo: oldNo, NewNo: newNo})
			oldNo++
			newNo++
		case '+':
			hunk.Lines = append(hunk.Lines, Line{Kind: Add, Text: raw[1:], NewNo: newNo})
			newNo++
			cur.Additions++
		case '-':
			hunk.Lines = append(hunk.Lines, Line{Kind: Del, Text: raw[1:], OldNo: oldNo})
			oldNo++
			cur.Deletions++
		case '\\':
			// "\ No newline at end of file"
		default:
			return nil, fmt.Errorf("line %d: unexpected diff line %q", n+1, raw)
		}
	}
	flushFile()
	return files, nil
}

// splitGitHeader splits "a/x b/y". Paths may contain spaces, so the header is
// ambiguous in general; ---/+++ and rename lines override it when present.
func splitGitHeader(h string) (string, string) {
	i := strings.LastIndex(h, " b/")
	if i < 0 || !strings.HasPrefix(h, "a/") {
		return h, h
	}
	return h[2:i], h[i+3:]
}

// parseHunkHeader reads the starting line numbers from "@@ -a,b +c,d @@".
func parseHunkHeader(h string) (int, int, error) {
	fields := strings.Fields(h)
	if len(fields) < 3 || !strings.HasPrefix(fields[1], "-") || !strings.HasPrefix(fields[2], "+") {
		return 0, 0, fmt.Errorf("malformed hunk header %q", h)
	}
	start := func(f string) (int, error) {
		n, _, _ := strings.Cut(f[1:], ",")
		return strconv.Atoi(n)
	}
	o, err := start(fields[1])
	if err != nil {
		return 0, 0, fmt.Errorf("hunk header %q: %w", h, err)
	}
	nn, err := start(fields[2])
	if err != nil {
		return 0, 0, fmt.Errorf("hunk header %q: %w", h, err)
	}
	return o, nn, nil
}
