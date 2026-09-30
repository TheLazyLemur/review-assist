package main

import (
	"encoding/json"
	"fmt"
	"regexp"
	"slices"
	"sort"
	"strings"
)

const (
	labelSlice     = "slice"
	labelReady     = "ready"
	labelUnplanned = "unplanned"
)

var (
	retiredLabels = []string{"needs-triage", "ready-for-agent"}
	taskStatuses  = []string{"todo", "ready", "doing", "done", "dropped"}
	sliceStatuses = []string{"done", "dropped"}
	relations     = []string{"depends-on", "relates-to", "discovered-during"}
)

// Dep is one blocker. Repo is empty for an issue in this repository; GitHub
// also lets an issue depend on one in another repository, whose number means
// nothing here.
type Dep struct {
	Repo       string
	Number     int
	Closed     bool
	NotPlanned bool
}

func (d Dep) Local() bool { return d.Repo == "" }

func (d Dep) String() string {
	if d.Local() {
		return fmt.Sprintf("#%d", d.Number)
	}
	return fmt.Sprintf("%s#%d", d.Repo, d.Number)
}

type Issue struct {
	Number     int
	Title      string
	Body       string
	ID         int64 // database id, which the sub-issue and dependency endpoints take
	Closed     bool
	NotPlanned bool
	Labels     []string
	Assignees  []string
	Parent     int // 0: none
	Deps       []Dep
}

func (i *Issue) Has(label string) bool { return slices.Contains(i.Labels, label) }
func (i *Issue) IsSlice() bool         { return i.Has(labelSlice) }

func (i *Issue) Status() string {
	switch {
	case i.Closed && i.NotPlanned:
		return "dropped"
	case i.Closed:
		return "done"
	case i.IsSlice():
		return "open"
	case len(i.Assignees) > 0:
		return "doing"
	case i.Has(labelReady):
		return "ready"
	default:
		return "todo"
	}
}

// LocalDeps are the numbers of the blockers in this repository: the edges of
// the graph.
func (i *Issue) LocalDeps() []int {
	var out []int
	for _, d := range i.Deps {
		if d.Local() {
			out = append(out, d.Number)
		}
	}
	return out
}

func (i *Issue) DependsOn(n int) bool { return slices.Contains(i.LocalDeps(), n) }

type Model map[int]*Issue

func (m Model) Sorted() []*Issue {
	out := make([]*Issue, 0, len(m))
	for _, i := range m {
		out = append(out, i)
	}
	sort.Slice(out, func(a, b int) bool { return out[a].Number < out[b].Number })
	return out
}

// ---- loading ---------------------------------------------------------------

const query = `query($owner: String!, $repo: String!, $endCursor: String) {
  repository(owner: $owner, name: $repo) {
    nameWithOwner
    issues(first: 100, after: $endCursor, states: [OPEN, CLOSED]) {
      pageInfo { hasNextPage endCursor }
      nodes {
        number title body state stateReason databaseId
        labels(first: 20) { nodes { name } }
        assignees(first: 5) { nodes { login } }
        parent { number repository { nameWithOwner } }
        blockedBy(first: 50) { nodes { number state stateReason repository { nameWithOwner } } }
      }
    }
  }
}`

type page struct {
	Data struct {
		Repository struct {
			NameWithOwner string
			Issues        struct {
				Nodes []struct {
					Number      int
					Title       string
					Body        string
					State       string
					StateReason string
					DatabaseID  int64
					Labels      struct{ Nodes []struct{ Name string } }
					Assignees   struct{ Nodes []struct{ Login string } }
					Parent      *struct {
						Number     int
						Repository struct{ NameWithOwner string }
					}
					BlockedBy struct {
						Nodes []struct {
							Number      int
							State       string
							StateReason string
							Repository  struct{ NameWithOwner string }
						}
					}
				}
			}
		}
	}
}

func parseIssues(raw []byte) (Model, error) {
	var pages []page
	if err := json.Unmarshal(raw, &pages); err != nil {
		return nil, fmt.Errorf("decode issues: %w", err)
	}
	m := Model{}
	for _, p := range pages {
		own := p.Data.Repository.NameWithOwner
		if own == "" {
			return nil, fmt.Errorf("decode issues: the query returned no repository name")
		}
		for _, n := range p.Data.Repository.Issues.Nodes {
			i := &Issue{
				Number: n.Number, Title: n.Title, Body: n.Body, ID: n.DatabaseID,
				Closed: n.State == "CLOSED", NotPlanned: n.StateReason == "NOT_PLANNED",
			}
			for _, l := range n.Labels.Nodes {
				i.Labels = append(i.Labels, l.Name)
			}
			for _, a := range n.Assignees.Nodes {
				i.Assignees = append(i.Assignees, a.Login)
			}
			if n.Parent != nil && n.Parent.Repository.NameWithOwner == own {
				i.Parent = n.Parent.Number
			}
			for _, b := range n.BlockedBy.Nodes {
				d := Dep{Number: b.Number, Closed: b.State == "CLOSED", NotPlanned: b.StateReason == "NOT_PLANNED"}
				if b.Repository.NameWithOwner != own {
					d.Repo = b.Repository.NameWithOwner
				}
				i.Deps = append(i.Deps, d)
			}
			sort.Slice(i.Deps, func(a, b int) bool {
				if i.Deps[a].Repo != i.Deps[b].Repo {
					return i.Deps[a].Repo < i.Deps[b].Repo
				}
				return i.Deps[a].Number < i.Deps[b].Number
			})
			m[i.Number] = i
		}
	}
	return m, nil
}

