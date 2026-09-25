#!/usr/bin/env python3
"""tracker - slices and tasks in this repo's GitHub Issues.

A slice is an issue labelled `slice`: one outcome you can demo. A task is one
mergeable pull request: a sub-issue of its slice, or an issue labelled
`unplanned` when it has none. `depends-on` (a native GitHub dependency) is the
only order between tasks.

Every write refuses what GitHub would accept but the model forbids: a pull
request number, a dependency cycle, starting a task that is not ready or still
blocked, closing a task with unchecked criteria, closing a slice over open
tasks, dropping without a reason. Reads load the whole repo in one query.

Set TRACKER_DRY_RUN=1 to print the writes instead of running them.
Needs only Python 3.9 and gh.
"""
from __future__ import annotations

import argparse
import json
import os
import re
import subprocess
import sys
from dataclasses import dataclass, field
from typing import Dict, List, Optional

SLICE, READY, UNPLANNED = "slice", "ready", "unplanned"
RETIRED_LABELS = ("needs-triage", "ready-for-agent")
TASK_STATUSES = ("todo", "ready", "doing", "done", "dropped")
SLICE_STATUSES = ("done", "dropped")
RELATIONS = ("depends-on", "relates-to", "discovered-during")

# Words that promise a quality rather than name an observation.
VAGUE = r"\b(correctly|properly|works?|good|clean|robust|appropriate|reasonable|as expected|sensible|nicely)\b"
OBSERVABLE = (r"`|\b(returns?|prints?|exits?|fails?|passes|contains?|refuses?|stops?|blocks?|shows?|rejects?|"
              r"accepts?|applies|matches|resolves?|covers?|posts?|lists?|opens?|reads?|writes?)\b")

NO_TASKS_YET = "_None yet. Run `to-tasks`._"


class Refused(Exception):
    pass


@dataclass
class Issue:
    number: int
    title: str
    body: str = ""
    id: int = 0  # database id, which the sub-issue and dependency endpoints take
    closed: bool = False
    not_planned: bool = False
    labels: set = field(default_factory=set)
    assignees: list = field(default_factory=list)
    parent: Optional[int] = None
    deps: list = field(default_factory=list)  # numbers this issue depends on

    @property
    def is_slice(self) -> bool:
        return SLICE in self.labels

    @property
    def status(self) -> str:
        if self.closed:
            return "dropped" if self.not_planned else "done"
        if self.is_slice:
            return "open"
        if self.assignees:
            return "doing"
        return "ready" if READY in self.labels else "todo"


Model = Dict[int, Issue]


# ---- reading ---------------------------------------------------------------

QUERY = """query($owner: String!, $repo: String!, $endCursor: String) {
  repository(owner: $owner, name: $repo) {
    issues(first: 100, after: $endCursor, states: [OPEN, CLOSED]) {
      pageInfo { hasNextPage endCursor }
      nodes {
        number title body state stateReason databaseId
        labels(first: 20) { nodes { name } }
        assignees(first: 5) { nodes { login } }
        parent { number }
        blockedBy(first: 50) { nodes { number } }
      }
    }
  }
}"""


def parse_issues(pages: list) -> Model:
    model: Model = {}
    for page in pages:
        for n in page["data"]["repository"]["issues"]["nodes"]:
            model[n["number"]] = Issue(
                number=n["number"], title=n["title"], body=n["body"] or "", id=n["databaseId"],
                closed=n["state"] == "CLOSED", not_planned=n["stateReason"] == "NOT_PLANNED",
                labels={l["name"] for l in n["labels"]["nodes"]},
                assignees=[a["login"] for a in n["assignees"]["nodes"]],
                parent=(n["parent"] or {}).get("number"),
                deps=sorted(b["number"] for b in n["blockedBy"]["nodes"]),
            )
    return model


