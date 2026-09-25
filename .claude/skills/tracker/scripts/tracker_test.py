#!/usr/bin/env python3
"""Tests for tracker.py, through main() with a fake gh. Nothing touches GitHub.

  python3 .claude/skills/tracker/scripts/tracker_test.py
"""
import contextlib
import io
import os
import sys
import unittest

sys.path.insert(0, os.path.dirname(os.path.abspath(__file__)))
import tracker  # noqa: E402
from tracker import Issue  # noqa: E402

DEMO = "`review-assist https://bitbucket.org/o/r/pull-requests/3` opens the pull request and shows its diff."


def slice_(n, title="Bitbucket pull requests cannot be reviewed", body=None, **kw):
    return Issue(n, title, body=body if body is not None else tracker.slice_body("Goal.", DEMO, [], [], []),
                 labels={"slice"}, **kw)


def task_(n, title, criteria=("`go test ./...` passes",), checked=False, parent=None, labels=(), **kw):
    body = tracker.task_body("What.", list(criteria))
    if checked:
        body = body.replace("- [ ]", "- [x]")
    labels = set(labels) | (set() if parent else {"unplanned"})
    return Issue(n, title, body=body, parent=parent, labels=labels, **kw)


class FakeGh:
    def __init__(self, model):
        self.model = model
        self.calls = []
        self.next = 100

    def load(self):
        return self.model

    def create(self, title, body, labels):
        self.next += 1
        self.calls.append(("create", self.next, title, body, tuple(labels)))
        return self.next

    def attach(self, parent, child):
        self.calls.append(("attach", parent, child))

    def add_dep(self, n, dep):
        self.calls.append(("add_dep", n, dep))

    def remove_dep(self, n, dep):
        self.calls.append(("remove_dep", n, dep))

    def edit(self, n, **kw):
        self.calls.append(("edit", n, {k: v for k, v in kw.items() if v is not None}))

    def close(self, n, reason, comment=""):
        self.calls.append(("close", n, reason, comment))

    def reopen(self, n):
        self.calls.append(("reopen", n))

    def comment(self, n, body):
        self.calls.append(("comment", n, body))

    def delete(self, n):
        self.calls.append(("delete", n))


def run(model, *argv):
    gh = FakeGh({i.number: i for i in model})
    out, err = io.StringIO(), io.StringIO()
    with contextlib.redirect_stdout(out), contextlib.redirect_stderr(err):
        code = tracker.main(list(argv), gh)
    return code, out.getvalue(), err.getvalue(), gh.calls


class TaskCreate(unittest.TestCase):
    def test_a_task_in_a_slice_is_a_sub_issue_with_its_dependencies(self):
        # given
        # ... a slice with one task already in it
        model = [slice_(20), task_(21, "Bitbucket CodeHost reads pull requests", parent=20)]

        # when
        # ... a second task is created in the slice, depending on the first
        code, out, err, calls = run(model, "task.create", "--title", "Bitbucket CodeHost posts comments",
                                    "--what", "Post comments and verdicts.", "--criterion", "`go test ./...` passes",
                                    "--criterion", "A comment posts to a real pull request", "--slice", "20",
                                    "--depends-on", "21")

        # then
        # ... it is created with What and Done when, attached to the slice, and depends on #21
        self.assertEqual(0, code, err)
        self.assertEqual("101\n", out)
        create = calls[0]
        self.assertEqual(("create", 101, "Bitbucket CodeHost posts comments"), create[:3])
        self.assertIn("**What.** Post comments and verdicts.", create[3])
        self.assertIn("**Done when.**\n\n- [ ] `go test ./...` passes\n- [ ] A comment posts to a real pull request", create[3])
        self.assertEqual((), create[4])
        self.assertEqual([("attach", 20, 101), ("add_dep", 101, 21)], calls[1:])

    def test_a_task_without_a_slice_is_labelled_unplanned(self):
        # given
        # ... no slice to put it in
        model = []

        # when
        # ... a task is created without --slice
        code, _, err, calls = run(model, "task.create", "--title", "Config files can leak a token",
                                  "--what", "W.", "--criterion", "`review-assist` refuses a 0644 config")

        # then
        # ... it is labelled unplanned and attached to nothing
        self.assertEqual(0, code, err)
        self.assertEqual(("unplanned",), calls[0][4])
        self.assertEqual(1, len(calls))

    def test_create_refuses_a_task_with_no_criteria(self):
        # given
        # ... a slice
        model = [slice_(20)]

        # when
        # ... a task is created with no --criterion
        code, _, err, calls = run(model, "task.create", "--title", "T", "--what", "W.", "--slice", "20")

        # then
        # ... nothing is written
        self.assertEqual(1, code)
        self.assertIn("--criterion", err)
        self.assertEqual([], calls)

    def test_create_refuses_a_parent_that_is_not_a_slice(self):
        # given
        # ... an unplanned task, which is not a slice
        model = [task_(5, "An unplanned task")]

        # when
        # ... a task is created under it
        code, _, err, calls = run(model, "task.create", "--title", "T", "--what", "W.", "--criterion", "c",
                                  "--slice", "5")

        # then
        # ... it is refused
        self.assertEqual(1, code)
        self.assertIn("#5 is not an open slice", err)
        self.assertEqual([], calls)

    def test_create_refuses_a_dependency_that_is_not_an_issue(self):
        # given
        # ... a repo where #9 is not an issue (a pull request, or missing)
        model = [slice_(20)]

        # when
        # ... a task depends on #9
        code, _, err, calls = run(model, "task.create", "--title", "T", "--what", "W.", "--criterion", "c",
                                  "--slice", "20", "--depends-on", "9")

        # then
        # ... it is refused
        self.assertEqual(1, code)
        self.assertIn("#9 is not an issue", err)
        self.assertEqual([], calls)


