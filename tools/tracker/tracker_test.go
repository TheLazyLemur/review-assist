package main

import (
	"bytes"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

const demo = "`review-assist https://bitbucket.org/o/r/pull-requests/3` opens the pull request and shows its diff."

func slice(n int) *Issue {
	return &Issue{Number: n, Title: "Bitbucket pull requests cannot be reviewed",
		Body: sliceBody("Goal.", demo, nil, nil, nil), Labels: []string{labelSlice}}
}

type opt func(*Issue)

func under(slice int) opt      { return func(i *Issue) { i.Parent = slice } }
func deps(ns ...int) opt       { return func(i *Issue) { i.Deps = local(ns...) } }
func labelled(l ...string) opt { return func(i *Issue) { i.Labels = append(i.Labels, l...) } }
func assigned() opt            { return func(i *Issue) { i.Assignees = []string{"me"} } }
func closed() opt              { return func(i *Issue) { i.Closed = true } }
func dropped() opt             { return func(i *Issue) { i.Closed, i.NotPlanned = true, true } }
func checked() opt             { return func(i *Issue) { i.Body = strings.ReplaceAll(i.Body, "- [ ]", "- [x]") } }
func criteria(c ...string) opt { return func(i *Issue) { i.Body = taskBody("What.", c, "") } }
func blockedBy(d ...Dep) opt   { return func(i *Issue) { i.Deps = d } }
func withBody(b string) opt    { return func(i *Issue) { i.Body = b } }

func local(ns ...int) []Dep {
	var out []Dep
	for _, n := range ns {
		out = append(out, Dep{Number: n})
	}
	return out
}

// task is unplanned unless placed under a slice.
func task(n int, title string, opts ...opt) *Issue {
	i := &Issue{Number: n, Title: title, Body: taskBody("What.", []string{"`go test ./...` passes"}, "")}
	for _, o := range opts {
		o(i)
	}
	if i.Parent == 0 && !i.IsSlice() && !i.Has(labelUnplanned) {
		i.Labels = append(i.Labels, labelUnplanned)
	}
	return i
}

type fakeGitHub struct {
	model Model
	calls []string
	next  int
}

func (f *fakeGitHub) Load() (Model, error) { return f.model, nil }
func (f *fakeGitHub) log(format string, a ...any) error {
	f.calls = append(f.calls, fmt.Sprintf(format, a...))
	return nil
}
func (f *fakeGitHub) Create(title, body string, labels []string) (int, error) {
	f.next++
	n := 100 + f.next
	return n, f.log("create %d %q %v\n%s", n, title, labels, body)
}
func (f *fakeGitHub) Attach(parent, child int) error { return f.log("attach %d %d", parent, child) }
func (f *fakeGitHub) AddDep(n, dep int) error        { return f.log("add_dep %d %d", n, dep) }
func (f *fakeGitHub) RemoveDep(n, dep int) error     { return f.log("remove_dep %d %d", n, dep) }
func (f *fakeGitHub) Edit(n int, e Edit) error {
	s := fmt.Sprintf("edit %d", n)
	if e.Title != nil {
		s += fmt.Sprintf(" title=%q", *e.Title)
	}
	if e.AddLabel != "" {
		s += " add_label=" + e.AddLabel
	}
	if e.RemoveLabel != "" {
		s += " remove_label=" + e.RemoveLabel
	}
	if e.Assign {
		s += " assign"
	}
	if e.Unassign {
		s += " unassign"
	}
	if e.Body != nil {
		s += "\n" + *e.Body
	}
	return f.log("%s", s)
}
func (f *fakeGitHub) Close(n int, reason, comment string) error {
	return f.log("close %d %q %q", n, reason, comment)
}
func (f *fakeGitHub) Reopen(n int) error               { return f.log("reopen %d", n) }
func (f *fakeGitHub) Comment(n int, body string) error { return f.log("comment %d %q", n, body) }
func (f *fakeGitHub) Delete(n int) error               { return f.log("delete %d", n) }

type result struct {
	code     int
	out, err string
	calls    []string
}

func runOn(issues []*Issue, args ...string) result {
	m := Model{}
	for _, i := range issues {
		m[i.Number] = i
	}
	gh := &fakeGitHub{model: m}
	var out, errb bytes.Buffer
	code := run(args, gh, &out, &errb, false)
	return result{code, out.String(), errb.String(), gh.calls}
}

func mustContain(t *testing.T, got string, wants ...string) {
	t.Helper()
	for _, w := range wants {
		if !strings.Contains(got, w) {
			t.Errorf("missing %q in:\n%s", w, got)
		}
	}
}

func mustSucceed(t *testing.T, r result) {
	t.Helper()
	if r.code != 0 {
		t.Fatalf("exit %d: %s", r.code, r.err)
	}
}

// ---- task.create -----------------------------------------------------------

func TestATaskInASliceIsASubIssueWithItsDependencies(t *testing.T) {
	// given
	// ... a slice with one task already in it
	model := []*Issue{slice(20), task(21, "Bitbucket CodeHost reads pull requests", under(20))}

	// when
	// ... a second task is created in the slice, depending on the first
	r := runOn(model, "task.create", "--title", "Bitbucket CodeHost posts comments", "--what", "Post comments and verdicts.",
		"--criterion", "`go test ./...` passes", "--criterion", "A comment posts to a real pull request",
		"--slice", "20", "--depends-on", "21")

	// then
	// ... it is created with What and Done when and no label, attached to the slice, depending on #21, and its number printed
	mustSucceed(t, r)
	if r.out != "101\n" {
		t.Errorf("stdout = %q, want only the number", r.out)
	}
	mustContain(t, r.calls[0], `create 101 "Bitbucket CodeHost posts comments" []`, "**What.** Post comments and verdicts.",
		"**Done when.**\n\n- [ ] `go test ./...` passes\n- [ ] A comment posts to a real pull request")
	if want := []string{"attach 20 101", "add_dep 101 21"}; !reflect.DeepEqual(r.calls[1:], want) {
		t.Errorf("calls = %q, want %q", r.calls[1:], want)
	}
}

func TestATaskWithoutASliceIsLabelledUnplanned(t *testing.T) {
	// given
	// ... an empty repo
	var model []*Issue

	// when
	// ... a task is created without --slice
	r := runOn(model, "task.create", "--title", "Config files can leak a token", "--what", "W.",
		"--criterion", "`review-assist` refuses a 0644 config")

	// then
	// ... it is labelled unplanned and attached to nothing
	mustSucceed(t, r)
	mustContain(t, r.calls[0], "[unplanned]")
	if len(r.calls) != 1 {
		t.Errorf("calls = %q", r.calls)
	}
}

// ---- slice.create ----------------------------------------------------------

func TestASliceHasEverySectionAndTheSliceLabel(t *testing.T) {
	// given
	// ... an empty repo
	var model []*Issue

	// when
	// ... a slice is created with a goal, a demo and one implemented ADR
	r := runOn(model, "slice.create", "--title", "Bitbucket pull requests cannot be reviewed",
		"--goal", "Review Bitbucket pull requests.", "--demo", demo, "--implements", "ADR 0001")

	// then
	// ... the body carries every section, empty ones say None, and it is labelled slice
	mustSucceed(t, r)
	mustContain(t, r.calls[0], "[slice]", "**Demo.** "+demo, "## Implements\n\n- ADR 0001",
		"## Out of scope\n\nNone.", "## Tasks\n\n"+noTasksYet)
}

// ---- issue.update ----------------------------------------------------------

func TestStatusChangesSendTheMatchingWrite(t *testing.T) {
	cases := []struct {
		name  string
		issue *Issue
		args  []string
		want  []string
	}{
		{"ready adds the label", task(5, "T"), []string{"--status", "ready"}, []string{"edit 5 add_label=ready"}},
		{"doing assigns you", task(5, "T", labelled(labelReady)), []string{"--status", "doing"}, []string{"edit 5 assign"}},
		{"todo on a task at todo writes nothing", task(5, "T"), []string{"--status", "todo"}, nil},
		{"todo takes back ready and the assignee", task(5, "T", labelled(labelReady), assigned()),
			[]string{"--status", "todo"}, []string{"edit 5 remove_label=ready unassign"}},
		{"todo reopens a closed task", task(5, "T", checked(), closed()), []string{"--status", "todo"}, []string{"reopen 5"}},
		{"done closes as completed with the note", task(5, "T", checked()), []string{"--status", "done", "--note", "Landed in #12."},
			[]string{`close 5 "completed" "Landed in #12."`}},
		{"dropped closes as not planned", task(5, "T"), []string{"--status", "dropped", "--note", "Not needed."},
			[]string{`close 5 "not planned" "Not needed."`}},
		{"a note alone is a comment", task(5, "T"), []string{"--note", "Reads work."}, []string{`comment 5 "Reads work."`}},
		{"a title and a body are one edit", task(5, "T"), []string{"--title", "New", "--body", "B."},
			[]string{"edit 5 title=\"New\"\nB."}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			// given
			// ... the issue in the case

			// when
			// ... its status or text is updated
			r := runOn([]*Issue{tc.issue}, append([]string{"issue.update", fmt.Sprint(tc.issue.Number)}, tc.args...)...)

			// then
			// ... exactly the expected writes go to GitHub
			mustSucceed(t, r)
			if !reflect.DeepEqual(r.calls, tc.want) {
				t.Errorf("calls = %q, want %q", r.calls, tc.want)
			}
		})
	}
}

