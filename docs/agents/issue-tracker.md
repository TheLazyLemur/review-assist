# Issue tracker: GitHub

Issues for this repo live as GitHub issues. Use the `gh` CLI for all operations;
it infers the repo from `git remote -v` when run inside the clone.

## Conventions

- **Create an issue**: `gh issue create --title "..." --body-file body.md --label ...`.
  It prints the issue URL; the number is its last path segment.
- **Read an issue**: `gh issue view <number> --comments`.
- **List issues**: `gh issue list --state open --json number,title,labels,assignees`
  with `--label`, `--assignee` and `--state` filters.
- **Comment on an issue**: `gh issue comment <number> --body "..."`.
- **Apply / remove labels**: `gh issue edit <number> --add-label "..."` / `--remove-label "..."`.
- **Claim**: `gh issue edit <number> --add-assignee @me`.
- **Close as done**: `gh issue close <number> --reason completed --comment "..."`.
- **Close as dropped**: `gh issue close <number> --reason "not planned" --comment "..."`.

Issues and pull requests share one number space: `#42` may be either. Resolve
with `gh issue view 42`, and fall back to `gh pr view 42`.

## Sub-issues and dependencies

`gh` has no commands for these, so they go through `gh api`. Both endpoints take
the issue's **database id**, not its number:

```sh
id() { gh api "repos/{owner}/{repo}/issues/$1" --jq .id; }
```

- **Add a task to a feature**:
  `gh api --method POST repos/{owner}/{repo}/issues/<feature>/sub_issues -F sub_issue_id=$(id <task>)`
- **List a feature's tasks, in order**:
  `gh api repos/{owner}/{repo}/issues/<feature>/sub_issues --jq '.[] | "#\(.number) \(.state) \(.title)"'`
- **Move a task before another**:
  `gh api --method PATCH repos/{owner}/{repo}/issues/<feature>/sub_issues/priority -F sub_issue_id=$(id <task>) -F before_id=$(id <other>)`
  (`after_id` moves it after).
- **Find a task's feature**: `gh api repos/{owner}/{repo}/issues/<task>/parent --jq .number`
- **Declare a blocker** (from the blocked side):
  `gh api --method POST repos/{owner}/{repo}/issues/<blocked>/dependencies/blocked_by -F issue_id=$(id <blocker>)`
- **List what blocks an issue**:
  `gh api repos/{owner}/{repo}/issues/<n>/dependencies/blocked_by --jq '.[] | "#\(.number) \(.state)"'`

`gh api` fills in `{owner}` and `{repo}` from the current clone.

Traps, all observed:

- `issue_dependencies_summary` on an issue lags a write. Read the `blocked_by`
  list above, not the summary, straight after declaring a blocker. Its
  `blocked_by` counts open blockers only; `total_blocked_by` counts all.
- `sub_issues_summary` counts a task closed as not planned as completed, so a
  feature's progress percentage includes dropped tasks.
- A feature stays open when its last task closes, even at 100%. Close it by hand.

## Pull requests as a triage surface

**PRs as a request surface: no.**

## When a skill says "publish to the issue tracker"

Create a GitHub issue.

## When a skill says "fetch the relevant ticket"

Run `gh issue view <number> --comments`.