class SliceCreate(unittest.TestCase):
    def test_a_slice_has_every_section_and_the_slice_label(self):
        # given
        # ... an empty repo
        model = []

        # when
        # ... a slice is created with a goal, demo and one implemented ADR
        code, out, err, calls = run(model, "slice.create", "--title", "Bitbucket pull requests cannot be reviewed",
                                    "--goal", "Review Bitbucket pull requests.", "--demo", DEMO,
                                    "--implements", "ADR 0001")

        # then
        # ... the body carries every section, empty ones say None, and it is labelled slice
        self.assertEqual(0, code, err)
        body = calls[0][3]
        self.assertIn(f"**Demo.** {DEMO}", body)
        self.assertIn("## Implements\n\n- ADR 0001", body)
        self.assertIn("## Out of scope\n\nNone.", body)
        self.assertIn("## Tasks\n\n" + tracker.NO_TASKS_YET, body)
        self.assertEqual(("slice",), calls[0][4])

    def test_create_refuses_a_slice_with_no_demo(self):
        # given
        # ... an empty repo
        model = []

        # when
        # ... a slice is created with a blank demo
        code, _, err, calls = run(model, "slice.create", "--title", "T", "--goal", "G.", "--demo", " ")

        # then
        # ... it is refused
        self.assertEqual(1, code)
        self.assertIn("--demo", err)
        self.assertEqual([], calls)