func TestASliceClosesOnceItsTasksAreClosed(t *testing.T) {
	// given
	// ... a slice whose one task is done
	model := []*Issue{slice(20), task(21, "Reads", under(20), checked(), closed())}

	// when
	// ... the slice is marked done
	r := runOn(model, "issue.update", "20", "--status", "done")

	// then
	// ... it closes as completed
	mustSucceed(t, r)
	if want := []string{`close 20 "completed" ""`}; !reflect.DeepEqual(r.calls, want) {
		t.Errorf("calls = %q, want %q", r.calls, want)
	}
}

func TestDoingAssignsAReadyTaskWhoseDependencyIsDone(t *testing.T) {
	// given
	// ... a ready task whose dependency is done
	model := []*Issue{slice(20), task(21, "First", under(20), checked(), closed()),
		task(22, "Second", under(20), labelled(labelReady), deps(21))}

	// when
	// ... it is started
	r := runOn(model, "issue.update", "22", "--status", "doing")

	// then
	// ... you are assigned
	mustSucceed(t, r)
	if want := []string{"edit 22 assign"}; !reflect.DeepEqual(r.calls, want) {
		t.Errorf("calls = %q", r.calls)
	}
}

func TestUpdateReadsTheBodyFromAFile(t *testing.T) {
	// given
	// ... a task and a file holding its new body
	path := filepath.Join(t.TempDir(), "body.md")
	if err := os.WriteFile(path, []byte("**What.** New.\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	// when
	// ... the body is updated from the file
	r := runOn([]*Issue{task(5, "T")}, "issue.update", "5", "--body-file", path)

	// then
	// ... the edit carries the file's contents
	mustSucceed(t, r)
	if want := []string{"edit 5\n**What.** New.\n"}; !reflect.DeepEqual(r.calls, want) {
		t.Errorf("calls = %q", r.calls)
	}
}

// ---- refusals --------------------------------------------------------------

func TestEveryRefusalWritesNothing(t *testing.T) {
	loop := []*Issue{slice(20), task(21, "A", under(20)), task(22, "B", under(20), deps(21)), task(23, "C", under(20), deps(22))}
	cases := []struct {
		name   string
		issues []*Issue
		args   []string
		want   string
	}{
		{"a pull request number", nil, []string{"issue.get", "9"}, "#9 is not an issue in this repo"},
		{"a word for a number", nil, []string{"issue.get", "CC-4"}, `not an issue number: "CC-4"`},
		{"ready on a slice", []*Issue{slice(20)}, []string{"ready", "20"}, "#20 is a slice, not a task"},
		{"a slice with no title", nil, []string{"slice.create", "--goal", "G.", "--demo", "d"}, "--title and --goal are required"},
		{"a slice with no demo", nil, []string{"slice.create", "--title", "T", "--goal", "G.", "--demo", " "}, "--demo is required"},
		{"syncing a task", []*Issue{task(5, "T")}, []string{"slice.sync", "5"}, "#5 is not a slice"},
		{"syncing a slice with no Tasks heading", []*Issue{{Number: 20, Title: "S", Labels: []string{labelSlice}, Body: "**Demo.** " + demo + "\n"}},
			[]string{"slice.sync", "20"}, "no '## Tasks' section"},
		{"a task with no what", nil, []string{"task.create", "--title", "T", "--criterion", "c"}, "--title and --what are required"},
		{"a task with no criterion", []*Issue{slice(20)}, []string{"task.create", "--title", "T", "--what", "W.", "--slice", "20"}, "--criterion"},
		{"a task under a non-slice", []*Issue{task(5, "T")}, []string{"task.create", "--title", "T", "--what", "W.", "--criterion", "c", "--slice", "5"},
			"#5 is not an open slice"},
		{"a task under a closed slice", []*Issue{func() *Issue { s := slice(20); s.Closed = true; return s }()},
			[]string{"task.create", "--title", "T", "--what", "W.", "--criterion", "c", "--slice", "20"}, "#20 is not an open slice"},
		{"a dependency that is not an issue", []*Issue{slice(20)}, []string{"task.create", "--title", "T", "--what", "W.", "--criterion", "c", "--depends-on", "9"},
			"#9 is not an issue"},
		{"a dependency that was dropped", []*Issue{task(5, "T", dropped())}, []string{"task.create", "--title", "T", "--what", "W.", "--criterion", "c", "--depends-on", "5"},
			"#5 was dropped"},
		{"a dependency on a slice", []*Issue{slice(20)}, []string{"task.create", "--title", "T", "--what", "W.", "--criterion", "c", "--depends-on", "20"},
			"#20 is a slice, not a task"},
		{"an update naming nothing", []*Issue{task(5, "T")}, []string{"issue.update", "5"}, "nothing to change"},
		{"a body and a body file", []*Issue{task(5, "T")}, []string{"issue.update", "5", "--body", "b", "--body-file", "f"}, "not both"},
		{"a status outside the vocabulary", []*Issue{task(5, "T")}, []string{"issue.update", "5", "--status", "blocked"},
			"status 'blocked' is outside the vocabulary: todo | ready | doing | done | dropped"},
		{"a slice made ready", []*Issue{slice(20)}, []string{"issue.update", "20", "--status", "ready"}, "come from its tasks"},
		{"dropping without a note", []*Issue{task(5, "T")}, []string{"issue.update", "5", "--status", "dropped"}, "dropping needs --note"},
		{"a slice closed over open tasks", []*Issue{slice(20), task(21, "First", under(20))}, []string{"issue.update", "20", "--status", "done"},
			"the slice still has open tasks: #21"},
		{"doing before ready", []*Issue{task(5, "T")}, []string{"issue.update", "5", "--status", "doing"}, "#5 is not ready: refine it first"},
		{"doing while blocked", []*Issue{slice(20), task(21, "First", under(20)), task(22, "Second", under(20), labelled(labelReady), deps(21))},
			[]string{"issue.update", "22", "--status", "doing"}, "#22 is blocked: waits on #21"},
		{"done with an unchecked criterion", []*Issue{task(5, "T", labelled(labelReady))}, []string{"issue.update", "5", "--status", "done"},
			"unchecked criteria; tick them in the body or change them: `go test ./...` passes"},
		{"a link to itself", []*Issue{task(12, "T")}, []string{"issue.link.add", "12", "depends-on", "12"}, "#12 cannot link to itself"},
		{"a relation outside the vocabulary", []*Issue{task(12, "T"), task(5, "U")}, []string{"issue.link.add", "12", "duplicates", "5"},
			"relation 'duplicates' is not in use"},
		{"a dependency between slices", []*Issue{slice(20), task(5, "T")}, []string{"issue.link.add", "5", "depends-on", "20"},
			"dependencies are between tasks, not slices"},
		{"a dependency added twice", []*Issue{task(5, "T"), task(6, "U", deps(5))}, []string{"issue.link.add", "6", "depends-on", "5"},
			"#6 already depends on #5"},
		{"a dependency cycle", loop, []string{"issue.link.add", "21", "depends-on", "23"}, "that would be a cycle"},
		{"removing a dependency that is not there", []*Issue{task(5, "T"), task(6, "U")}, []string{"issue.link.remove", "6", "depends-on", "5"},
			"#6 does not depend on #5"},
		{"a related line added twice", []*Issue{task(7, "A", withBody("Body.\n\nRelated: #8\n")), task(8, "B")},
			[]string{"issue.link.add", "7", "relates-to", "8"}, "#7 already says 'Related: #8'"},
		{"removing a related line that is not there", []*Issue{task(7, "A"), task(8, "B")}, []string{"issue.link.remove", "7", "relates-to", "8"},
			"#7 does not say 'Related: #8'"},
		{"a delete without --yes", []*Issue{task(5, "T")}, []string{"issue.delete", "5"}, "pass --yes"},
		{"deleting a slice with tasks", []*Issue{slice(20), task(21, "First", under(20))}, []string{"issue.delete", "20", "--yes"},
			"#20 has tasks; drop or delete them first"},
		{"deleting a dependency of an open task", []*Issue{task(5, "T"), task(6, "U", deps(5))}, []string{"issue.delete", "5", "--yes"},
			"#5 is a dependency of #6"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			// given
			// ... the issues in the case

			// when
			// ... the refused write is attempted
			r := runOn(tc.issues, tc.args...)

			// then
			// ... it exits non-zero, names the reason, and nothing reaches GitHub
			if r.code == 0 {
				t.Fatalf("exit 0, want a refusal; stdout: %s", r.out)
			}
			mustContain(t, r.err, tc.want)
			if len(r.calls) > 0 {
				t.Errorf("wrote %q", r.calls)
			}
		})
	}
}

// ---- links -----------------------------------------------------------------

func TestLinksWriteADependencyOrABodyLine(t *testing.T) {
	cases := []struct {
		name   string
		issues []*Issue
		args   []string
		want   string
	}{
		{"depends-on adds a native dependency", []*Issue{task(5, "T"), task(6, "U")}, []string{"issue.link.add", "6", "depends-on", "5"}, "add_dep 6 5"},
		{"depends-on is removed the same way", []*Issue{task(5, "T"), task(6, "U", deps(5))}, []string{"issue.link.remove", "6", "depends-on", "5"}, "remove_dep 6 5"},
		{"relates-to appends a line", []*Issue{task(7, "A", withBody("## Notes\n")), task(8, "B")}, []string{"issue.link.add", "7", "relates-to", "8"},
			"edit 7\n## Notes\n\nRelated: #8\n"},
		{"discovered-during appends its line", []*Issue{task(7, "A", withBody("## Notes\n")), task(8, "B")}, []string{"issue.link.add", "7", "discovered-during", "8"},
			"edit 7\n## Notes\n\nDiscovered during #8\n"},
		{"removing a line leaves no gap", []*Issue{task(7, "A", withBody("## Notes\n\nRelated: #8\n\nDiscovered during #9\n")), task(8, "B")},
			[]string{"issue.link.remove", "7", "relates-to", "8"}, "edit 7\n## Notes\n\nDiscovered during #9\n"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			// given
			// ... the issues in the case

			// when
			// ... the link is changed
			r := runOn(tc.issues, tc.args...)

			// then
			// ... one write carries it, to the issue named first
			mustSucceed(t, r)
			if want := []string{tc.want}; !reflect.DeepEqual(r.calls, want) {
				t.Errorf("calls = %q, want %q", r.calls, want)
			}
		})
	}
}