def tasks_of(model: Model, slice_no: Optional[int]) -> List[Issue]:
    """A slice's tasks, or with None the unplanned ones."""
    if slice_no is None:
        return [i for _, i in sorted(model.items()) if not i.is_slice and i.parent is None]
    return [i for _, i in sorted(model.items()) if i.parent == slice_no]


def unmet(model: Model, issue: Issue) -> List[int]:
    """Dependencies not done. A dropped dependency never will be, so it counts."""
    return [d for d in issue.deps if d in model and model[d].status != "done"]


def frontier(model: Model, slice_no: Optional[int]) -> List[int]:
    return [t.number for t in tasks_of(model, slice_no)
            if t.status in ("todo", "ready") and not unmet(model, t)]


def cycles(model: Model) -> List[List[int]]:
    found, state = [], {}

    def walk(n, path):
        if state.get(n) == 1:  # back edge
            found.append(path[path.index(n):] + [n])
            return
        if state.get(n) == 2:
            return
        state[n] = 1
        for d in model[n].deps if n in model else []:
            walk(d, path + [n])
        state[n] = 2

    for n in sorted(model):
        walk(n, [])
    seen, out = set(), []
    for c in found:  # one entry per cycle, not per rotation
        if frozenset(c) not in seen:
            seen.add(frozenset(c))
            out.append(c)
    return out


def reaches(model: Model, start: int, goal: int) -> bool:
    todo, seen = [start], set()
    while todo:
        n = todo.pop()
        if n == goal:
            return True
        if n not in seen and n in model:
            seen.add(n)
            todo.extend(model[n].deps)
    return False


def demo_of(body: str) -> str:
    m = re.search(r"\*\*Demo\.?\*\*[ \t]*(.*?)(?:\n\n|\n##|\Z)", body, re.S)
    return " ".join(m.group(1).split()) if m else ""


def criteria_of(body: str) -> List[tuple]:
    """(checked, text) for each checkbox under Done when."""
    m = re.search(r"\*\*Done when\.?\*\*(.*?)(?:\n## |\Z)", body, re.S)
    if not m:
        return []
    return [(box == "x", text.strip()) for box, text in re.findall(r"^- \[([ xX])\] (.+)$", m.group(1), re.M)]


def flags(criterion: str) -> List[str]:
    out = []
    if re.search(VAGUE, criterion, re.I):
        out.append("vague word")
    if re.search(r"\sand\s", criterion) and not criterion.startswith("Given"):
        out.append("two things joined by 'and' — split")
    if not re.search(OBSERVABLE, criterion, re.I):
        out.append("names nothing to run or read")
    return out


# ---- rendering -------------------------------------------------------------

def slice_body(goal: str, demo: str, implements: List[str], out_of_scope: List[str], open_: List[str]) -> str:
    def items(xs):
        return "\n".join(f"- {x}" for x in xs) if xs else "None."
    return (f"**Goal.** {goal}\n\n**Demo.** {demo}\n\n"
            f"## Implements\n\n{items(implements)}\n\n"
            f"## Out of scope\n\n{items(out_of_scope)}\n\n"
            f"## Open in this slice\n\n{items(open_)}\n\n"
            f"## Tasks\n\n{NO_TASKS_YET}\n\n"
            f"## Notes\n")


def task_body(what: str, criteria: List[str], note: str = "") -> str:
    boxes = "\n".join(f"- [ ] {c}" for c in criteria)
    return f"**What.** {what}\n\n**Done when.**\n\n{boxes}\n\n## Notes\n" + (f"\n{note}\n" if note else "")


def mermaid(model: Model, slice_no: int) -> str:
    own = {t.number: t for t in tasks_of(model, slice_no)}
    ext = sorted({d for t in own.values() for d in t.deps if d not in own})

    def label(n):
        title = model[n].title if n in model else "?"
        return "#" + str(n) + " " + title.replace('"', "#quot;")

    lines = ["```mermaid", "flowchart LR"]
    lines += [f'  t{n}["{label(n)}"]:::external' for n in ext]
    lines += [f'  t{n}["{label(n)}"]' for n in own]
    lines += [f"  t{d} --> t{n}" for n, t in own.items() for d in t.deps]
    if ext:
        lines.append("  classDef external stroke-dasharray: 4 4")
    lines.append("```")
    return "\n".join(lines)