class Status(unittest.TestCase):
    def test_doing_refuses_a_task_that_is_not_ready(self):
        # given
        # ... a task still at todo
        model = [task_(5, "T")]

        # when
        # ... it is started
        code, _, err, calls = run(model, "issue.update", "5", "--status", "doing")

        # then
        # ... it is refused and points at refining
        self.assertEqual(1, code)
        self.assertIn("refine", err)
        self.assertEqual([], calls)

    def test_doing_refuses_a_ready_task_that_is_blocked(self):
        # given
        # ... a ready task that depends on an open one
        model = [slice_(20), task_(21, "First", parent=20),
                 task_(22, "Second", parent=20, labels={"ready"}, deps=[21])]

        # when
        # ... the blocked task is started
        code, _, err, calls = run(model, "issue.update", "22", "--status", "doing")

        # then
        # ... it is refused, naming what it waits on
        self.assertEqual(1, code)
        self.assertIn("waits on #21", err)
        self.assertEqual([], calls)

    def test_doing_assigns_a_ready_unblocked_task(self):
        # given
        # ... a ready task whose dependency is done
        model = [slice_(20), task_(21, "First", parent=20, checked=True, closed=True),
                 task_(22, "Second", parent=20, labels={"ready"}, deps=[21])]

        # when
        # ... it is started
        code, _, err, calls = run(model, "issue.update", "22", "--status", "doing")

        # then
        # ... you are assigned
        self.assertEqual(0, code, err)
        self.assertEqual([("edit", 22, {"assign": True})], calls)

    def test_todo_on_a_task_already_at_todo_writes_nothing(self):
        # given
        # ... a task at todo: open, no ready label, nobody assigned
        model = [task_(5, "T")]

        # when
        # ... it is set to todo
        code, _, err, calls = run(model, "issue.update", "5", "--status", "todo")

        # then
        # ... there is nothing to change, so nothing is sent to gh
        self.assertEqual(0, code, err)
        self.assertEqual([], calls)

    def test_ready_adds_the_label(self):
        # given
        # ... a task at todo
        model = [task_(5, "T")]

        # when
        # ... it is marked ready
        code, _, err, calls = run(model, "issue.update", "5", "--status", "ready")

        # then
        # ... the ready label is added
        self.assertEqual(0, code, err)
        self.assertEqual([("edit", 5, {"add_label": "ready"})], calls)

    def test_done_refuses_unchecked_criteria(self):
        # given
        # ... a task with an unchecked criterion
        model = [task_(5, "T", labels={"ready"})]

        # when
        # ... it is closed as done
        code, _, err, calls = run(model, "issue.update", "5", "--status", "done")

        # then
        # ... it is refused, naming the criterion
        self.assertEqual(1, code)
        self.assertIn("`go test ./...` passes", err)
        self.assertEqual([], calls)

    def test_done_closes_a_task_whose_criteria_are_ticked(self):
        # given
        # ... a task with every criterion ticked
        model = [task_(5, "T", labels={"ready"}, checked=True)]

        # when
        # ... it is closed as done with a note
        code, _, err, calls = run(model, "issue.update", "5", "--status", "done", "--note", "Landed in #12.")

        # then
        # ... it closes as completed, the note as the closing comment
        self.assertEqual(0, code, err)
        self.assertEqual([("close", 5, "completed", "Landed in #12.")], calls)

    def test_dropped_needs_a_note(self):
        # given
        # ... a task
        model = [task_(5, "T")]

        # when
        # ... it is dropped without a reason
        code, _, err, calls = run(model, "issue.update", "5", "--status", "dropped")

        # then
        # ... it is refused
        self.assertEqual(1, code)
        self.assertIn("--note", err)
        self.assertEqual([], calls)

    def test_a_slice_cannot_close_over_open_tasks(self):
        # given
        # ... a slice with an open task
        model = [slice_(20), task_(21, "First", parent=20)]

        # when
        # ... the slice is closed as done
        code, _, err, calls = run(model, "issue.update", "20", "--status", "done")

        # then
        # ... it is refused, naming the open task
        self.assertEqual(1, code)
        self.assertIn("#21", err)
        self.assertEqual([], calls)

    def test_a_slice_takes_only_done_or_dropped(self):
        # given
        # ... a slice
        model = [slice_(20)]

        # when
        # ... it is marked ready
        code, _, err, calls = run(model, "issue.update", "20", "--status", "ready")

        # then
        # ... it is refused: readiness belongs to tasks
        self.assertEqual(1, code)
        self.assertIn("come from its tasks", err)
        self.assertEqual([], calls)


class Links(unittest.TestCase):
    def test_depends_on_refuses_a_cycle(self):
        # given
        # ... #23 depends on #22, which depends on #21
        model = [slice_(20), task_(21, "A", parent=20), task_(22, "B", parent=20, deps=[21]),
                 task_(23, "C", parent=20, deps=[22])]

        # when
        # ... #21 is made to depend on #23
        code, _, err, calls = run(model, "issue.link.add", "21", "depends-on", "23")

        # then
        # ... it is refused as a cycle
        self.assertEqual(1, code)
        self.assertIn("cycle", err)
        self.assertEqual([], calls)

    def test_removing_a_related_line_leaves_no_gap(self):
        # given
        # ... a body ending with a Related line, then a Discovered during line
        model = [task_(7, "A"), task_(8, "B"), task_(9, "C")]
        model[0].body += "\nRelated: #8\n\nDiscovered during #9\n"

        # when
        # ... the Related line is removed
        code, _, err, calls = run(model, "issue.link.remove", "7", "relates-to", "8")

        # then
        # ... one blank line separates what remains
        self.assertEqual(0, code, err)
        self.assertTrue(calls[0][2]["body"].endswith("## Notes\n\nDiscovered during #9\n"))

    def test_relates_to_appends_a_line_to_the_body(self):
        # given
        # ... two tasks
        model = [task_(7, "A"), task_(8, "B")]

        # when
        # ... #7 is related to #8
        code, _, err, calls = run(model, "issue.link.add", "7", "relates-to", "8")

        # then
        # ... the body ends with the Related line
        self.assertEqual(0, code, err)
        self.assertTrue(calls[0][2]["body"].endswith("\n\nRelated: #8\n"))