func TestDeleteRemovesAnIssueNothingNeeds(t *testing.T) {
	// given
	// ... a task whose only dependant is closed
	model := []*Issue{task(5, "T"), task(6, "U", deps(5), checked(), closed())}

	// when
	// ... it is deleted with --yes
	r := runOn(model, "issue.delete", "5", "--yes")

	// then
	// ... it is deleted
	mustSucceed(t, r)
	if want := []string{"delete 5"}; !reflect.DeepEqual(r.calls, want) {
		t.Errorf("calls = %q", r.calls)
	}
}

// ---- slice.sync and validate -----------------------------------------------

func TestSyncWritesTheTaskListFrontierAndGraph(t *testing.T) {
	// given
	// ... a slice with a done task, a task that can start, and one blocked on it
	model := []*Issue{slice(20), task(21, "Reads", under(20), checked(), closed()),
		task(22, `Posts "comments"`, under(20), deps(21)), task(23, "Wires it", under(20), deps(22))}

	// when
	// ... the slice is synced
	r := runOn(model, "slice.sync", "20")

	// then
	// ... the Tasks section lists every task, what can start, and each edge, and the other sections survive
	mustSucceed(t, r)
	mustContain(t, r.calls[0], "- #21\n- #22\n- #23", "Startable now: #22.", `t22["#22 Posts #quot;comments#quot;"]`,
		"t21 --> t22\n  t22 --> t23", "## Notes", "**Demo.** "+demo)
	mustContain(t, r.out, "synced #20: 3 tasks; startable now: #22")
}

