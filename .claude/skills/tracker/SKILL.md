---
name: tracker
description: Use when planning, picking up, or reporting on work in this repo - its GitHub Issues, how a feature splits into one task per pull request, and the conventions for titles, bodies, status, labels and links.
---

# Task tracker

Work that has not happened yet lives in this repo's GitHub Issues. It is the
only place work-in-flight is recorded. A finding with no issue is a finding
that is lost.

Read and write it through one dispatcher, `.claude/skills/tracker/scripts/tracker`.
It runs `gh`, which uses your own `gh auth login`.

```sh
tracker=.claude/skills/tracker/scripts/tracker   # path is from the repo root
$tracker board.list
```

Methods are `<noun>.<verb>`; `$tracker --help` prints the surface. Every write
refuses a pull request number (issues and pull requests share one number
space), a label or status outside the vocabularies below, dropping an issue
without saying why, and closing a feature that still has open tasks. Set
`TRACKER_DRY_RUN=1` to print the `gh` commands instead of running them.

Reach past it with raw `gh` only for something the surface does not cover; the
commands, including sub-issues and dependencies through `gh api`, are in
`docs/agents/issue-tracker.md`. Hand-written sub-issue and dependency calls are
where the database-id-versus-number mix-up and the lagging summaries come from;
removing those is why the dispatcher exists.

## Granularity

A **feature** is an issue. A **task** is one mergeable pull request, and it is a
sub-issue of its feature.

A task is the right size when `main` is green and installable once it lands.
The code may be inert, such as an adapter nothing wires in yet, as long as it
moves the feature on. Anything smaller is a sub-task: implementation detail for
the session's todo list, not an issue. It dies with the session, deliberately.

Inert code is not free in Go: `unused` flags unexported functions nothing
calls. Export the intermediate layer, or the task does not land green.

The sub-issue order is the task order, and it usually carries dependency: a
TUI change cannot precede the port it calls. Tasks that are genuinely
independent may be taken in any order.

```
#20  Bitbucket pull requests cannot be reviewed
  #21  Bitbucket CodeHost reads pull requests, diffs and comments   inert, not wired
  #22  Bitbucket CodeHost posts comments and verdicts               inert, not wired
  #23  Repositories on bitbucket.org open with the Bitbucket adapter  feature goes live
```

Not issues: "add the Bitbucket section to config.example.json", "write the DTO
structs", "test an empty comment list". Those are session todos.

If you cannot write the pull request title from the task title, the task is
too small or too vague.

## Conventions

- **A title is a claim, not a topic.** "Own-PR verdicts fail on Bitbucket", not
  "Bitbucket verdicts".
- **The body carries enough to pick the issue up cold**: what breaks, where, and
  how you know. Not how to fix it.
- **The tracker holds what, why, and where it stopped.** How is decided when the
  task is taken. It goes in `.agents/specs/` and `.agents/plans/`, one file per
  task, named `YYYY-MM-DD-<task number>-<slug>.md`, then in the code and its
  pull request. Never in the issue body. A constraint that outlives the task,
  such as "reuse the gh runner, do not add a second", is a requirement, so it
  does belong in the body.
- **A task's pull request says `Closes #<task>`.** Merging it closes the task.
  Close the feature by hand when its last task lands: GitHub leaves it open.
- **Use the domain language.** Titles and bodies use the terms in `CONTEXT.md`.

### Status

| State | On GitHub |
|---|---|
| open | open, no assignee |
| in progress | open, assigned (`--add-assignee @me` is the session's first write) |
| blocked | open, with an open blocking dependency |
| done | closed as completed |
| dropped | closed as not planned, with a comment saying why |

A task left in progress at the end of a session gets a comment saying where it
stopped. A pull-request-sized task is opaque without one.

### Labels

- `needs-triage`: not yet checked, sized or split.
- `ready-for-agent`: specified well enough for an agent to take it cold.

Add labels sparingly. GitHub already shows a task's open pull request, so no
label says one is open.

### Links

- **Blocks** is a native dependency, declared from the blocked side. One edge
  per dependency: two statements of one fact can disagree.
- **Related** is a `Related: #n` line at the end of the body.
- **Discovered during** is a `Discovered during #n` line at the end of the body,
  for an issue found while working on another.

## The surface

```
board.list [--status S] [--label L]     issue.create --title T --body|--body-file ...
issue.get <N>                           issue.update <N> --title|--body|--status|--note
task.list <FEATURE>                     issue.delete <N> --yes
                                        issue.label.add|remove  <N> <LABEL>
                                        issue.link.add|remove   <N> <RELATION> <OTHER>
                                        task.append <FEATURE> <TITLE>
                                        task.move   <FEATURE> <TASK> --before|--after <OTHER>
```

Open a feature with its tasks. `issue.create` prints the feature number on
stdout and nothing else, so it can be captured:

```sh
n=$($tracker issue.create \
  --title 'Title as a claim' --body-file body.md --label needs-triage \
  --task 'Bitbucket CodeHost reads pull requests' --task 'Bitbucket CodeHost posts comments')
```

Take a task, say where it stopped, and close it:

```sh
$tracker issue.update 21 --status in-progress
$tracker issue.update 21 --note 'Reads work; posting next.'
$tracker issue.update 21 --status done
```

A task's pull request says `Closes #21`, so merging it closes the task anyway.
Close the feature when its last task lands; the dispatcher refuses while any
task is open:

```sh
$tracker issue.update 20 --status done --note 'All tasks landed.'
```

Declare a blocker from the blocked side, and reorder tasks:

```sh
$tracker issue.link.add 23 blocked-by 22
$tracker task.move 20 23 --before 21
```

`scripts/tracker-test` covers the surface, every refusal and the quoting against
`TRACKER_DRY_RUN=1`, so it touches no network and no issue. Run it after
editing the dispatcher. It does not prove the queries against GitHub: after a
change to a query, run the method once against a throwaway repo.

## Rules

- Anything that survives the session goes in the tracker before it is
  forgotten. Deferred work that must happen goes in an issue, never only in pull
  request prose.
- Close a task the moment it is true, not at the end of the turn.
- Do not invent a status or a label outside the vocabularies above.
- Write through the dispatcher. Raw `gh` is for reads the surface lacks.
- A spec or a plan is never an issue.
