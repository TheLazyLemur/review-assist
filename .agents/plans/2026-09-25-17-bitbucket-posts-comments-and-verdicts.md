# #17 Bitbucket Cloud code host posts comments and verdicts

## Goal

`internal/adapters/bitbucket.Client` gains the four posting methods of
`pr.CodeHost`: `SubmitVerdict`, `PostComment`, `Reply`, `DeleteComment`, with
the same signatures as the GitHub code host. Nothing wires the client in yet
(#20), so the methods are exported and tested against an `httptest` server.
Merging and the other pull request actions are #18, not this task.

## Bitbucket Cloud API (2.0)

Paths under `/repositories/{workspace}/{slug}/pullrequests/{id}`:

- `POST /comments` with `{"content":{"raw":"..."}}`. A comment on a file adds
  `"inline":{"path":"..."}`. A head-side line adds `"to":<line>`, a base-side
  line `"from":<line>`. A reply adds `"parent":{"id":<id>}`.
- `DELETE /comments/{comment_id}`
- `POST /approve`
- `POST /request-changes`

Neither verdict endpoint takes a message. A verdict with a message posts the
message as a comment on the pull request first, then sends the verdict. If the
verdict then fails, the error says the comment was posted, so the user does
not post it again.

## Rules

- A range anchor (`Anchor.StartLine != 0`) returns an error that names ranges
  and sends no request.
- Reply posts `parent.id` = `parent.ID`. Never `ReplyTo`: on Bitbucket that is
  the direct parent, not the thread root as on GitHub.
- A comment on the pull request itself can be replied to (Bitbucket threads
  them), unlike GitHub.
- An unknown decision is an error that sends no request.
- `do` is GET only today. Generalise it to take a method and a body; keep its
  error format (reason first, then `(<status> <METHOD> <url>)`) and basic auth.
- `HeadSHA` is not sent. Known gap, recorded on #17; do not work around it.

## Tests

`bitbucket_test.go`, package `bitbucket_test`, beside the existing tests and
reusing `serve`, `repo`, `prPath`. One test per criterion on #17:

1. comment on the pull request: body has no `inline`
2. comment on a file: `inline` has the path and no `from`/`to`
3. head-side line: `inline.to` is the line
4. base-side line: `inline.from` is the line
5. range: error names ranges, server sees no request
6. reply to a reply: `parent.id` is the comment's `ID`, not `ReplyTo`
7. delete: DELETE to `/comments/{ID}`
8. approve with no message: only the approve request
9. request changes: the request-changes request
10. verdict with a message: comment first, then the verdict
11. verdict fails after the comment: the error says the comment was posted

Decode request bodies into maps or small structs and compare; do not compare
raw JSON strings.