func TestSyncWritesNothingWhenInSync(t *testing.T) {
	// given
	// ... a slice whose body is what a first sync wrote
	model := []*Issue{slice(20), task(21, "Reads", under(20))}
	first := runOn(model, "slice.sync", "20")
	mustSucceed(t, first)
	model[0].Body = strings.TrimPrefix(first.calls[0], "edit 20\n")

	// when
	// ... it is synced again
	r := runOn(model, "slice.sync", "20")

	// then
	// ... nothing is written
	mustSucceed(t, r)
	mustContain(t, r.out, "#20 already in sync; startable now: #21")
	if len(r.calls) > 0 {
		t.Errorf("wrote %q", r.calls)
	}
}

func TestSyncFillsATasksHeadingAtTheEndOfTheBody(t *testing.T) {
	// given
	// ... a slice whose body ends on the Tasks heading with no newline after it
	model := []*Issue{{Number: 20, Title: "S", Labels: []string{labelSlice}, Body: "**Demo.** " + demo + "\n\n## Tasks"},
		task(21, "Reads", under(20))}

	// when
	// ... the slice is synced
	r := runOn(model, "slice.sync", "20")

	// then
	// ... the Tasks section is written after the demo
	mustSucceed(t, r)
	mustContain(t, r.calls[0], "edit 20\n**Demo.** "+demo+"\n\n## Tasks\n\n", "- #21\n\nStartable now: #21.")
}