def tasks_section(model: Model, slice_no: int) -> str:
    tasks = tasks_of(model, slice_no)
    if not tasks:
        return f"## Tasks\n\n{NO_TASKS_YET}\n\n"
    links = "\n".join(f"- #{t.number}" for t in tasks)
    free = frontier(model, slice_no)
    startable = ", ".join(f"#{n}" for n in free) if free else "nothing: every open task has an unmet dependency or is in progress"
    return ("## Tasks\n\n"
            "Each task is a sub-issue. `depends-on` says when each can start; there is no order beyond that.\n\n"
            f"{links}\n\nStartable now: {startable}.\n\n{mermaid(model, slice_no)}\n\n")


def synced_body(body: str, section: str) -> str:
    if not re.search(r"^## Tasks\s*$", body, re.M):
        raise Refused("the slice body has no '## Tasks' section")
    return re.sub(r"^## Tasks\s*\n.*?(?=^## |\Z)", lambda _: section, body, count=1, flags=re.S | re.M)


def structure(body: str) -> str:
    """A body without its "Startable now" line: that changes with progress, not with the plan."""
    return re.sub(r"^Startable now: .*$", "", body, flags=re.M)


def link_line(relation: str, other: int) -> str:
    return {"relates-to": f"Related: #{other}", "discovered-during": f"Discovered during #{other}"}[relation]


def validate(model: Model) -> List[str]:
    errs = []
    for n, i in sorted(model.items()):
        for l in sorted(i.labels & set(RETIRED_LABELS)):
            errs.append(f"#{n}: carries the retired label '{l}'")
        parent = model.get(i.parent) if i.parent else None
        if i.is_slice:
            if i.parent:
                errs.append(f"#{n}: a slice cannot be a task of #{i.parent}")
            if READY in i.labels:
                errs.append(f"#{n}: a slice cannot be ready; readiness belongs to tasks")
            if UNPLANNED in i.labels:
                errs.append(f"#{n}: a slice cannot be unplanned")
            if not demo_of(i.body):
                errs.append(f"#{n}: slice has no demo")
            if not i.closed and re.search(r"^## Tasks\s*$", i.body, re.M) \
                    and structure(synced_body(i.body, tasks_section(model, n))) != structure(i.body):
                errs.append(f"#{n}: the Tasks section is out of date; run: tracker.py slice.sync {n}")
            continue
        if i.parent is None and UNPLANNED not in i.labels:
            errs.append(f"#{n}: no slice and not labelled unplanned")
        if i.parent is not None and UNPLANNED in i.labels:
            errs.append(f"#{n}: labelled unplanned but is a task of #{i.parent}")
        if i.parent is not None and parent is not None and not parent.is_slice:
            errs.append(f"#{n}: its parent #{i.parent} is not labelled slice")
        for d in i.deps:
            if d in model and model[d].is_slice:
                errs.append(f"#{n}: depends on slice #{d}; dependencies are between tasks")
            elif not i.closed and d in model and model[d].status == "dropped":
                errs.append(f"#{n}: depends on #{d}, which was dropped")
        crit = criteria_of(i.body)
        if i.status == "doing" and READY not in i.labels:
            errs.append(f"#{n}: in progress but never marked ready")
        if i.status in ("ready", "doing") and not crit:
            errs.append(f"#{n}: {i.status} but has no acceptance criteria")
        if i.status == "done":
            for checked, text in crit:
                if not checked:
                    errs.append(f"#{n}: closed as done with an unchecked criterion: {text}")
    for c in cycles(model):
        errs.append("dependency cycle: " + " -> ".join(f"#{n}" for n in c))
    return errs


