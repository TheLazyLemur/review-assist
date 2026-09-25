---
name: tracker
description: Use when planning, picking up, or reporting on work in this repo - its GitHub Issues, how a feature splits into one task per pull request, and the conventions for titles, bodies, status, labels and links.
---

# Task tracker

Work that has not happened yet lives in this repo's GitHub Issues. It is the
only place work-in-flight is recorded. A finding with no issue is a finding
that is lost.

The `gh` commands, including sub-issues and dependencies, are in
`docs/agents/issue-tracker.md`. This skill is the conventions.

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

## Rules

- Anything that survives the session goes in the tracker before it is
  forgotten. Deferred work that must happen goes in an issue, never only in pull
  request prose.
- Close a task the moment it is true, not at the end of the turn.
- Do not invent a status or a label outside the vocabularies above.
- A spec or a plan is never an issue.