func TestACleanModelValidates(t *testing.T) {
	// given
	// ... a synced slice with a done task and a ready one depending on it, and an unplanned task
	m := Model{20: slice(20), 21: task(21, "Reads", under(20), checked(), closed()),
		22: task(22, "Posts", under(20), labelled(labelReady), deps(21)), 5: task(5, "Unplanned")}
	m[20].Body, _ = syncedBody(m[20].Body, m.tasksSection(20))

	// when
	// ... it is validated
	r := runOn([]*Issue{m[20], m[21], m[22], m[5]}, "validate")

	// then
	// ... there is nothing to report
	mustSucceed(t, r)
	if r.out != "ok\n" {
		t.Errorf("stdout = %q", r.out)
	}
}

func TestClosingATaskDoesNotMakeTheTasksSectionStale(t *testing.T) {
	// given
	// ... a slice synced while its first task was open
	m := Model{20: slice(20), 21: task(21, "Reads", under(20), labelled(labelReady), checked()),
		22: task(22, "Posts", under(20), deps(21))}
	m[20].Body, _ = syncedBody(m[20].Body, m.tasksSection(20))

	// when
	// ... the first task closes, so what can start changes, and the repo is validated
	m[21].Closed = true
	r := runOn([]*Issue{m[20], m[21], m[22]}, "validate")

	// then
	// ... progress alone is not drift: only the task list and the graph are compared
	mustSucceed(t, r)
}