def paint(on: bool):
    codes = {"head": "1", "green": "32", "yellow": "33", "dim": "2", "cyan": "36"}
    if not on:
        return lambda style, text: text
    return lambda style, text: f"\033[{codes[style]}m{text}\033[0m"


def waits_on(numbers: List[int]) -> str:
    names = [f"#{n}" for n in numbers]
    return ", ".join(names) if len(names) <= 3 else f"{', '.join(names[:2])} +{len(names) - 2} more"


def board(model: Model, show_done: bool = False, colour: bool = False) -> str:
    c = paint(colour)
    groups = [(s.number, f"#{s.number} {s.title}") for _, s in sorted(model.items()) if s.is_slice and not s.closed]
    if tasks_of(model, None):
        groups.append((None, "Unplanned"))
    out = []
    for key, title in groups:
        tasks = tasks_of(model, key)
        done = [t for t in tasks if t.closed]
        doing = [t for t in tasks if t.status == "doing"]
        rest = [t for t in tasks if t.status in ("todo", "ready")]
        nxt = [t for t in rest if not unmet(model, t)]
        blocked = [t for t in rest if unmet(model, t)]
        out.append(c("head", title) + "  " + c("dim", f"{len(done)}/{len(tasks)} closed"))

        def row(t, style, extra=""):
            tag = c("cyan", "[ready] ") if t.status == "ready" else "        "
            out.append(f"    {c(style, ('#' + str(t.number)).ljust(6))}{tag}{t.title}{extra}")

        for label, members, style in (("NEXT", nxt, "green"), ("IN PROGRESS", doing, "yellow")):
            if members:
                out.append(f"  {c(style, label)}")
                for t in members:
                    row(t, style)
        if blocked:
            out.append(f"  {c('dim', 'BLOCKED')}")
            for t in blocked:
                row(t, "dim", c("dim", f"  waits on {waits_on(unmet(model, t))}"))
        if done and show_done:
            out.append(f"  {c('dim', 'CLOSED')}")
            for t in done:
                row(t, "dim", c("dim", f"  ({t.status})"))
        elif done:
            out.append(f"  {c('dim', f'CLOSED  {len(done)}  (--done to list)')}")
        out.append("")
    return "\n".join(out).rstrip() or "No open slices and no unplanned tasks."


def ready_report(model: Model, n: int) -> str:
    t = model[n]
    out = [f"task     #{n} {t.title}  status={t.status}"]
    if t.parent is None:
        out.append("slice    none" + ("  (labelled unplanned, so only the task's own criteria apply)"
                                      if UNPLANNED in t.labels else "  PROBLEM: no slice and not labelled unplanned"))
    else:
        s = model.get(t.parent)
        demo = demo_of(s.body) if s else ""
        if not demo:
            out.append(f"slice    #{t.parent}  BLOCKED: no demo")
        else:
            thin = len(demo.split()) < 8 or "`" not in demo  # a demo names a command
            out.append(f"slice    #{t.parent}  demo={'THIN — read it before refining' if thin else 'looks concrete'}")
            out.append(f"         \"{demo[:110]}{'...' if len(demo) > 110 else ''}\"")
    if not t.deps:
        out.append("deps     none declared — check that is true, not just unfilled")
    else:
        left = unmet(model, t)
        out.append(f"deps     {len(t.deps)} declared" + (
            "  unmet: " + ", ".join(f"#{d} ({model[d].status})" for d in left) if left else "  all done"))
    crit = criteria_of(t.body)
    out.append(f"criteria {len(crit)}")
    if not crit:
        out.append("         PROBLEM: none (no '**Done when.**' checklist)")
    for _, text in crit:
        f = flags(text)
        out.append(f"         [{'; '.join(f) if f else 'ok'}] {text[:90]}")
    out.append("\nready when: the slice demo is concrete and every criterion is checkable by someone else; "
               f"then run: tracker.py issue.update {n} --status ready. Blocked-ness is reported separately.")
    return "\n".join(out)


