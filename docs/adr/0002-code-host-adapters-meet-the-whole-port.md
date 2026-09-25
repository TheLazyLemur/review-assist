# Code host adapters meet the whole port

Every code host adapter meets the whole `pr.CodeHost` contract, so the core
and the TUI never ask which platform they are talking to. Where one platform
call is not enough, the adapter builds the result from several of that
platform's own calls. Where the platform cannot do a thing at all, the adapter
refuses it by name and sends nothing. It never returns a zero or a guess in
place of a real value: a wrong number on screen looks like a fact. The port
stays the same for every platform, and the cost of the extra calls stays in
the adapter that needs them.

Seen on Bitbucket, where the API is thinner than GitHub's:

- A verdict with a message is a comment, then the verdict, because Bitbucket
  takes no message with a verdict (#17).
- Reopen is refused, because Bitbucket cannot reopen a declined pull request
  (#18).
- The line and file counts come from each pull request's diffstat, because
  the pull request itself has none (#27). This costs one request per pull
  request in a list.

## Considered Options

- **Let the port say "unknown"** and have the TUI hide what a platform lacks.
  Rejected: every caller then handles every platform's gaps, and the gaps
  spread out of the adapter into the core and the TUI.
- **Leave the field at its zero value.** Rejected: that is how "+0 -0 in 0
  files" reached the screen for every Bitbucket pull request (#27).