func TestEveryBrokenRuleIsReported(t *testing.T) {
	// given
	// ... one issue breaking each rule
	model := []*Issue{
		{Number: 20, Title: "S", Labels: []string{labelSlice}, Body: "**Goal.** G.\n\n## Tasks\n\nstale\n"},
		{Number: 5, Title: "Orphan", Body: taskBody("W.", []string{"c"}, "")},
		task(6, "Retired", labelled("needs-triage")),
		task(21, "Ready, no criteria", under(20), labelled(labelReady), withBody("**What.** W.\n")),
		task(22, "Started unrefined", under(20), assigned()),
		task(23, "Done, unchecked", under(20), closed()),
		task(24, "Dropped", under(20), dropped()),
		task(25, "Needs the dropped one", under(20), deps(24)),
		task(26, "Loop a", under(20), deps(27)), task(27, "Loop b", under(20), deps(26)),
	}

	// when
	// ... it is validated
	r := runOn(model, "validate")

	// then
	// ... each problem is named, and the exit is non-zero
	if r.code == 0 {
		t.Fatal("exit 0")
	}
	mustContain(t, r.out, "#20: slice has no demo", "#20: the Tasks section is out of date",
		"#5: no slice and not labelled unplanned", "#6: carries the retired label 'needs-triage'",
		"#21: ready but has no acceptance criteria", "#22: in progress but never marked ready",
		"#23: closed as done with an unchecked criterion", "#25: depends on #24, which was dropped",
		"dependency cycle: #26 -> #27 -> #26")
}

// ---- board and ready -------------------------------------------------------

func TestBoardGroupsEachSliceByWhatCanStart(t *testing.T) {
	// given
	// ... a slice with a done, a ready, a doing and a blocked task, and one unplanned task
	model := []*Issue{slice(20), task(21, "Reads", under(20), checked(), closed()),
		task(22, "Posts", under(20), labelled(labelReady), deps(21)),
		task(23, "Config", under(20), labelled(labelReady), assigned()),
		task(24, "Wires it", under(20), deps(22)), task(5, "Unplanned one")}

	// when
	// ... the board is printed
	r := runOn(model, "board")

	// then
	// ... the slice shows NEXT, IN PROGRESS, BLOCKED with what it waits on, and a closed count
	mustSucceed(t, r)
	mustContain(t, r.out, "#20 Bitbucket pull requests cannot be reviewed  1/4 closed",
		"  NEXT\n    #22   [ready] Posts", "  IN PROGRESS\n    #23           Config",
		"  BLOCKED\n    #24           Wires it  waits on #22", "  CLOSED  1  (--done to list)",
		"Unplanned  0/1 closed\n  NEXT\n    #5            Unplanned one")
}

func TestBoardListsClosedTasksWithDone(t *testing.T) {
	// given
	// ... a slice with one done and one dropped task
	model := []*Issue{slice(20), task(21, "Reads", under(20), checked(), closed()), task(22, "Gone", under(20), dropped())}

	// when
	// ... the board is printed with --done
	r := runOn(model, "board", "--done")

	// then
	// ... each closed task is listed with how it closed
	mustSucceed(t, r)
	mustContain(t, r.out, "  CLOSED\n    #21           Reads  (done)\n    #22           Gone  (dropped)")
}

