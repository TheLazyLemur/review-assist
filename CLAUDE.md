# review-assist

## Comments

Comment only what the code cannot say: why, an invariant, a trap. Do not
restate what a name or a line already shows. No comment is better than one
that repeats the code.

## Agent skills

### Issue tracker

Work in flight lives in this repo's GitHub Issues as slices and tasks: a slice
is an issue labelled `slice`, a task is a sub-issue of its slice (or labelled
`unplanned`), and depends on is the only order. `./board` shows what can start.
The `tracker` skill owns the model and the dispatcher; the raw `gh` commands
are in `docs/agents/issue-tracker.md`.

Write slices with `to-slice`, their tasks with `to-tasks`, and take a task to
ready with `refine-task`, rather than by hand. Run `./tracker validate` before
finishing work that touched the tracker.

Specs live in `.agents/specs/` and plans in `.agents/plans/`, one file per task,
named `YYYY-MM-DD-<task number>-<slug>.md`. That location overrides the default
of any skill, command or plugin that writes one. The files are committed and
stay after the task lands.

### Domain docs

Single context: `CONTEXT.md` and `docs/adr/` at the repo root.