// ---- the graph -------------------------------------------------------------

// tasksOf lists a slice's tasks, or with 0 the unplanned ones.
func (m Model) tasksOf(slice int) []*Issue {
	var out []*Issue
	for _, i := range m.Sorted() {
		if slice == 0 && !i.IsSlice() && i.Parent == 0 || slice != 0 && i.Parent == slice {
			out = append(out, i)
		}
	}
	return out
}

// unmet lists the blockers not done. A dropped one never will be, so it counts.
// A blocker in this repository is judged by its issue here; one elsewhere by
// the state GitHub reports for it.
func (m Model) unmet(i *Issue) []Dep {
	var out []Dep
	for _, d := range i.Deps {
		if !d.Local() {
			if !d.Closed || d.NotPlanned {
				out = append(out, d)
			}
			continue
		}
		if dep, ok := m[d.Number]; ok && dep.Status() != "done" {
			out = append(out, d)
		}
	}
	return out
}

func (m Model) frontier(slice int) []int {
	var out []int
	for _, t := range m.tasksOf(slice) {
		if s := t.Status(); (s == "todo" || s == "ready") && len(m.unmet(t)) == 0 {
			out = append(out, t.Number)
		}
	}
	return out
}

// cycles finds each dependency cycle once, whatever issue it is entered from.
func (m Model) cycles() [][]int {
	var found [][]int
	state := map[int]int{} // 1: on the current path, 2: finished
	var walk func(n int, path []int)
	walk = func(n int, path []int) {
		switch state[n] {
		case 1:
			at := slices.Index(path, n)
			found = append(found, append(slices.Clone(path[at:]), n))
			return
		case 2:
			return
		}
		state[n] = 1
		if i, ok := m[n]; ok {
			for _, d := range i.LocalDeps() {
				walk(d, append(slices.Clone(path), n))
			}
		}
		state[n] = 2
	}
	for _, i := range m.Sorted() {
		walk(i.Number, nil)
	}
	var out [][]int
	seen := map[string]bool{}
	for _, c := range found {
		key := slices.Clone(c[:len(c)-1])
		slices.Sort(key)
		k := fmt.Sprint(key)
		if !seen[k] {
			seen[k] = true
			out = append(out, c)
		}
	}
	return out
}

func (m Model) reaches(start, goal int) bool {
	todo, seen := []int{start}, map[int]bool{}
	for len(todo) > 0 {
		n := todo[len(todo)-1]
		todo = todo[:len(todo)-1]
		if n == goal {
			return true
		}
		if i, ok := m[n]; ok && !seen[n] {
			seen[n] = true
			todo = append(todo, i.LocalDeps()...)
		}
	}
	return false
}

// ---- bodies ----------------------------------------------------------------

var (
	demoRe     = regexp.MustCompile(`(?s)\*\*Demo\.?\*\*[ \t]*(.*?)(?:\n\n|\n##|\z)`)
	doneWhenRe = regexp.MustCompile(`(?s)\*\*Done when\.?\*\*(.*?)(?:\n## |\z)`)
	boxRe      = regexp.MustCompile(`(?m)^- \[([ xX])\] (.+)$`)
	tasksHead  = regexp.MustCompile(`(?m)^## Tasks\s*$`)
	// Must match wherever tasksHead does, including a heading that ends the body.
	tasksBlock = regexp.MustCompile(`(?ms)^## Tasks\s*(?:\n.*?)?(?:^## |\z)`)
	startable  = regexp.MustCompile(`(?m)^Startable now: .*$`)
	blankRuns  = regexp.MustCompile(`\n{3,}`)

	// Words that promise a quality rather than name an observation.
	vagueRe      = regexp.MustCompile(`(?i)\b(correctly|properly|works?|good|clean|robust|appropriate|reasonable|as expected|sensible|nicely)\b`)
	observableRe = regexp.MustCompile("(?i)`|\\b(returns?|prints?|exits?|fails?|passes|contains?|refuses?|stops?|blocks?|shows?|rejects?|" +
		`accepts?|applies|matches|resolves?|covers?|posts?|lists?|opens?|reads?|writes?)\b`)
	andRe = regexp.MustCompile(`\sand\s`)
)

