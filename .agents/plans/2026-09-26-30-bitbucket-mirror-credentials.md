# #30 Agent review mirrors a private Bitbucket repository

Issue: https://github.com/TheLazyLemur/review-assist/issues/30

## Decisions

- `gitrepo.Source` gets `BitbucketToken`. The composition root always sets
  it to `bitbucket.api_token`; gitrepo alone decides it is for Bitbucket.
  Nothing new is stored.
- A Bitbucket repository with no clone here and no token is refused by name,
  rather than left to the user's helpers. compose already refuses Bitbucket
  without a token, so this only fires on a wiring mistake.
- The mirror `Repo` carries the token only when the repository is on
  Bitbucket. The mirror's `git clone --bare` and `Repo.fetch` get it; read
  commands (cat-file, ls-tree, grep, log) never do. A clone at Cwd gets no
  token and keeps its own remote's auth.
- Git gets the token through a per-invocation credential helper scoped to
  `https://bitbucket.org`: `-c credential.https://bitbucket.org.helper= -c
  credential.https://bitbucket.org.helper=!f() { printf
  'username=x-bitbucket-api-token-auth\npassword=%s\n'
  "$REVIEW_ASSIST_GIT_TOKEN"; }; f`, with `REVIEW_ASSIST_GIT_TOKEN` set only on that git process. `-c`
  writes nothing to config, the remote URL stays
  `https://bitbucket.org/<owner>/<name>.git`, and the helper text names the
  variable, so the token is on no command line.
- The scope keeps the token from any host bitbucket.org redirects to: git
  asks helpers again for the new host, and an unscoped helper would answer.
- The inherited helpers for bitbucket.org are cleared by the empty scoped
  helper. Git reads credential config by URL match, and the more specific
  key resets the list built from less specific ones, so this drops the
  user's and system's unscoped helpers too (Apple git ships osxkeychain).
  Without it a user helper could answer first with a stale password, and on
  success git sends the credential to every helper to store, which would
  persist the token. No global `credential.helper=` is needed. GitHub mirrors
  and local clones keep the user's helpers, such as gh's.
- `printf`, not `echo`: macOS `/bin/sh`'s echo reads backslashes as escapes.
  The test token holds a backslash to catch that.
- With a token, `GIT_TRACE_CURL`, `GIT_CURL_VERBOSE` and `GIT_TRACE_REDACT`
  are dropped from git's environment, so a user's debug settings cannot print
  the Authorization header into an error shown in the TUI.
- `GIT_TERMINAL_PROMPT=0` stays on every git command.

## Tests

- A local `git http-backend` behind `httptest` answers 401 until a request
  carries the expected Basic credentials. `url.<server>/.insteadOf
  https://<host>/`, set through `GIT_CONFIG_COUNT`, points the real clone URL
  at it. Credential matching sees the rewritten URL, so the helper's scope is
  a package variable that `export_test.go` points at the test server; nothing
  else in gitrepo changes for tests.
- `GIT_TRACE` to a file records the command line of every git process,
  including the helper and `git-remote-http`, for the argv check.
- The GitHub test gives the user a helper for the GitHub server and checks
  the mirror still clones, which fails if gitrepo cleared or replaced it.
- Mutations each caught by one test: no scoped reset (a stale password in
  the user's `store` helper), the token given to a local clone (the clone's
  fetch with the user's helper), the helper written to the mirror's config
  (no `credential` in it), and an unscoped helper (a redirect to another host
  that records the passwords it is offered).