class Sync(unittest.TestCase):
    def test_sync_writes_the_task_list_frontier_and_graph(self):
        # given
        # ... a slice with a done task, a task that can start, and one blocked on it
        model = [slice_(20), task_(21, "Reads", parent=20, checked=True, closed=True),
                 task_(22, 'Posts "comments"', parent=20, deps=[21]), task_(23, "Wires it", parent=20, deps=[22])]

        # when
        # ... the slice is synced
        code, out, err, calls = run(model, "slice.sync", "20")

        # then
        # ... the Tasks section lists every task, what can start, and each edge, and the other sections survive
        self.assertEqual(0, code, err)
        body = calls[0][2]["body"]
        self.assertIn("- #21\n- #22\n- #23", body)
        self.assertIn("Startable now: #22.", body)
        self.assertIn('t22["#22 Posts #quot;comments#quot;"]', body)
        self.assertIn("t21 --> t22\n  t22 --> t23", body)
        self.assertIn("## Notes", body)
        self.assertIn(f"**Demo.** {DEMO}", body)

    def test_sync_refuses_a_slice_with_no_tasks_heading(self):
        # given
        # ... a slice whose body lost its Tasks heading
        model = [slice_(20, body=f"**Demo.** {DEMO}\n")]

        # when
        # ... it is synced
        code, _, err, calls = run(model, "slice.sync", "20")

        # then
        # ... it is refused rather than guessing where the section goes
        self.assertEqual(1, code)
        self.assertIn("## Tasks", err)
        self.assertEqual([], calls)


class Validate(unittest.TestCase):
    def test_a_clean_model_passes(self):
        # given
        # ... a synced slice with a done task and a ready one that depends on it, and an unplanned task
        model = {i.number: i for i in [
            slice_(20), task_(21, "Reads", parent=20, checked=True, closed=True),
            task_(22, "Posts", parent=20, labels={"ready"}, deps=[21]), task_(5, "Unplanned")]}
        model[20].body = tracker.synced_body(model[20].body, tracker.tasks_section(model, 20))

        # when
        # ... it is validated
        code, out, err, _ = run(list(model.values()), "validate")

        # then
        # ... there is nothing to report
        self.assertEqual(0, code, out + err)
        self.assertEqual("ok\n", out)

    def test_closing_a_task_does_not_make_the_tasks_section_stale(self):
        # given
        # ... a slice synced while its first task was open
        model = {i.number: i for i in [slice_(20), task_(21, "Reads", parent=20, labels={"ready"}, checked=True),
                                       task_(22, "Posts", parent=20, deps=[21])]}
        model[20].body = tracker.synced_body(model[20].body, tracker.tasks_section(model, 20))

        # when
        # ... the first task closes, so what can start changes, and the repo is validated
        model[21].closed = True
        code, out, err, _ = run(list(model.values()), "validate")

        # then
        # ... progress alone is not drift: only the task list and the graph are compared
        self.assertEqual(0, code, out + err)

    def test_every_broken_rule_is_reported(self):
        # given
        # ... one issue breaking each rule
        model = [
            slice_(20, body="**Goal.** G.\n\n## Tasks\n\nstale\n"),        # no demo, stale Tasks
            Issue(5, "Orphan", body=tracker.task_body("W.", ["c"])),       # no slice, not unplanned
            task_(6, "Retired", labels={"needs-triage"}),                   # retired label
            task_(21, "Ready, no criteria", criteria=(), parent=20, labels={"ready"}),
            task_(22, "Started unrefined", parent=20, assignees=["me"]),
            task_(23, "Done, unchecked", parent=20, closed=True),
            task_(24, "Dropped", parent=20, closed=True, not_planned=True),
            task_(25, "Needs the dropped one", parent=20, deps=[24]),
            task_(26, "Loop a", parent=20, deps=[27]), task_(27, "Loop b", parent=20, deps=[26]),
        ]

        # when
        # ... it is validated
        code, out, err, _ = run(model, "validate")

        # then
        # ... each problem is named, and the exit is non-zero
        self.assertEqual(1, code)
        self.assertIn("#20: slice has no demo", out)
        self.assertIn("#20: the Tasks section is out of date", out)
        self.assertIn("#5: no slice and not labelled unplanned", out)
        self.assertIn("#6: carries the retired label 'needs-triage'", out)
        self.assertIn("#21: ready but has no acceptance criteria", out)
        self.assertIn("#22: in progress but never marked ready", out)
        self.assertIn("#23: closed as done with an unchecked criterion", out)
        self.assertIn("#25: depends on #24, which was dropped", out)
        self.assertIn("dependency cycle: #26 -> #27 -> #26", out)