const noTasksYet = "_None yet. Run `to-tasks`._"

func demoOf(body string) string {
	if m := demoRe.FindStringSubmatch(body); m != nil {
		return strings.Join(strings.Fields(m[1]), " ")
	}
	return ""
}

type criterion struct {
	Checked bool
	Text    string
}

func criteriaOf(body string) []criterion {
	m := doneWhenRe.FindStringSubmatch(body)
	if m == nil {
		return nil
	}
	var out []criterion
	for _, b := range boxRe.FindAllStringSubmatch(m[1], -1) {
		out = append(out, criterion{Checked: strings.EqualFold(b[1], "x"), Text: strings.TrimSpace(b[2])})
	}
	return out
}

func flags(c string) []string {
	var out []string
	if vagueRe.MatchString(c) {
		out = append(out, "vague word")
	}
	if andRe.MatchString(c) && !strings.HasPrefix(c, "Given") {
		out = append(out, "two things joined by 'and' — split")
	}
	if !observableRe.MatchString(c) {
		out = append(out, "names nothing to run or read")
	}
	return out
}

func sliceBody(goal, demo string, implements, outOfScope, open []string) string {
	items := func(xs []string) string {
		if len(xs) == 0 {
			return "None."
		}
		var b strings.Builder
		for n, x := range xs {
			if n > 0 {
				b.WriteString("\n")
			}
			b.WriteString("- " + x)
		}
		return b.String()
	}
	return fmt.Sprintf("**Goal.** %s\n\n**Demo.** %s\n\n## Implements\n\n%s\n\n## Out of scope\n\n%s\n\n"+
		"## Open in this slice\n\n%s\n\n## Tasks\n\n%s\n\n## Notes\n",
		goal, demo, items(implements), items(outOfScope), items(open), noTasksYet)
}

func taskBody(what string, criteria []string, note string) string {
	boxes := make([]string, len(criteria))
	for n, c := range criteria {
		boxes[n] = "- [ ] " + c
	}
	body := fmt.Sprintf("**What.** %s\n\n**Done when.**\n\n%s\n\n## Notes\n", what, strings.Join(boxes, "\n"))
	if note != "" {
		body += "\n" + note + "\n"
	}
	return body
}

func (m Model) mermaid(slice int) string {
	own := map[int]bool{}
	tasks := m.tasksOf(slice)
	for _, t := range tasks {
		own[t.Number] = true
	}
	id := func(d Dep) string {
		if d.Local() {
			return fmt.Sprintf("t%d", d.Number)
		}
		return "x_" + strings.NewReplacer("/", "_", "-", "_", ".", "_").Replace(d.Repo) + fmt.Sprintf("_%d", d.Number)
	}
	label := func(d Dep) string {
		title := "?"
		if d.Local() {
			if i, ok := m[d.Number]; ok {
				title = i.Title
			}
		} else {
			title = "in another repository"
		}
		return strings.ReplaceAll(d.String()+" "+title, `"`, "#quot;")
	}
	var ext []Dep
	seen := map[string]bool{}
	for _, t := range tasks {
		for _, d := range t.Deps {
			if (!d.Local() || !own[d.Number]) && !seen[d.String()] {
				seen[d.String()] = true
				ext = append(ext, d)
			}
		}
	}
	sort.Slice(ext, func(a, b int) bool {
		if ext[a].Repo != ext[b].Repo {
			return ext[a].Repo < ext[b].Repo
		}
		return ext[a].Number < ext[b].Number
	})
	lines := []string{"```mermaid", "flowchart LR"}
	for _, d := range ext {
		lines = append(lines, fmt.Sprintf(`  %s["%s"]:::external`, id(d), label(d)))
	}
	for _, t := range tasks {
		lines = append(lines, fmt.Sprintf(`  t%d["%s"]`, t.Number, label(Dep{Number: t.Number})))
	}
	for _, t := range tasks {
		for _, d := range t.Deps {
			lines = append(lines, fmt.Sprintf("  %s --> t%d", id(d), t.Number))
		}
	}
	if len(ext) > 0 {
		lines = append(lines, "  classDef external stroke-dasharray: 4 4")
	}
	return strings.Join(append(lines, "```"), "\n")
}