func TestReadyFlagsWeakCriteriaAThinDemoAndUnmetDeps(t *testing.T) {
	// given
	// ... a slice with a thin demo, and a task under it with weak criteria and an open dependency
	model := []*Issue{{Number: 20, Title: "S", Labels: []string{labelSlice}, Body: "**Demo.** It works.\n\n## Tasks\n"},
		task(21, "First", under(20)),
		task(22, "Second", under(20), deps(21), criteria("The adapter works properly",
			"Comments post and replies thread", "`review-assist` lists the pull requests"))}

	// when
	// ... the ready report runs for the second task
	r := runOn(model, "ready", "22")

	// then
	// ... the demo is called thin, the open dependency is named, and each weak criterion is flagged
	mustSucceed(t, r)
	mustContain(t, r.out, "demo=THIN", "unmet: #21 (todo) First",
		"[vague word; names nothing to run or read] The adapter works properly",
		"two things joined by 'and'", "[ok] `review-assist` lists the pull requests")
}

func TestIssueGetShowsATasksSliceAndDependants(t *testing.T) {
	// given
	// ... a slice with two tasks, the second depending on the first
	model := []*Issue{slice(20), task(21, "Reads", under(20)), task(22, "Posts", under(20), deps(21))}

	// when
	// ... the first task is read
	r := runOn(model, "issue.get", "21")

	// then
	// ... it names its slice and what needs it
	mustSucceed(t, r)
	mustContain(t, r.out, "#21 Reads\ntask of #20, todo", "needed by #22 (todo) Posts")
}

func TestIssueGetShowsASlicesTasks(t *testing.T) {
	// given
	// ... a slice with two tasks
	model := []*Issue{slice(20), task(21, "Reads", under(20)), task(22, "Posts", under(20), deps(21))}

	// when
	// ... the slice is read
	r := runOn(model, "issue.get", "20")

	// then
	// ... it lists its tasks with their status
	mustSucceed(t, r)
	mustContain(t, r.out, "slice, open", "  #21  todo    Reads", "  #22  todo    Posts")
}

// ---- blockers in other repositories ----------------------------------------

func TestABlockerInAnotherRepositoryIsNamedWithItsRepository(t *testing.T) {
	// given
	// ... a local #3, and a task blocked by issue 3 of another repository, which is open
	model := []*Issue{task(3, "Local three", checked(), closed()),
		task(5, "T", labelled(labelReady), blockedBy(Dep{Repo: "acme/api", Number: 3}))}

	// when
	// ... the board is printed and the task is started
	board := runOn(model, "board")
	start := runOn(model, "issue.update", "5", "--status", "doing")

	// then
	// ... it waits on acme/api#3, not on the local #3, which is done
	mustContain(t, board.out, "#5    [ready] T  waits on acme/api#3")
	mustContain(t, start.err, "#5 is blocked: waits on acme/api#3")
}

func TestABlockerInAnotherRepositoryIsUnmetUntilItsOwnStateIsDone(t *testing.T) {
	// given
	// ... tasks blocked by issues of another repository: one open, one done, one dropped, while local #3 is open
	elsewhere := func(closed, notPlanned bool) opt {
		return blockedBy(Dep{Repo: "acme/api", Number: 3, Closed: closed, NotPlanned: notPlanned})
	}
	model := []*Issue{task(3, "Local three"), task(5, "Open there", elsewhere(false, false)),
		task(6, "Done there", elsewhere(true, false)), task(7, "Dropped there", elsewhere(true, true))}

	// when
	// ... the board is printed
	r := runOn(model, "board")

	// then
	// ... the open and the dropped blockers hold their tasks back, the done one frees its task, and the local #3 plays no part
	mustSucceed(t, r)
	mustContain(t, r.out, "  NEXT\n    #3            Local three\n    #6            Done there\n",
		"  BLOCKED\n    #5            Open there  waits on acme/api#3\n    #7            Dropped there  waits on acme/api#3")
}

func TestReadyReportsADependencyThatIsNotAnIssueHere(t *testing.T) {
	// given
	// ... a task blocked by an issue in another repository, and by a local number that is not an issue
	model := []*Issue{task(5, "T", blockedBy(Dep{Number: 4}, Dep{Repo: "acme/api", Number: 3}))}

	// when
	// ... the ready report runs
	r := runOn(model, "ready", "5")

	// then
	// ... the missing local one and the other repository's one are both named
	mustSucceed(t, r)
	mustContain(t, r.out, "deps     2 declared", "MISSING: #4", "acme/api#3 (open, another repository)")
}