# ---- writing ---------------------------------------------------------------

class Gh:
    """Every read and write goes through gh, which uses your own gh auth login."""

    def __init__(self, dry: bool = False):
        self.dry = dry
        self.ids: Dict[int, int] = {}
        self.next_fake = 900

    def load(self) -> Model:
        out = subprocess.run(
            ["gh", "api", "graphql", "--paginate", "--slurp", "-f", f"query={QUERY}",
             "-F", "owner={owner}", "-F", "repo={repo}"],
            check=True, capture_output=True, text=True).stdout
        model = parse_issues(json.loads(out))
        self.ids = {n: i.id for n, i in model.items()}
        return model

    def _write(self, args: List[str], stdin: Optional[str] = None) -> str:
        if self.dry:
            print("gh " + " ".join(_quote(a) for a in args) + (f"\n  stdin: {stdin}" if stdin else ""))
            return ""
        return subprocess.run(["gh"] + args, input=stdin, check=True, capture_output=True, text=True).stdout

    def create(self, title: str, body: str, labels: List[str]) -> int:
        args = ["issue", "create", "--title", title, "--body-file", "-"]
        for l in labels:
            args += ["--label", l]
        out = self._write(args, body)
        if self.dry:
            self.next_fake += 1
            return self.next_fake
        return int(out.strip().rsplit("/", 1)[-1])

    def id_of(self, n: int) -> int:
        if n not in self.ids:
            if self.dry:
                return n * 1000
            out = subprocess.run(["gh", "api", f"repos/{{owner}}/{{repo}}/issues/{n}", "--jq", ".id"],
                                 check=True, capture_output=True, text=True).stdout
            self.ids[n] = int(out)
        return self.ids[n]

    def attach(self, parent: int, child: int):
        self._write(["api", "--method", "POST", f"repos/{{owner}}/{{repo}}/issues/{parent}/sub_issues",
                     "-F", f"sub_issue_id={self.id_of(child)}", "--silent"])

    def add_dep(self, n: int, dep: int):
        self._write(["api", "--method", "POST", f"repos/{{owner}}/{{repo}}/issues/{n}/dependencies/blocked_by",
                     "-F", f"issue_id={self.id_of(dep)}", "--silent"])

    def remove_dep(self, n: int, dep: int):
        self._write(["api", "--method", "DELETE",
                     f"repos/{{owner}}/{{repo}}/issues/{n}/dependencies/blocked_by/{self.id_of(dep)}", "--silent"])

    def edit(self, n: int, title=None, body=None, add_label=None, remove_label=None, assign=None):
        args, stdin = ["issue", "edit", str(n)], None
        if title is not None:
            args += ["--title", title]
        if body is not None:
            args += ["--body-file", "-"]
            stdin = body
        if add_label:
            args += ["--add-label", add_label]
        if remove_label:
            args += ["--remove-label", remove_label]
        if assign is True:
            args += ["--add-assignee", "@me"]
        if assign is False:
            args += ["--remove-assignee", "@me"]
        self._write(args, stdin)

    def close(self, n: int, reason: str, comment: str = ""):
        self._write(["issue", "close", str(n), "--reason", reason] + (["--comment", comment] if comment else []))

    def reopen(self, n: int):
        self._write(["issue", "reopen", str(n)])

    def comment(self, n: int, body: str):
        self._write(["issue", "comment", str(n), "--body-file", "-"], body)

    def delete(self, n: int):
        self._write(["issue", "delete", str(n), "--yes"])


def _quote(a: str) -> str:
    return a if re.fullmatch(r"[\w@%+=:,./{}-]+", a) else "'" + a.replace("'", "'\\''") + "'"


def issue(model: Model, n: int) -> Issue:
    if n not in model:
        raise Refused(f"#{n} is not an issue in this repo (issues and pull requests share numbers)")
    return model[n]