func (m Model) tasksSection(slice int) string {
	tasks := m.tasksOf(slice)
	if len(tasks) == 0 {
		return "## Tasks\n\n" + noTasksYet + "\n\n"
	}
	links := make([]string, len(tasks))
	for n, t := range tasks {
		links[n] = fmt.Sprintf("- #%d", t.Number)
	}
	start := "nothing: every open task has an unmet dependency or is in progress"
	if free := m.frontier(slice); len(free) > 0 {
		start = hashes(free)
	}
	return "## Tasks\n\n" +
		"Each task is a sub-issue. `depends-on` says when each can start; there is no order beyond that.\n\n" +
		strings.Join(links, "\n") + "\n\nStartable now: " + start + ".\n\n" + m.mermaid(slice) + "\n\n"
}

func syncedBody(body, section string) (string, error) {
	if !tasksHead.MatchString(body) {
		return "", refused("the slice body has no '## Tasks' section")
	}
	loc := tasksBlock.FindStringIndex(body)
	end := loc[1]
	if strings.HasSuffix(body[loc[0]:end], "## ") {
		end -= len("## ") // the next heading starts there; keep it
	}
	return body[:loc[0]] + section + body[end:], nil
}

// structure drops the "Startable now" line, which changes with progress, not
// with the plan.
func structure(body string) string { return startable.ReplaceAllString(body, "") }

func linkLine(relation string, other int) string {
	if relation == "relates-to" {
		return fmt.Sprintf("Related: #%d", other)
	}
	return fmt.Sprintf("Discovered during #%d", other)
}

func hashes(ns []int) string {
	out := make([]string, len(ns))
	for n, x := range ns {
		out[n] = fmt.Sprintf("#%d", x)
	}
	return strings.Join(out, ", ")
}

func waitsOn(deps []Dep) string {
	names := make([]string, len(deps))
	for n, d := range deps {
		names[n] = d.String()
	}
	if len(names) <= 3 {
		return strings.Join(names, ", ")
	}
	return fmt.Sprintf("%s +%d more", strings.Join(names[:2], ", "), len(names)-2)
}

// ---- checks ----------------------------------------------------------------

func (m Model) validate() []string {
	var errs []string
	add := func(f string, a ...any) { errs = append(errs, fmt.Sprintf(f, a...)) }
	for _, i := range m.Sorted() {
		n := i.Number
		for _, l := range retiredLabels {
			if i.Has(l) {
				add("#%d: carries the retired label '%s'", n, l)
			}
		}
		if i.IsSlice() {
			if i.Parent != 0 {
				add("#%d: a slice cannot be a task of #%d", n, i.Parent)
			}
			if i.Has(labelReady) {
				add("#%d: a slice cannot be ready; readiness belongs to tasks", n)
			}
			if i.Has(labelUnplanned) {
				add("#%d: a slice cannot be unplanned", n)
			}
			if demoOf(i.Body) == "" {
				add("#%d: slice has no demo", n)
			}
			if !i.Closed && tasksHead.MatchString(i.Body) {
				if synced, _ := syncedBody(i.Body, m.tasksSection(n)); structure(synced) != structure(i.Body) {
					add("#%d: the Tasks section is out of date; run: ./tracker slice.sync %d", n, n)
				}
			}
			continue
		}
		if i.Parent == 0 && !i.Has(labelUnplanned) {
			add("#%d: no slice and not labelled unplanned", n)
		}
		if i.Parent != 0 && i.Has(labelUnplanned) {
			add("#%d: labelled unplanned but is a task of #%d", n, i.Parent)
		}
		if p, ok := m[i.Parent]; i.Parent != 0 && ok && !p.IsSlice() {
			add("#%d: its parent #%d is not labelled slice", n, i.Parent)
		}
		for _, d := range i.Deps {
			dropped := d.Closed && d.NotPlanned
			if dep, ok := m[d.Number]; d.Local() && ok {
				if dep.IsSlice() {
					add("#%d: depends on slice #%d; dependencies are between tasks", n, d.Number)
					continue
				}
				dropped = dep.Status() == "dropped"
			}
			if !i.Closed && dropped {
				add("#%d: depends on %s, which was dropped", n, d)
			}
		}
		crit := criteriaOf(i.Body)
		if i.Status() == "doing" && !i.Has(labelReady) {
			add("#%d: in progress but never marked ready", n)
		}
		if s := i.Status(); (s == "ready" || s == "doing") && len(crit) == 0 {
			add("#%d: %s but has no acceptance criteria", n, s)
		}
		if i.Status() == "done" {
			for _, c := range crit {
				if !c.Checked {
					add("#%d: closed as done with an unchecked criterion: %s", n, c.Text)
				}
			}
		}
	}
	for _, c := range m.cycles() {
		errs = append(errs, "dependency cycle: "+strings.ReplaceAll(hashes(c), ", ", " -> "))
	}
	return errs
}
