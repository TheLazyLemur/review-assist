---
name: tracker
description: Use when planning, picking up, or reporting on work in this repo - slices and tasks in its GitHub Issues, their statuses and dependencies, what can start now, and the dispatcher that reads and writes them. For writing a slice use to-slice, for breaking one into tasks use to-tasks, for taking a task to ready use refine-task.
---

# Tracker

Work that has not happened yet lives in this repo's GitHub Issues, as slices
and tasks. It is the only place work in flight is recorded. A finding with no
issue is a finding that is lost.

The terms (slice, demo, task, unplanned, depends on, ready, acceptance
criterion) are defined in `CONTEXT.md`. This skill is how they map onto GitHub
and how to read and write them.

| Model | On GitHub |
|---|---|
| slice | an issue labelled `slice` |
| task | a sub-issue of its slice |
| unplanned task | an issue labelled `unplanned`, with no parent |
| depends on | a native issue dependency ("blocked by"), declared from the dependent task |
| related, discovered during | a `Related: #n` or `Discovered during #n` line at the end of the body |

Write slices with `to-slice`, their tasks with `to-tasks`, and take a task to
ready with `refine-task`, rather than by hand.

## The dispatcher

Read and write through `./tracker` at the repo root. It builds the Go program
in `tools/tracker/` (a no-op when nothing changed) and runs it in your current
directory. It runs `gh`, which uses your own `gh auth login`, and loads the
whole repo in one query.

```sh
./board                # what can start, what is in progress, what is blocked
./tracker validate     # exits non-zero on any problem
./tracker --help
```

A blocker can be an issue in another repository. It shows as `owner/repo#n`
and counts as unmet until GitHub reports it closed as completed.

It refuses what GitHub would accept but the model forbids: a pull request
number (issues and pull requests share one number space), a dependency cycle,
starting a task that is not ready or is still blocked, closing a task with
unchecked criteria, closing a slice over open tasks, and dropping anything
without saying why. Set `TRACKER_DRY_RUN=1` to print the writes instead of
running them.

Reach past it with raw `gh` only for a read it does not cover; the commands are
in `docs/agents/issue-tracker.md`.

```
board [--done]                          slice.create --title --goal --demo [--implements]...
validate                                             [--out-of-scope]... [--open]...
issue.get <N>                           slice.sync <SLICE>
ready <TASK>                            task.create --title --what --criterion... [--slice N]
                                                    [--depends-on N]... [--note]
                                        issue.update <N> [--title] [--body|--body-file]
                                                         [--status S] [--note]
                                        issue.link.add|remove <N> <RELATION> <OTHER>
                                        issue.delete <N> --yes
```

`task.create` and `slice.create` print the new number and nothing else, so it
can be captured. After adding tasks or dependencies to a slice, run
`slice.sync`: it rewrites the slice's Tasks section (the task list, what can
start now, and a mermaid graph) from the issues, so it never drifts. `validate`
reports a stale section.

`go test ./tools/...` covers every method and refusal against a fake GitHub,
so it touches no network and no issue. It does not prove the query against
GitHub: after changing the query, run `./board` once against a throwaway repo.

## Status

| Task status | On GitHub | Set with |
|---|---|---|
| todo | open | `--status todo` |
| ready | open, labelled `ready` | `--status ready`, after `refine-task` |
| doing | open, assigned | `--status doing`: refused unless ready and every dependency is done |
| done | closed as completed | `--status done`: refused while a criterion is unchecked |
| dropped | closed as not planned | `--status dropped --note 'why'` |

A slice is open until you close it: `--status done` once every task is closed,
or `--status dropped` with a note. Its progress comes from its tasks.

Blocked is not a status. A task is blocked while a dependency is not done;
`board` shows what each waits on. Ready and blocked are separate questions.

A task's pull request says `Closes #<task>`, so merging it closes the task.
Tick its criteria in the body before it merges, or `validate` reports it.

A task left in progress at the end of a session gets a note saying where it
stopped: `issue.update <N> --note '...'`.

## Granularity

A task lands as one pull request that leaves `main` green and installable. The
code may be inert, such as an adapter nothing wires in yet, as long as it moves
the slice on. Tasks are layers under a slice; the slice is the vertical cut
that gets demoed. Anything smaller than a task is a session todo, not an issue.

Inert code is not free in Go: `unused` flags unexported functions nothing
calls. Export the intermediate layer, or the task does not land green.

## Conventions

- **A title is a claim or a product, not a topic.** A slice or an unplanned
  task names what is wrong or missing: "Own-PR verdicts fail on Bitbucket".
  A planned task names what it produces: "Bitbucket CodeHost reads pull
  requests".
- **The body carries enough to pick the work up cold.** Not how to do it: how is
  decided when the task is taken, in `.agents/specs/` and `.agents/plans/`, one
  file per task named `YYYY-MM-DD-<task number>-<slug>.md`. A constraint that
  outlives the task is a requirement, so it does belong in the body.
- **No file paths or code in slices and tasks**; they go stale. A type shape
  that pins a decision is the exception.
- **Use the domain language** of `CONTEXT.md`.

## Rules

- Anything that survives the session goes in the tracker before it is
  forgotten. Deferred work that must happen goes in an issue, never only in pull
  request prose.
- Work that arrives unplanned is still a task: `task.create` without `--slice`.
  Do not invent a slice to hold it.
- Close a task the moment it is true, not at the end of the turn.
- Write through the dispatcher. Run `validate` before you finish.
- A spec or a plan is never an issue.