class Board(unittest.TestCase):
    def test_board_groups_each_slice_by_what_can_start(self):
        # given
        # ... a slice with a done, a ready, a doing and a blocked task, and one unplanned task
        model = [slice_(20), task_(21, "Reads", parent=20, checked=True, closed=True),
                 task_(22, "Posts", parent=20, labels={"ready"}, deps=[21]),
                 task_(23, "Config", parent=20, labels={"ready"}, assignees=["me"]),
                 task_(24, "Wires it", parent=20, deps=[22]), task_(5, "Unplanned one")]

        # when
        # ... the board is printed without colour
        code, out, err, _ = run(model, "board", "--no-colour")

        # then
        # ... the slice shows NEXT, IN PROGRESS, BLOCKED with what it waits on, and a closed count
        self.assertEqual(0, code, err)
        self.assertIn("#20 Bitbucket pull requests cannot be reviewed  1/4 closed", out)
        self.assertIn("  NEXT\n    #22   [ready] Posts", out)
        self.assertIn("  IN PROGRESS\n    #23           Config", out)
        self.assertIn("  BLOCKED\n    #24           Wires it  waits on #22", out)
        self.assertIn("  CLOSED  1  (--done to list)", out)
        # ... and unplanned work gets its own group
        self.assertIn("Unplanned  0/1 closed\n  NEXT\n    #5            Unplanned one", out)


class Ready(unittest.TestCase):
    def test_the_report_flags_weak_criteria_a_thin_demo_and_unmet_deps(self):
        # given
        # ... a slice with a thin demo, and a task under it with weak criteria and an open dependency
        model = [slice_(20, body="**Demo.** It works.\n\n## Tasks\n"), task_(21, "First", parent=20),
                 task_(22, "Second", parent=20, deps=[21], criteria=(
                     "The adapter works properly", "Comments post and replies thread",
                     "`review-assist` lists the pull requests"))]

        # when
        # ... the ready report runs for the second task
        code, out, err, _ = run(model, "ready", "22")

        # then
        # ... the demo is called thin, the open dependency is named, and each weak criterion is flagged
        self.assertEqual(0, code, err)
        self.assertIn("demo=THIN", out)
        self.assertIn("unmet: #21 (todo)", out)
        self.assertIn("[vague word; names nothing to run or read] The adapter works properly", out)
        self.assertIn("two things joined by 'and'", out)
        self.assertIn("[ok] `review-assist` lists the pull requests", out)


class Parse(unittest.TestCase):
    def test_graphql_pages_become_the_model(self):
        # given
        # ... one page of the repo query with a slice, a doing task and a dropped one
        def node(n, state="OPEN", reason=None, labels=(), assignees=(), parent=None, blocked_by=()):
            return {"number": n, "title": f"T{n}", "body": None, "state": state, "stateReason": reason,
                    "databaseId": n * 10, "labels": {"nodes": [{"name": l} for l in labels]},
                    "assignees": {"nodes": [{"login": a} for a in assignees]},
                    "parent": {"number": parent} if parent else None,
                    "blockedBy": {"nodes": [{"number": b} for b in blocked_by]}}
        pages = [{"data": {"repository": {"issues": {"nodes": [
            node(20, labels=["slice"]), node(22, assignees=["me"], parent=20, blocked_by=[21]),
            node(21, state="CLOSED", reason="NOT_PLANNED", parent=20)]}}}}]

        # when
        # ... the pages are parsed
        model = tracker.parse_issues(pages)

        # then
        # ... status, parent, dependencies and database ids come through
        self.assertEqual("open", model[20].status)
        self.assertEqual("doing", model[22].status)
        self.assertEqual("dropped", model[21].status)
        self.assertEqual(20, model[22].parent)
        self.assertEqual([21], model[22].deps)
        self.assertEqual(220, model[22].id)
        self.assertEqual("", model[20].body)


if __name__ == "__main__":
    unittest.main(verbosity=1)
