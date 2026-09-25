// Command tracker reads and writes slices and tasks in this repo's GitHub
// Issues. Run it as ./tracker from the repo; ./board runs its board.
//
// A slice is an issue labelled slice: one outcome you can demo. A task is one
// mergeable pull request: a sub-issue of its slice, or an issue labelled
// unplanned when it has none. depends-on (a native GitHub dependency) is the
// only order between tasks.
//
// Every write refuses what GitHub would accept but the model forbids: a pull
// request number, a dependency cycle, starting a task that is not ready or
// still blocked, closing a task with unchecked criteria, closing a slice over
// open tasks, dropping without a reason. Reads load the whole repo in one
// query.
//
// Set TRACKER_DRY_RUN=1 to print the writes instead of running them.
package main

import (
	"bytes"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"os/exec"
	"regexp"
	"slices"
	"strconv"
	"strings"

	"golang.org/x/term"
)

type refusal struct{ msg string }

func (r refusal) Error() string { return r.msg }

func refused(f string, a ...any) error { return refusal{fmt.Sprintf(f, a...)} }

// GitHub is every read and write the tracker makes.
type GitHub interface {
	Load() (Model, error)
	Create(title, body string, labels []string) (int, error)
	Attach(parent, child int) error
	AddDep(n, dep int) error
	RemoveDep(n, dep int) error
	Edit(n int, e Edit) error
	Close(n int, reason, comment string) error
	Reopen(n int) error
	Comment(n int, body string) error
	Delete(n int) error
}

type Edit struct {
	Title, Body           *string
	AddLabel, RemoveLabel string
	Assign, Unassign      bool
}

func main() {
	gh := &ghCLI{dry: os.Getenv("TRACKER_DRY_RUN") != "", out: os.Stdout}
	colour := term.IsTerminal(int(os.Stdout.Fd())) && os.Getenv("NO_COLOR") == ""
	os.Exit(run(os.Args[1:], gh, os.Stdout, os.Stderr, colour))
}

func run(args []string, gh GitHub, stdout, stderr io.Writer, colour bool) int {
	if len(args) == 0 || args[0] == "-h" || args[0] == "--help" || args[0] == "help" {
		fmt.Fprint(stderr, usage)
		if len(args) == 0 {
			return 2
		}
		return 0
	}
	cmd, ok := commands[args[0]]
	if !ok {
		fmt.Fprintf(stderr, "tracker: no such method %q\n\n%s", args[0], usage)
		return 2
	}
	c := &call{name: args[0], out: stdout, colour: colour}
	if err := c.parse(cmd, args[1:]); err != nil {
		fmt.Fprintf(stderr, "tracker: %s: %v\n", args[0], err)
		return 2
	}
	model, err := gh.Load()
	if err == nil {
		err = cmd.run(c, model, gh)
	}
	var r refusal
	switch {
	case err == nil:
		return 0
	case errors.As(err, &r):
		fmt.Fprintf(stderr, "tracker: %s\n", r.msg)
	default:
		fmt.Fprintf(stderr, "tracker: %v\n", err)
	}
	return 1
}

const usage = `tracker <method> [args]

  board [--done] [--no-colour]            what can start, what is in progress, what is blocked
  validate                                check the model; exits non-zero on any problem
  issue.get <N>                           an issue, its dependencies and its tasks
  ready <TASK>                            what stands between a task and ready

  slice.create --title T --goal G --demo D [--implements X]... [--out-of-scope X]... [--open X]...
                                          prints the new slice's number, nothing else
  slice.sync <SLICE>                      rewrite its Tasks section: tasks, startable now, graph
  task.create --title T --what W --criterion C... [--slice N] [--depends-on N]... [--note X]
                                          prints the new task's number; without --slice it is unplanned
  issue.update <N> [--title T] [--body B | --body-file F] [--status S] [--note X]
                                          task: todo|ready|doing|done|dropped; slice: done|dropped
  issue.link.add|remove <N> <RELATION> <OTHER>
                                          depends-on | relates-to | discovered-during
  issue.delete <N> --yes                  prefer --status dropped, which keeps the record
`

