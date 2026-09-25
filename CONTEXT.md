# review-assist

A terminal tool for reviewing pull requests on a code host, with AI agents that
suggest where a comment could go. Agents only suggest; the user posts.

## Language

### Pull requests

**Code host**:
The service that holds a repository and its pull requests, such as GitHub,
GitHub Enterprise or Bitbucket.
_Avoid_: Host, forge, provider

**Platform**:
The kind of code host: GitHub or Bitbucket. GitHub Enterprise is the GitHub
platform on another hostname.
_Avoid_: Provider, type, vendor

**Hostname**:
The server name of a code host, such as `github.com` or `ghe.example.com`.
_Avoid_: Host, domain

**Repository**:
A repository on a code host, named by its hostname, owner and name.
_Avoid_: Project, repo slug

**Remote**:
The one git remote of a clone that review-assist takes the repository from.
It is the first of these that points at a code host: the remote the current
branch tracks, then `origin`, then the only remote on a code host. If none
qualifies, review-assist refuses to start. Other remotes are ignored.
_Avoid_: Origin, upstream

**Owner**:
The account or organisation that holds a repository. On Bitbucket this is the
workspace.
_Avoid_: Workspace, org, namespace

**Verdict**:
A reviewer's decision on a pull request: approve or changes requested. A
verdict may carry a message.
_Avoid_: Review, review event, status

**Comment**:
Text a user posts on a pull request. A comment with an anchor is about code;
a comment without one is about the whole pull request.
_Avoid_: Issue comment, review comment, inline comment, note

**Anchor**:
The place a comment points to: a file, and optionally a line on one side of
the diff.
_Avoid_: Position, location, inline

**Side**:
The version of a file a line belongs to: base (before the change) or head
(after it).
_Avoid_: Left, right, from, to, old, new

**Own pull request**:
A pull request the user opened. On GitHub a verdict on it posts as a comment
headed with the verdict, because GitHub does not accept a verdict from the
author. Bitbucket accepts one, so there the verdict is sent.

### Agent review

**Agent review**:
A read-only analysis of a pull request by agents, which returns findings.
_Avoid_: Inspection, audit, AI review

**Subject**:
What an agent review looks at: the diff of a pull request and the repository
at its head and base commits.
_Avoid_: Workspace, context, material

**Finding**:
A suggestion from an agent review: where a comment could go, why, and a draft
of the comment.
_Avoid_: Issue, result, suggestion

**Assessment**:
The verifier's short statement on whether a pull request is ready to merge,
and what blocks it if not.
_Avoid_: Verdict, summary, conclusion

**Agent**:
One model-driven worker in an agent review. An agent is a specialist or the
verifier.
_Avoid_: Bot, worker, backend

**Specialist**:
An agent that works on one assignment and reports findings.
_Avoid_: Reviewer, sub-agent

**Verifier**:
The agent that removes duplicate findings, re-checks each one against the
code, ranks them and writes the assessment.
_Avoid_: Judge, checker, reviewer

**Assignment**:
A lens over a scope: the work one specialist owns.
_Avoid_: Unit, job, task

**Lens**:
A checklist of one kind of problem to look for, such as correctness or
security.
_Avoid_: Category, focus, perspective

**Scope**:
The changed files an assignment covers.
_Avoid_: Files, slice, partition

**Level**:
How many agents an agent review runs, from quick to max.
_Avoid_: Depth, mode, effort

**Effort**:
How hard each model reasons. Effort is separate from level: level is how many
agents run, effort is how hard each one thinks.
_Avoid_: Thinking, reasoning, level

**Tool**:
A read-only operation an agent may call to explore the subject.
_Avoid_: Function, command, capability

**Finish tool**:
The one tool that ends an agent's work and records its findings.
_Avoid_: Submit, done, return

### Backends

**Backend**:
What runs agents. A backend may offer an agent only the tools of its task.
_Avoid_: Agent, model, provider, runner

**Task**:
What a backend runs for one agent: instructions, tools and a turn limit.
_Avoid_: Job, request, assignment

**Turn**:
One reply from the model to an agent.
_Avoid_: Step, iteration, round

**API backend**:
A backend reached over a model API, where review-assist runs the agent loop.
The Messages API backend is one; a Chat Completions backend would be another.
_Avoid_: Endpoint, provider, Anthropic backend

**CLI backend**:
A backend that is a command-line agent, such as Claude Code, which runs its
own agent loop. A CLI agent qualifies only if its built-in tools can be turned
off, so the task's tools are all it can call.
_Avoid_: Agent CLI, harness, wrapper

**Credential**:
A secret that proves who the user is to a backend or a code host, such as an
API key or a token.
_Avoid_: Key, token, secret, auth

### Planning

**Slice**:
One outcome you can demo end to end. The unit of planned work, and the only
grouping above a task.
_Avoid_: Feature, epic, milestone, story

**Demo**:
What can be run at the end of a slice that could not be run before: a command
and its observable result.
_Avoid_: Goal, outcome, acceptance

**Task**:
One unit of work that lands as one pull request, with its own status. It
usually belongs to a slice.
_Avoid_: Ticket, issue, card, sub-task

**Unplanned**:
A task that arrived rather than being planned into a slice. How many there are
says whether the plan is wrong.
_Avoid_: Bug, interrupt, ad hoc

**Depends on**:
The tasks that must be done before another can start. It is the only order
between tasks, because the work forks.
_Avoid_: Blocked by, order, priority, sequence

**Ready**:
A task whose acceptance criteria someone other than its author can check,
under a slice whose demo is concrete. It is about the writing, not the graph: a
ready task may still wait on a dependency.
_Avoid_: Groomed, refined, triaged, unblocked

**Acceptance criterion**:
One observable thing that must be true for a task to be done. If you cannot say
what you would run or read to decide, it is not one yet.
_Avoid_: Requirement, definition of done, done-when