def task(model: Model, n: int) -> Issue:
    i = issue(model, n)
    if i.is_slice:
        raise Refused(f"#{n} is a slice, not a task")
    return i


def read_body(args) -> Optional[str]:
    if getattr(args, "body_file", None):
        with open(args.body_file) as f:
            return f.read()
    return getattr(args, "body", None)


# ---- commands --------------------------------------------------------------

def cmd_board(a, model, gh):
    colour = sys.stdout.isatty() and not a.no_colour and "NO_COLOR" not in os.environ
    print(board(model, a.done, colour))


def cmd_validate(a, model, gh):
    errs = validate(model)
    for e in errs:
        print(e)
    if errs:
        raise Refused(f"{len(errs)} problem(s)")
    print("ok")


def cmd_get(a, model, gh):
    i = issue(model, a.n)
    kind = "slice" if i.is_slice else ("task of #%d" % i.parent if i.parent else "unplanned task")
    print(f"#{i.number} {i.title}\n{kind}, {i.status}")
    for d in i.deps:
        print(f"depends on #{d} ({model[d].status if d in model else '?'}) {model[d].title if d in model else ''}")
    for o in sorted(n for n, x in model.items() if i.number in x.deps):
        print(f"needed by #{o} ({model[o].status}) {model[o].title}")
    for t in tasks_of(model, i.number) if i.is_slice else []:
        print(f"  #{t.number}  {t.status:<8}{t.title}")
    print("\n" + i.body.strip() + "\n\n(comments: gh issue view %d --comments)" % i.number)


def cmd_ready(a, model, gh):
    task(model, a.n)
    print(ready_report(model, a.n))


def cmd_slice_create(a, model, gh):
    if not a.title.strip() or not a.goal.strip():
        raise Refused("slice.create: --title and --goal are required")
    if not a.demo.strip():
        raise Refused("slice.create: --demo is required: what can be run at the end that could not be run before")
    body = slice_body(a.goal, a.demo, a.implements, a.out_of_scope, a.open)
    print(gh.create(a.title, body, [SLICE]))


def cmd_slice_sync(a, model, gh):
    s = issue(model, a.n)
    if not s.is_slice:
        raise Refused(f"#{a.n} is not a slice")
    body = synced_body(s.body, tasks_section(model, a.n))
    free = ", ".join(f"#{n}" for n in frontier(model, a.n)) or "none"
    if body == s.body:
        print(f"#{a.n} already in sync; startable now: {free}")
        return
    gh.edit(a.n, body=body)
    print(f"synced #{a.n}: {len(tasks_of(model, a.n))} tasks; startable now: {free}")


def cmd_task_create(a, model, gh):
    if not a.title.strip() or not a.what.strip():
        raise Refused("task.create: --title and --what are required")
    if not a.criterion:
        raise Refused("task.create: name at least one --criterion someone else can check")
    if a.slice is not None:
        s = issue(model, a.slice)
        if not s.is_slice or s.closed:
            raise Refused(f"#{a.slice} is not an open slice")
    for d in a.depends_on:
        if task(model, d).status == "dropped":
            raise Refused(f"#{d} was dropped; nothing can depend on it")
    n = gh.create(a.title, task_body(a.what, a.criterion, a.note or ""), [] if a.slice else [UNPLANNED])
    if a.slice is not None:
        gh.attach(a.slice, n)
    for d in a.depends_on:
        gh.add_dep(n, d)
    print(n)


