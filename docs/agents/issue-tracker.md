# Issue tracker: GitHub

Issues for this repo live as GitHub issues, as slices and tasks. Write through
the dispatcher, `./tracker`, described in the
`tracker` skill. The raw `gh` commands below are for reads it does not cover;
`gh` infers the repo from `git remote -v` when run inside the clone.

## Conventions

- **Read an issue**: `gh issue view <number> --comments`.
- **List issues**: `gh issue list --state open --json number,title,labels,assignees`
  with `--label`, `--assignee` and `--state` filters.
- **Comment on an issue**: `gh issue comment <number> --body "..."`.

Issues and pull requests share one number space: `#42` may be either. Resolve
with `gh issue view 42`, and fall back to `gh pr view 42`.

## Sub-issues and dependencies

A task is a sub-issue of its slice, and depends on is a native issue
dependency. `gh` has no commands for these, so they go through `gh api`, and
both endpoints take the issue's **database id**, not its number:

```sh
id() { gh api "repos/{owner}/{repo}/issues/$1" --jq .id; }
```

- **List a slice's tasks**:
  `gh api repos/{owner}/{repo}/issues/<slice>/sub_issues --jq '.[] | "#\(.number) \(.state) \(.title)"'`
- **Find a task's slice**: `gh api repos/{owner}/{repo}/issues/<task>/parent --jq .number`
- **List what a task depends on**:
  `gh api repos/{owner}/{repo}/issues/<n>/dependencies/blocked_by --jq '.[] | "#\(.number) \(.state)"'`

`gh api` fills in `{owner}` and `{repo}` from the current clone.

Traps, all observed:

- `issue_dependencies_summary` on an issue lags a write. Read the `blocked_by`
  list above, not the summary, straight after declaring a dependency.
- `sub_issues_summary` lags a write too, and it counts a task closed as not
  planned as completed. The dispatcher counts from the issues instead.
- A slice stays open when its last task closes, even at 100%. Close it with
  `./tracker issue.update <slice> --status done`.
- A sub-issue list has an order, and GitHub shows it. It means nothing here:
  depends on is the only order between tasks.

## Pull requests as a triage surface

**PRs as a request surface: no.**

## When a skill says "publish to the issue tracker"

Create a task with `./tracker task.create`, or a slice with `slice.create`.

## When a skill says "fetch the relevant ticket"

Run `./tracker issue.get <number>`, and `gh issue view <number> --comments`
for the comments.
