# #20 Repositories on bitbucket.org open with the Bitbucket code host

Issue: https://github.com/TheLazyLemur/review-assist/issues/20

## Decisions

- `newCodeHost` in `cmd/review-assist` picks the code host by
  `repo.Platform`: GitHub gets the `gh` client, Bitbucket the REST client on
  `https://api.bitbucket.org/2.0`. Any other platform is an error. The
  "not supported yet" refusal is gone.
- Bitbucket needs `bitbucket.email` (the Atlassian account email) and
  `bitbucket.api_token`, from the config file or
  `REVIEW_ASSIST_BITBUCKET_EMAIL` / `REVIEW_ASSIST_BITBUCKET_API_TOKEN`. No
  flag, as with the Claude token. No password key. If either is missing,
  `newCodeHost` refuses with one error naming both keys, both env vars and the
  config file. A GitHub repository never reads them.
- `gitrepo.PickRemote` and `FindRemote` return the picked remote's name as
  well as the repository. `resolveTarget` returns that name, or `""` when the
  cwd is not a clone of the target (a pasted URL of another repository).
- `tui.Deps.LocalRepo` becomes `Remote string`: `""` means no local clone,
  which hides checkout as `LocalRepo=false` did. The header shows
  `remote <name>` after the repository when it is set.
- With no clone, the Bitbucket client gets no Remote or Dir, so Checkout
  refuses by name.
- `openBrowser` runs `open`, `xdg-open` or `rundll32
  url.dll,FileProtocolHandler` with no stdout or stderr, so it cannot draw
  over the TUI.
- `--init`'s example config gets a `bitbucket` section with empty values; the
  existing every-key test covers it.
- Token scopes come from Atlassian's OpenAPI spec
  (`developer.atlassian.com/cloud/bitbucket/swagger.v3.json`,
  `x-atlassian-oauth2-scopes`) for each endpoint the client calls:
  `read:user:bitbucket`, `read:pullrequest:bitbucket`,
  `write:pullrequest:bitbucket`, and `read:repository:bitbucket`, because the
  pull request diff redirects to the repository diff endpoint.