def cmd_update(a, model, gh):
    i = issue(model, a.n)
    body = read_body(a)
    if a.title is None and body is None and a.status is None and a.note is None:
        raise Refused("issue.update: nothing to change; name --title, --body, --status or --note")
    if a.status is not None:
        allowed = SLICE_STATUSES if i.is_slice else TASK_STATUSES
        if a.status not in allowed:
            hint = "; a slice's other states come from its tasks" if i.is_slice else ""
            raise Refused(f"#{a.n}: status '{a.status}' is outside the vocabulary: {' | '.join(allowed)}{hint}")
        _check_status(model, i, a.status, a.note)
    if a.title is not None or body is not None:
        gh.edit(a.n, title=a.title, body=body)
    if a.status is None:
        if a.note:
            gh.comment(a.n, a.note)
        return
    if a.status in ("done", "dropped"):
        gh.close(a.n, "completed" if a.status == "done" else "not planned", a.note or "")
        return
    if i.closed:
        gh.reopen(a.n)
    if a.status == "todo" and (READY in i.labels or i.assignees):
        gh.edit(a.n, remove_label=READY if READY in i.labels else None, assign=False if i.assignees else None)
    elif a.status == "ready" and READY not in i.labels:
        gh.edit(a.n, add_label=READY)
    elif a.status == "doing":
        gh.edit(a.n, assign=True)
    if a.note:
        gh.comment(a.n, a.note)


def _check_status(model: Model, i: Issue, status: str, note: Optional[str]):
    n = i.number
    if status == "dropped" and not note:
        raise Refused(f"#{n}: dropping needs --note saying why")
    if i.is_slice:
        left = [f"#{t.number}" for t in tasks_of(model, n) if not t.closed]
        if left:
            raise Refused(f"#{n}: the slice still has open tasks: {', '.join(left)}")
        return
    if status == "doing":
        if READY not in i.labels:
            raise Refused(f"#{n} is not ready: refine it first (refine-task), then --status ready")
        if unmet(model, i):
            raise Refused(f"#{n} is blocked: waits on {waits_on(unmet(model, i))}")
    if status == "done":
        open_boxes = [text for checked, text in criteria_of(i.body) if not checked]
        if open_boxes:
            raise Refused(f"#{n} has unchecked criteria; tick them in the body or change them: {'; '.join(open_boxes)}")


def cmd_link(a, model, gh):
    adding = a.cmd == "issue.link.add"
    i, other = issue(model, a.n), issue(model, a.other)
    if a.n == a.other:
        raise Refused(f"#{a.n} cannot link to itself")
    if a.relation == "depends-on":
        if i.is_slice or other.is_slice:
            raise Refused("dependencies are between tasks, not slices")
        if adding:
            if a.other in i.deps:
                raise Refused(f"#{a.n} already depends on #{a.other}")
            if reaches(model, a.other, a.n):
                raise Refused(f"#{a.other} already depends on #{a.n}, directly or through others: that would be a cycle")
            gh.add_dep(a.n, a.other)
        else:
            if a.other not in i.deps:
                raise Refused(f"#{a.n} does not depend on #{a.other}")
            gh.remove_dep(a.n, a.other)
        return
    line = link_line(a.relation, a.other)
    lines = i.body.rstrip("\n").split("\n")
    if adding:
        if line in lines:
            raise Refused(f"#{a.n} already says '{line}'")
        gh.edit(a.n, body=i.body.rstrip("\n") + f"\n\n{line}\n")
    else:
        if line not in lines:
            raise Refused(f"#{a.n} does not say '{line}'")
        kept = "\n".join(l for l in lines if l != line)
        gh.edit(a.n, body=re.sub(r"\n{3,}", "\n\n", kept).rstrip("\n") + "\n")


def cmd_delete(a, model, gh):
    i = issue(model, a.n)
    if not a.yes:
        raise Refused("issue.delete: pass --yes. Deleting cannot be undone; --status dropped keeps the record")
    if i.is_slice and tasks_of(model, a.n):
        raise Refused(f"#{a.n} has tasks; drop or delete them first")
    needed = sorted(n for n, x in model.items() if a.n in x.deps and not x.closed)
    if needed:
        raise Refused(f"#{a.n} is a dependency of {waits_on(needed)}")
    gh.delete(a.n)