// ---- loading ---------------------------------------------------------------

func TestGraphQLPagesBecomeTheModel(t *testing.T) {
	// given
	// ... one page of the repo query: a slice, a doing task blocked by a local and a foreign issue, and a dropped task
	raw := `[{"data":{"repository":{"nameWithOwner":"me/app","issues":{"nodes":[
	  {"number":20,"title":"S","body":"","state":"OPEN","stateReason":null,"databaseId":200,
	   "labels":{"nodes":[{"name":"slice"}]},"assignees":{"nodes":[]},"parent":null,"blockedBy":{"nodes":[]}},
	  {"number":22,"title":"T","body":null,"state":"OPEN","stateReason":null,"databaseId":220,
	   "labels":{"nodes":[]},"assignees":{"nodes":[{"login":"me"}]},
	   "parent":{"number":20,"repository":{"nameWithOwner":"me/app"}},
	   "blockedBy":{"nodes":[
	     {"number":3,"state":"CLOSED","stateReason":"COMPLETED","repository":{"nameWithOwner":"acme/api"}},
	     {"number":21,"state":"CLOSED","stateReason":"NOT_PLANNED","repository":{"nameWithOwner":"me/app"}}]}},
	  {"number":21,"title":"D","body":"","state":"CLOSED","stateReason":"NOT_PLANNED","databaseId":210,
	   "labels":{"nodes":[]},"assignees":{"nodes":[]},
	   "parent":{"number":20,"repository":{"nameWithOwner":"me/app"}},"blockedBy":{"nodes":[]}}]}}}}]`

	// when
	// ... the pages are parsed
	m, err := parseIssues([]byte(raw))

	// then
	// ... status, parent, database ids and each blocker's repository and state come through
	if err != nil {
		t.Fatal(err)
	}
	if m[20].Status() != "open" || m[22].Status() != "doing" || m[21].Status() != "dropped" {
		t.Errorf("statuses: %s %s %s", m[20].Status(), m[22].Status(), m[21].Status())
	}
	if m[22].Parent != 20 || m[22].ID != 220 {
		t.Errorf("parent %d id %d", m[22].Parent, m[22].ID)
	}
	want := []Dep{{Number: 21, Closed: true, NotPlanned: true}, {Repo: "acme/api", Number: 3, Closed: true}}
	if !reflect.DeepEqual(m[22].Deps, want) {
		t.Errorf("deps = %+v, want %+v", m[22].Deps, want)
	}
}

func TestAPullRequestKeptAsAnIssueIsNotWork(t *testing.T) {
	// given
	// ... a closed issue labelled pull-request, the record of a PR ported from another repository
	raw := `[{"data":{"repository":{"nameWithOwner":"me/app","issues":{"nodes":[
	  {"number":1,"title":"PR: add a thing","body":"","state":"CLOSED","stateReason":"COMPLETED","databaseId":100,
	   "labels":{"nodes":[{"name":"pull-request"}]},"assignees":{"nodes":[]},"parent":null,"blockedBy":{"nodes":[]}}]}}}}]`

	// when
	// ... the pages are parsed
	m, err := parseIssues([]byte(raw))

	// then
	// ... it is left out, so validate does not ask for a slice or the unplanned label
	if err != nil {
		t.Fatal(err)
	}
	if len(m) != 0 {
		t.Errorf("want no issues, got %v", m.Sorted())
	}
}

// ---- dry run ---------------------------------------------------------------

func TestDryRunPrintsQuotedWritesWithoutRunningThem(t *testing.T) {
	// given
	// ... the real gh wrapper in dry run, with the ids of #21 known
	var out bytes.Buffer
	g := &ghCLI{dry: true, out: &out, ids: map[int]int64{21: 2100}}

	// when
	// ... a task with shell characters in its title is created and given a dependency
	n, err := g.Create("O'Brien's mail $(bounces)", "Body.", []string{labelUnplanned})
	if err != nil {
		t.Fatal(err)
	}
	if err := g.AddDep(n, 21); err != nil {
		t.Fatal(err)
	}

	// then
	// ... each write is printed as a quoted gh command, and the fake number is used
	mustContain(t, out.String(),
		`gh issue create --title 'O'\''Brien'\''s mail $(bounces)' --body-file - --label unplanned`+"\n  stdin: Body.",
		"gh api --method POST repos/{owner}/{repo}/issues/901/dependencies/blocked_by -F issue_id=2100 --silent")
}