// ---- arguments -------------------------------------------------------------

type list []string

func (l *list) String() string     { return strings.Join(*l, ",") }
func (l *list) Set(v string) error { *l = append(*l, v); return nil }

type command struct {
	positional int // leading positional arguments, before the flags
	flags      func(*flag.FlagSet, *call)
	run        func(*call, Model, GitHub) error
}

type call struct {
	name   string
	out    io.Writer
	colour bool
	pos    []string

	title, goal, demo, what, note, body, bodyFile, status string
	implements, outOfScope, open, criteria, dependsOn     list
	slice                                                 int
	done, noColour, yes                                   bool
	set                                                   map[string]bool
}

func (c *call) parse(cmd command, args []string) error {
	if len(args) < cmd.positional {
		return fmt.Errorf("expected %d argument(s) before the flags", cmd.positional)
	}
	c.pos = args[:cmd.positional]
	fs := flag.NewFlagSet(c.name, flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	if cmd.flags != nil {
		cmd.flags(fs, c)
	}
	if err := fs.Parse(args[cmd.positional:]); err != nil {
		return err
	}
	if fs.NArg() > 0 {
		return fmt.Errorf("unexpected argument %q", fs.Arg(0))
	}
	c.set = map[string]bool{}
	fs.Visit(func(f *flag.Flag) { c.set[f.Name] = true })
	return nil
}

func (c *call) num(at int) (int, error) { return number(c.pos[at]) }

func number(s string) (int, error) {
	n, err := strconv.Atoi(strings.TrimPrefix(s, "#"))
	if err != nil || n <= 0 {
		return 0, refused("not an issue number: %q", s)
	}
	return n, nil
}

func (c *call) print(f string, a ...any) { fmt.Fprintf(c.out, f+"\n", a...) }

func issueIn(m Model, n int) (*Issue, error) {
	i, ok := m[n]
	if !ok {
		return nil, refused("#%d is not an issue in this repo (issues and pull requests share numbers)", n)
	}
	return i, nil
}

func taskIn(m Model, n int) (*Issue, error) {
	i, err := issueIn(m, n)
	if err == nil && i.IsSlice() {
		return nil, refused("#%d is a slice, not a task", n)
	}
	return i, err
}

var commands = map[string]command{
	"board": {
		flags: func(fs *flag.FlagSet, c *call) {
			fs.BoolVar(&c.done, "done", false, "")
			fs.BoolVar(&c.noColour, "no-colour", false, "")
		},
		run: func(c *call, m Model, _ GitHub) error {
			c.print("%s", m.board(c.done, c.colour && !c.noColour))
			return nil
		},
	},
	"validate": {run: func(c *call, m Model, _ GitHub) error {
		errs := m.validate()
		for _, e := range errs {
			c.print("%s", e)
		}
		if len(errs) > 0 {
			return refused("%d problem(s)", len(errs))
		}
		c.print("ok")
		return nil
	}},
	"issue.get":    {positional: 1, run: cmdGet},
	"ready":        {positional: 1, run: cmdReady},
	"slice.create": {flags: sliceFlags, run: cmdSliceCreate},
	"slice.sync":   {positional: 1, run: cmdSliceSync},
	"task.create":  {flags: taskFlags, run: cmdTaskCreate},
	"issue.update": {positional: 1, flags: updateFlags, run: cmdUpdate},
	"issue.link.add": {positional: 3, run: func(c *call, m Model, gh GitHub) error {
		return cmdLink(c, m, gh, true)
	}},
	"issue.link.remove": {positional: 3, run: func(c *call, m Model, gh GitHub) error {
		return cmdLink(c, m, gh, false)
	}},
	"issue.delete": {positional: 1, flags: func(fs *flag.FlagSet, c *call) { fs.BoolVar(&c.yes, "yes", false, "") },
		run: cmdDelete},
}

func sliceFlags(fs *flag.FlagSet, c *call) {
	fs.StringVar(&c.title, "title", "", "")
	fs.StringVar(&c.goal, "goal", "", "")
	fs.StringVar(&c.demo, "demo", "", "")
	fs.Var(&c.implements, "implements", "")
	fs.Var(&c.outOfScope, "out-of-scope", "")
	fs.Var(&c.open, "open", "")
}

func taskFlags(fs *flag.FlagSet, c *call) {
	fs.StringVar(&c.title, "title", "", "")
	fs.StringVar(&c.what, "what", "", "")
	fs.Var(&c.criteria, "criterion", "")
	fs.IntVar(&c.slice, "slice", 0, "")
	fs.Var(&c.dependsOn, "depends-on", "")
	fs.StringVar(&c.note, "note", "", "")
}

func updateFlags(fs *flag.FlagSet, c *call) {
	fs.StringVar(&c.title, "title", "", "")
	fs.StringVar(&c.body, "body", "", "")
	fs.StringVar(&c.bodyFile, "body-file", "", "")
	fs.StringVar(&c.status, "status", "", "")
	fs.StringVar(&c.note, "note", "", "")
}

// ---- reads -----------------------------------------------------------------

func cmdGet(c *call, m Model, _ GitHub) error {
	n, err := c.num(0)
	if err != nil {
		return err
	}
	i, err := issueIn(m, n)
	if err != nil {
		return err
	}
	kind := "unplanned task"
	switch {
	case i.IsSlice():
		kind = "slice"
	case i.Parent != 0:
		kind = fmt.Sprintf("task of #%d", i.Parent)
	}
	c.print("#%d %s\n%s, %s", i.Number, i.Title, kind, i.Status())
	for _, d := range i.Deps {
		c.print("depends on %s", m.describe(d))
	}
	for _, o := range m.Sorted() {
		if o.DependsOn(n) {
			c.print("needed by #%d (%s) %s", o.Number, o.Status(), o.Title)
		}
	}
	if i.IsSlice() {
		for _, t := range m.tasksOf(n) {
			c.print("  #%d  %-8s%s", t.Number, t.Status(), t.Title)
		}
	}
	c.print("\n%s\n\n(comments: gh issue view %d --comments)", strings.TrimSpace(i.Body), n)
	return nil
}

// describe names a dependency with its state, and says so when it is not an
// issue in this repo.
func (m Model) describe(d Dep) string {
	if !d.Local() {
		state := "open"
		switch {
		case d.Closed && d.NotPlanned:
			state = "dropped"
		case d.Closed:
			state = "done"
		}
		return fmt.Sprintf("%s (%s, another repository)", d, state)
	}
	if i, ok := m[d.Number]; ok {
		return fmt.Sprintf("%s (%s) %s", d, i.Status(), i.Title)
	}
	return fmt.Sprintf("%s (MISSING: not an issue in this repository)", d)
}

func cmdReady(c *call, m Model, _ GitHub) error {
	n, err := c.num(0)
	if err != nil {
		return err
	}
	t, err := taskIn(m, n)
	if err != nil {
		return err
	}
	c.print("task     #%d %s  status=%s", n, t.Title, t.Status())
	switch s, ok := m[t.Parent]; {
	case t.Parent == 0 && t.Has(labelUnplanned):
		c.print("slice    none  (labelled unplanned, so only the task's own criteria apply)")
	case t.Parent == 0:
		c.print("slice    none  PROBLEM: no slice and not labelled unplanned")
	case !ok || demoOf(s.Body) == "":
		c.print("slice    #%d  BLOCKED: no demo", t.Parent)
	default:
		demo := demoOf(s.Body)
		verdict := "looks concrete"
		if len(strings.Fields(demo)) < 8 || !strings.Contains(demo, "`") { // a demo names a command
			verdict = "THIN — read it before refining"
		}
		c.print("slice    #%d  demo=%s", t.Parent, verdict)
		shown := demo
		if r := []rune(demo); len(r) > 110 {
			shown = string(r[:110]) + "..."
		}
		c.print("         \"%s\"", shown)
	}
	if len(t.Deps) == 0 {
		c.print("deps     none declared — check that is true, not just unfilled")
	} else {
		line := fmt.Sprintf("deps     %d declared", len(t.Deps))
		var missing, left []string
		for _, d := range t.Deps {
			if _, ok := m[d.Number]; d.Local() && !ok {
				missing = append(missing, d.String())
			}
		}
		for _, d := range m.unmet(t) {
			left = append(left, m.describe(d))
		}
		if len(missing) > 0 {
			line += "  MISSING: " + strings.Join(missing, ", ")
		}
		if len(left) > 0 {
			line += "  unmet: " + strings.Join(left, "; ")
		} else if len(missing) == 0 {
			line += "  all done"
		}
		c.print("%s", line)
	}
	crit := criteriaOf(t.Body)
	c.print("criteria %d", len(crit))
	if len(crit) == 0 {
		c.print("         PROBLEM: none (no '**Done when.**' checklist)")
	}
	for _, cr := range crit {
		verdict := "ok"
		if f := flags(cr.Text); len(f) > 0 {
			verdict = strings.Join(f, "; ")
		}
		text := cr.Text
		if r := []rune(text); len(r) > 90 {
			text = string(r[:90])
		}
		c.print("         [%s] %s", verdict, text)
	}
	c.print("\nready when: the slice demo is concrete and every criterion is checkable by someone else; "+
		"then run: ./tracker issue.update %d --status ready. Blocked-ness is reported separately.", n)
	return nil
}

func paint(on bool) func(style, text string) string {
	codes := map[string]string{"head": "1", "green": "32", "yellow": "33", "dim": "2", "cyan": "36"}
	return func(style, text string) string {
		if !on {
			return text
		}
		return "\033[" + codes[style] + "m" + text + "\033[0m"
	}
}

func (m Model) board(showDone, colour bool) string {
	c := paint(colour)
	type group struct {
		slice int
		title string
	}
	var groups []group
	for _, s := range m.Sorted() {
		if s.IsSlice() && !s.Closed {
			groups = append(groups, group{s.Number, fmt.Sprintf("#%d %s", s.Number, s.Title)})
		}
	}
	if len(m.tasksOf(0)) > 0 {
		groups = append(groups, group{0, "Unplanned"})
	}
	var out []string
	for _, g := range groups {
		var done, doing, next, blocked []*Issue
		tasks := m.tasksOf(g.slice)
		for _, t := range tasks {
			switch s := t.Status(); {
			case t.Closed:
				done = append(done, t)
			case s == "doing":
				doing = append(doing, t)
			case len(m.unmet(t)) > 0:
				blocked = append(blocked, t)
			default:
				next = append(next, t)
			}
		}
		out = append(out, c("head", g.title)+"  "+c("dim", fmt.Sprintf("%d/%d closed", len(done), len(tasks))))
		row := func(t *Issue, style, extra string) {
			tag := "        "
			if t.Status() == "ready" {
				tag = c("cyan", "[ready] ")
			}
			out = append(out, fmt.Sprintf("    %s%s%s%s", c(style, fmt.Sprintf("%-6s", fmt.Sprintf("#%d", t.Number))), tag, t.Title, extra))
		}
		for _, sec := range []struct {
			label, style string
			members      []*Issue
		}{{"NEXT", "green", next}, {"IN PROGRESS", "yellow", doing}} {
			if len(sec.members) > 0 {
				out = append(out, "  "+c(sec.style, sec.label))
				for _, t := range sec.members {
					row(t, sec.style, "")
				}
			}
		}
		if len(blocked) > 0 {
			out = append(out, "  "+c("dim", "BLOCKED"))
			for _, t := range blocked {
				row(t, "dim", c("dim", "  waits on "+waitsOn(m.unmet(t))))
			}
		}
		switch {
		case len(done) > 0 && showDone:
			out = append(out, "  "+c("dim", "CLOSED"))
			for _, t := range done {
				row(t, "dim", c("dim", fmt.Sprintf("  (%s)", t.Status())))
			}
		case len(done) > 0:
			out = append(out, "  "+c("dim", fmt.Sprintf("CLOSED  %d  (--done to list)", len(done))))
		}
		out = append(out, "")
	}
	if b := strings.TrimRight(strings.Join(out, "\n"), "\n "); b != "" {
		return b
	}
	return "No open slices and no unplanned tasks."
}

// ---- writes ----------------------------------------------------------------

func cmdSliceCreate(c *call, _ Model, gh GitHub) error {
	if strings.TrimSpace(c.title) == "" || strings.TrimSpace(c.goal) == "" {
		return refused("slice.create: --title and --goal are required")
	}
	if strings.TrimSpace(c.demo) == "" {
		return refused("slice.create: --demo is required: what can be run at the end that could not be run before")
	}
	n, err := gh.Create(c.title, sliceBody(c.goal, c.demo, c.implements, c.outOfScope, c.open), []string{labelSlice})
	if err == nil {
		c.print("%d", n)
	}
	return err
}

func cmdSliceSync(c *call, m Model, gh GitHub) error {
	n, err := c.num(0)
	if err != nil {
		return err
	}
	s, err := issueIn(m, n)
	if err != nil {
		return err
	}
	if !s.IsSlice() {
		return refused("#%d is not a slice", n)
	}
	body, err := syncedBody(s.Body, m.tasksSection(n))
	if err != nil {
		return err
	}
	free := hashes(m.frontier(n))
	if free == "" {
		free = "none"
	}
	if body == s.Body {
		c.print("#%d already in sync; startable now: %s", n, free)
		return nil
	}
	if err := gh.Edit(n, Edit{Body: &body}); err != nil {
		return err
	}
	c.print("synced #%d: %d tasks; startable now: %s", n, len(m.tasksOf(n)), free)
	return nil
}

func cmdTaskCreate(c *call, m Model, gh GitHub) error {
	if strings.TrimSpace(c.title) == "" || strings.TrimSpace(c.what) == "" {
		return refused("task.create: --title and --what are required")
	}
	if len(c.criteria) == 0 {
		return refused("task.create: name at least one --criterion someone else can check")
	}
	if c.slice != 0 {
		s, err := issueIn(m, c.slice)
		if err != nil {
			return err
		}
		if !s.IsSlice() || s.Closed {
			return refused("#%d is not an open slice", c.slice)
		}
	}
	var deps []int
	for _, raw := range c.dependsOn {
		d, err := number(raw)
		if err != nil {
			return err
		}
		t, err := taskIn(m, d)
		if err != nil {
			return err
		}
		if t.Status() == "dropped" {
			return refused("#%d was dropped; nothing can depend on it", d)
		}
		deps = append(deps, d)
	}
	var labels []string
	if c.slice == 0 {
		labels = []string{labelUnplanned}
	}
	n, err := gh.Create(c.title, taskBody(c.what, c.criteria, c.note), labels)
	if err != nil {
		return err
	}
	if c.slice != 0 {
		if err := gh.Attach(c.slice, n); err != nil {
			return err
		}
	}
	for _, d := range deps {
		if err := gh.AddDep(n, d); err != nil {
			return err
		}
	}
	c.print("%d", n)
	return nil
}

func cmdUpdate(c *call, m Model, gh GitHub) error {
	n, err := c.num(0)
	if err != nil {
		return err
	}
	i, err := issueIn(m, n)
	if err != nil {
		return err
	}
	if c.set["body"] && c.set["body-file"] {
		return refused("issue.update: name --body or --body-file, not both")
	}
	var body *string
	switch {
	case c.set["body"]:
		body = &c.body
	case c.set["body-file"]:
		b, err := os.ReadFile(c.bodyFile)
		if err != nil {
			return refused("issue.update: %v", err)
		}
		s := string(b)
		body = &s
	}
	var title *string
	if c.set["title"] {
		title = &c.title
	}
	if title == nil && body == nil && !c.set["status"] && !c.set["note"] {
		return refused("issue.update: nothing to change; name --title, --body, --status or --note")
	}
	if c.set["status"] {
		allowed, hint := taskStatuses, ""
		if i.IsSlice() {
			allowed, hint = sliceStatuses, "; a slice's other states come from its tasks"
		}
		if !slices.Contains(allowed, c.status) {
			return refused("#%d: status '%s' is outside the vocabulary: %s%s", n, c.status, strings.Join(allowed, " | "), hint)
		}
		if err := checkStatus(m, i, c.status, c.note); err != nil {
			return err
		}
	}
	if title != nil || body != nil {
		if err := gh.Edit(n, Edit{Title: title, Body: body}); err != nil {
			return err
		}
	}
	switch c.status {
	case "":
	case "done":
		return gh.Close(n, "completed", c.note)
	case "dropped":
		return gh.Close(n, "not planned", c.note)
	default:
		if i.Closed {
			if err := gh.Reopen(n); err != nil {
				return err
			}
		}
		var e Edit
		switch {
		case c.status == "todo":
			e = Edit{Unassign: len(i.Assignees) > 0}
			if i.Has(labelReady) {
				e.RemoveLabel = labelReady
			}
		case c.status == "ready" && !i.Has(labelReady):
			e.AddLabel = labelReady
		case c.status == "doing":
			e.Assign = true
		}
		if e != (Edit{}) {
			if err := gh.Edit(n, e); err != nil {
				return err
			}
		}
	}
	if c.note != "" {
		return gh.Comment(n, c.note)
	}
	return nil
}

func checkStatus(m Model, i *Issue, status, note string) error {
	n := i.Number
	if status == "dropped" && note == "" {
		return refused("#%d: dropping needs --note saying why", n)
	}
	if i.IsSlice() {
		var left []int
		for _, t := range m.tasksOf(n) {
			if !t.Closed {
				left = append(left, t.Number)
			}
		}
		if len(left) > 0 {
			return refused("#%d: the slice still has open tasks: %s", n, hashes(left))
		}
		return nil
	}
	if status == "doing" {
		if !i.Has(labelReady) {
			return refused("#%d is not ready: refine it first (refine-task), then --status ready", n)
		}
		if left := m.unmet(i); len(left) > 0 {
			return refused("#%d is blocked: waits on %s", n, waitsOn(left))
		}
	}
	if status == "done" {
		var open []string
		for _, cr := range criteriaOf(i.Body) {
			if !cr.Checked {
				open = append(open, cr.Text)
			}
		}
		if len(open) > 0 {
			return refused("#%d has unchecked criteria; tick them in the body or change them: %s", n, strings.Join(open, "; "))
		}
	}
	return nil
}

func cmdLink(c *call, m Model, gh GitHub, adding bool) error {
	n, err := c.num(0)
	if err != nil {
		return err
	}
	relation := c.pos[1]
	if !slices.Contains(relations, relation) {
		return refused("relation '%s' is not in use: %s", relation, strings.Join(relations, " | "))
	}
	other, err := number(c.pos[2])
	if err != nil {
		return err
	}
	if n == other {
		return refused("#%d cannot link to itself", n)
	}
	i, err := issueIn(m, n)
	if err != nil {
		return err
	}
	o, err := issueIn(m, other)
	if err != nil {
		return err
	}
	if relation == "depends-on" {
		if i.IsSlice() || o.IsSlice() {
			return refused("dependencies are between tasks, not slices")
		}
		if !adding {
			if !i.DependsOn(other) {
				return refused("#%d does not depend on #%d", n, other)
			}
			return gh.RemoveDep(n, other)
		}
		if i.DependsOn(other) {
			return refused("#%d already depends on #%d", n, other)
		}
		if m.reaches(other, n) {
			return refused("#%d already depends on #%d, directly or through others: that would be a cycle", other, n)
		}
		return gh.AddDep(n, other)
	}
	line := linkLine(relation, other)
	lines := strings.Split(strings.TrimRight(i.Body, "\n"), "\n")
	has := slices.Contains(lines, line)
	var body string
	switch {
	case adding && has:
		return refused("#%d already says '%s'", n, line)
	case adding:
		body = strings.TrimRight(i.Body, "\n") + "\n\n" + line + "\n"
	case !has:
		return refused("#%d does not say '%s'", n, line)
	default:
		kept := slices.DeleteFunc(lines, func(l string) bool { return l == line })
		body = strings.TrimRight(blankRuns.ReplaceAllString(strings.Join(kept, "\n"), "\n\n"), "\n") + "\n"
	}
	return gh.Edit(n, Edit{Body: &body})
}

func cmdDelete(c *call, m Model, gh GitHub) error {
	n, err := c.num(0)
	if err != nil {
		return err
	}
	i, err := issueIn(m, n)
	if err != nil {
		return err
	}
	if !c.yes {
		return refused("issue.delete: pass --yes. Deleting cannot be undone; --status dropped keeps the record")
	}
	if i.IsSlice() && len(m.tasksOf(n)) > 0 {
		return refused("#%d has tasks; drop or delete them first", n)
	}
	var needed []Dep
	for _, o := range m.Sorted() {
		if o.DependsOn(n) && !o.Closed {
			needed = append(needed, Dep{Number: o.Number})
		}
	}
	if len(needed) > 0 {
		return refused("#%d is a dependency of %s", n, waitsOn(needed))
	}
	return gh.Delete(n)
}

// ---- gh --------------------------------------------------------------------

// ghCLI runs gh, which uses your own gh auth login and finds the repo from the
// working directory.
type ghCLI struct {
	dry  bool
	out  io.Writer
	ids  map[int]int64
	fake int
}

func (g *ghCLI) Load() (Model, error) {
	out, err := g.gh(nil, "api", "graphql", "--paginate", "--slurp", "-f", "query="+query,
		"-F", "owner={owner}", "-F", "repo={repo}")
	if err != nil {
		return nil, err
	}
	m, err := parseIssues(out)
	if err != nil {
		return nil, err
	}
	g.ids = map[int]int64{}
	for n, i := range m {
		g.ids[n] = i.ID
	}
	return m, nil
}

func (g *ghCLI) gh(stdin []byte, args ...string) ([]byte, error) {
	cmd := exec.Command("gh", args...)
	if stdin != nil {
		cmd.Stdin = bytes.NewReader(stdin)
	}
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	out, err := cmd.Output()
	if err != nil {
		msg := strings.TrimSpace(stderr.String())
		if msg == "" {
			msg = err.Error()
		}
		return nil, fmt.Errorf("gh failed: %s", msg)
	}
	return out, nil
}

var plainWord = regexp.MustCompile(`^[\w@%+=:,./{}-]+$`)

func quote(a string) string {
	if plainWord.MatchString(a) {
		return a
	}
	return "'" + strings.ReplaceAll(a, "'", `'\''`) + "'"
}

func (g *ghCLI) write(stdin *string, args ...string) (string, error) {
	if g.dry {
		quoted := make([]string, len(args))
		for n, a := range args {
			quoted[n] = quote(a)
		}
		line := "gh " + strings.Join(quoted, " ")
		if stdin != nil && *stdin != "" {
			line += "\n  stdin: " + *stdin
		}
		fmt.Fprintln(g.out, line)
		return "", nil
	}
	var in []byte
	if stdin != nil {
		in = []byte(*stdin)
	}
	out, err := g.gh(in, args...)
	return string(out), err
}

func (g *ghCLI) Create(title, body string, labels []string) (int, error) {
	args := []string{"issue", "create", "--title", title, "--body-file", "-"}
	for _, l := range labels {
		args = append(args, "--label", l)
	}
	out, err := g.write(&body, args...)
	if err != nil {
		return 0, err
	}
	if g.dry {
		g.fake++
		return 900 + g.fake, nil
	}
	url := strings.TrimSpace(out)
	return strconv.Atoi(url[strings.LastIndex(url, "/")+1:])
}

func (g *ghCLI) idOf(n int) (int64, error) {
	if id, ok := g.ids[n]; ok {
		return id, nil
	}
	if g.dry {
		return int64(n) * 1000, nil
	}
	out, err := g.gh(nil, "api", fmt.Sprintf("repos/{owner}/{repo}/issues/%d", n), "--jq", ".id")
	if err != nil {
		return 0, err
	}
	id, err := strconv.ParseInt(strings.TrimSpace(string(out)), 10, 64)
	if err == nil {
		g.ids[n] = id
	}
	return id, err
}

func (g *ghCLI) Attach(parent, child int) error {
	id, err := g.idOf(child)
	if err != nil {
		return err
	}
	_, err = g.write(nil, "api", "--method", "POST", fmt.Sprintf("repos/{owner}/{repo}/issues/%d/sub_issues", parent),
		"-F", fmt.Sprintf("sub_issue_id=%d", id), "--silent")
	return err
}

func (g *ghCLI) AddDep(n, dep int) error {
	id, err := g.idOf(dep)
	if err != nil {
		return err
	}
	_, err = g.write(nil, "api", "--method", "POST", fmt.Sprintf("repos/{owner}/{repo}/issues/%d/dependencies/blocked_by", n),
		"-F", fmt.Sprintf("issue_id=%d", id), "--silent")
	return err
}

func (g *ghCLI) RemoveDep(n, dep int) error {
	id, err := g.idOf(dep)
	if err != nil {
		return err
	}
	_, err = g.write(nil, "api", "--method", "DELETE",
		fmt.Sprintf("repos/{owner}/{repo}/issues/%d/dependencies/blocked_by/%d", n, id), "--silent")
	return err
}

func (g *ghCLI) Edit(n int, e Edit) error {
	args := []string{"issue", "edit", strconv.Itoa(n)}
	if e.Title != nil {
		args = append(args, "--title", *e.Title)
	}
	if e.Body != nil {
		args = append(args, "--body-file", "-")
	}
	if e.AddLabel != "" {
		args = append(args, "--add-label", e.AddLabel)
	}
	if e.RemoveLabel != "" {
		args = append(args, "--remove-label", e.RemoveLabel)
	}
	if e.Assign {
		args = append(args, "--add-assignee", "@me")
	}
	if e.Unassign {
		args = append(args, "--remove-assignee", "@me")
	}
	_, err := g.write(e.Body, args...)
	return err
}

func (g *ghCLI) Close(n int, reason, comment string) error {
	args := []string{"issue", "close", strconv.Itoa(n), "--reason", reason}
	if comment != "" {
		args = append(args, "--comment", comment)
	}
	_, err := g.write(nil, args...)
	return err
}

func (g *ghCLI) Reopen(n int) error {
	_, err := g.write(nil, "issue", "reopen", strconv.Itoa(n))
	return err
}

func (g *ghCLI) Comment(n int, body string) error {
	_, err := g.write(&body, "issue", "comment", strconv.Itoa(n), "--body-file", "-")
	return err
}

func (g *ghCLI) Delete(n int) error {
	_, err := g.write(nil, "issue", "delete", strconv.Itoa(n), "--yes")
	return err
}