def parser() -> argparse.ArgumentParser:
    ap = argparse.ArgumentParser(prog="tracker.py", description=__doc__,
                                 formatter_class=argparse.RawDescriptionHelpFormatter)
    sub = ap.add_subparsers(dest="cmd", required=True, metavar="method")

    b = sub.add_parser("board", help="per slice: what can start, what is in progress, what is blocked")
    b.add_argument("--done", action="store_true", help="list closed tasks instead of counting them")
    b.add_argument("--no-colour", action="store_true")
    sub.add_parser("validate", help="check the model; exits non-zero on any problem")
    for name, h in (("issue.get", "an issue, its dependencies and its tasks"),
                    ("ready", "what stands between a task and ready")):
        p = sub.add_parser(name, help=h)
        p.add_argument("n", type=int)

    s = sub.add_parser("slice.create", help="a slice: one outcome you can demo; prints its number")
    s.add_argument("--title", required=True)
    s.add_argument("--goal", required=True, help="why this slice exists, what it de-risks")
    s.add_argument("--demo", required=True, help="a command and its observable result")
    s.add_argument("--implements", action="append", default=[], help="an ADR, spec or CONTEXT term it builds")
    s.add_argument("--out-of-scope", action="append", default=[])
    s.add_argument("--open", action="append", default=[], help="a decision it must make, and where that is recorded")
    p = sub.add_parser("slice.sync", help="rewrite a slice's Tasks section: task list, startable now, graph")
    p.add_argument("n", type=int)

    t = sub.add_parser("task.create", help="a task: one mergeable pull request; prints its number")
    t.add_argument("--title", required=True)
    t.add_argument("--what", required=True)
    t.add_argument("--criterion", action="append", default=[], help="one checkable acceptance criterion")
    t.add_argument("--slice", type=int, help="the slice it belongs to; without it the task is unplanned")
    t.add_argument("--depends-on", type=int, action="append", default=[])
    t.add_argument("--note")

    u = sub.add_parser("issue.update", help="change a title, body or status, or leave a note")
    u.add_argument("n", type=int)
    u.add_argument("--title")
    g = u.add_mutually_exclusive_group()
    g.add_argument("--body")
    g.add_argument("--body-file")
    u.add_argument("--status", help="task: todo|ready|doing|done|dropped; slice: done|dropped")
    u.add_argument("--note", help="a comment; required when dropping")

    for name in ("issue.link.add", "issue.link.remove"):
        p = sub.add_parser(name, help="depends-on | relates-to | discovered-during")
        p.add_argument("n", type=int)
        p.add_argument("relation", choices=RELATIONS)
        p.add_argument("other", type=int)

    d = sub.add_parser("issue.delete", help="delete an issue; prefer --status dropped")
    d.add_argument("n", type=int)
    d.add_argument("--yes", action="store_true")
    return ap


COMMANDS = {
    "board": cmd_board, "validate": cmd_validate, "issue.get": cmd_get, "ready": cmd_ready,
    "slice.create": cmd_slice_create, "slice.sync": cmd_slice_sync, "task.create": cmd_task_create,
    "issue.update": cmd_update, "issue.link.add": cmd_link, "issue.link.remove": cmd_link,
    "issue.delete": cmd_delete,
}


def main(argv: Optional[List[str]] = None, gh=None) -> int:
    a = parser().parse_args(argv)
    gh = gh or Gh(dry=bool(os.environ.get("TRACKER_DRY_RUN")))
    try:
        COMMANDS[a.cmd](a, gh.load(), gh)
    except Refused as e:
        sys.stdout.flush()
        print(f"tracker: {e}", file=sys.stderr)
        return 1
    except subprocess.CalledProcessError as e:
        print(f"tracker: gh failed: {(e.stderr or '').strip() or e}", file=sys.stderr)
        return 1
    return 0


if __name__ == "__main__":
    sys.exit(main())
