# review-assist

## Comments

Comment only what the code cannot say: why, an invariant, a trap. Do not
restate what a name or a line already shows. No comment is better than one
that repeats the code.

## Agent skills

### Issue tracker

Work in flight lives in this repo's GitHub Issues: a feature is an issue, and
each task is one pull request, as a sub-issue of its feature. The `tracker`
skill owns the conventions; the `gh` commands are in
`docs/agents/issue-tracker.md`.

Specs live in `.agents/specs/` and plans in `.agents/plans/`, one file per task,
named `YYYY-MM-DD-<task number>-<slug>.md`. That location overrides the default
of any skill, command or plugin that writes one. The files are committed and
stay after the task lands.

### Domain docs

Single context: `CONTEXT.md` and `docs/adr/` at the repo root.
