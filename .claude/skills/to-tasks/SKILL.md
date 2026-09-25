---
name: to-tasks
description: "Break a slice into tasks in this repo's GitHub Issues, each declaring what it depends on, so the work is a dependency graph rather than an ordered list. Use whenever the user wants a slice broken down, decomposed, or turned into tickets, asks what the tasks are or what can be started in parallel, or points at a slice whose Tasks section is empty. Presents the breakdown and the graph for approval before creating any issue. Not for writing the slice itself (that is to-slice)."
---

# To Tasks

Break a slice into tasks, one sub-issue each.

The terms are in `CONTEXT.md` and the tracker commands are in the `tracker`
skill. This skill is the part that is not written down there: how to cut the
work, and the approval step.

## Create only what you were asked for

Create the tasks and sync the slice. Do not edit other slices or tasks. Say what
else you think should change and let the user decide.

## Tasks are layers; the slice is the vertical cut

The slice is what gets demoed, so the tasks under it are horizontal by
necessity: a port, an adapter, a TUI change. Do not try to make each task
demoable on its own; you will either fail or produce one enormous task.

This only works because the slice above is already a thin vertical cut. If a
task turns out to be demoable on its own, it should probably have been its own
slice.

Each task still lands as one pull request that leaves `main` green. An adapter
nothing wires in yet is fine; a half-written one that breaks the build is not.

**Wide refactors are the exception.** A mechanical change that fans across the
codebase, such as renaming a domain type, cannot land in one green step.
Sequence it expand then contract: one task adds the new form beside the old, a
task per batch moves callers, and a final task deletes the old form once no
caller remains, depending on every batch.

## Process

1. **Read the slice in full**, and everything it lists under Implements (ADRs,
   specs, `CONTEXT.md` terms). Read the code the slice touches. Work from those,
   not from memory. If the slice already carries a draft task list, check it
   rather than taking it on trust.

   ```sh
   .claude/skills/tracker/scripts/tracker.py issue.get 20
   ```

2. **Draft the tasks.** Each is one unit of work, finishable without holding the
   whole slice in your head, named for what it produces, with a **What** and a
   **Done when** a reader can check without asking you. Apply the checkable
   test from `refine-task` as you write.

   Give each its dependencies: what must finish first, and nothing more. Be
   strict: a dependency added out of caution removes a branch someone could
   have worked in parallel.

   Include the slice's **Open in this slice** decisions as tasks. A decision is
   work. Its Done when names where the answer gets recorded.

3. **Present for approval before creating anything.** The list with each task's
   title, what it depends on and what it produces, then the graph as mermaid so
   the parallel branches are visible. Name any edge you are unsure of. Then ask:
   is the granularity right, is every edge real, should any be merged or split?

   This step is the main reason to use this skill rather than doing it freehand.
   Twelve issues appearing unannounced are hard to review; a list and a diagram
   are not. Wait for the answer.

4. **Create the tasks** with the dispatcher, in dependency order so each
   `--depends-on` names a task that already exists. Capture each number:

   ```sh
   t=.claude/skills/tracker/scripts/tracker.py
   a=$($t task.create --slice 20 --title "Bitbucket CodeHost reads pull requests" \
     --what "..." --criterion "..." --criterion "...")
   b=$($t task.create --slice 20 --title "Bitbucket CodeHost posts comments" \
     --what "..." --criterion "..." --depends-on "$a")
   ```

   Avoid file paths and code snippets in tasks; they go stale. A type shape
   that pins a decision is the exception.

5. **Sync the slice and check the graph.** Do not write the diagram by hand; it
   drifts the moment an edge changes.

   ```sh
   $t slice.sync 20
   $t validate
   ```

   `slice.sync` rewrites the slice's Tasks section with the task list, what can
   start now, and a mermaid graph generated from the dependencies. `validate`
   catches cycles, tasks outside any slice that are not unplanned, and a stale
   Tasks section. Fix anything it reports before you finish.

   A cycle is refused when you add the edge, and is the one error worth the
   check on its own: each task in a circular chain reads sensibly, so nobody
   catches it by eye.

6. **Report what can start now**, as `slice.sync` printed it.

## Ad hoc work

Work that arrives rather than being planned is still a task: `task.create`
without `--slice` labels it unplanned. Do not invent a slice to hold it.
