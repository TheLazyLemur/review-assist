# #16 Bitbucket Cloud code host reads pull requests, diffs and comments

Issue: https://github.com/TheLazyLemur/review-assist/issues/16

## Decisions

- Package `internal/adapters/bitbucket`, a `Client` over `net/http`. The
  base URL (`https://api.bitbucket.org/2.0` in production) is a constructor
  argument so tests use an `httptest` server.
- Add `pr.Bitbucket Platform = "bitbucket"` next to `pr.GitHub`.
- `Author` and `Viewer` are the Bitbucket `nickname`. `display_name` is not
  unique; `account_id` is unreadable in the list. `pr.IsOwn` compares the two.
- `pr.Closed` lists `DECLINED` and `SUPERSEDED`; both map to `State` `CLOSED`.
- The client does not assert `pr.CodeHost` yet: posting (#17) and actions
  (#18) are missing. Nothing wires it in (#20).
- Additions, deletions, changed files, labels, checks and `ReviewDecision` are
  not in Bitbucket's pull request JSON and stay zero.
- Added in review:
  - `List` sends `sort=-updated_on` and stops at 100, as the GitHub adapter
    does with `--limit 100`.
  - The client has a 60s timeout: callers pass `context.Background()`, so
    nothing else bounds a stalled response.
  - A `next` link that is not under the base URL is refused: it comes from
    the response body, and every request carries the credentials.
  - Outdated inline comments are left out, as on GitHub: their lines belong to
    an older commit. Pending (unsubmitted) comments are left out.
  - `start_from`/`start_to` map to `StartLine`/`StartSide`. A range keeps its
    start when the line or the side differs from the end.
  - An inline comment with no path, or a range start with no end line, is an
    error.
- `ReplyTo` is the immediate parent on Bitbucket but the thread root on
  GitHub. #17's `Reply` must not copy GitHub's root logic.

## Bitbucket API used

- Auth: HTTP Basic, username the account email, password the API token.
- Pages: `{"values": [...], "next": "<absolute URL>"}`; follow `next` until
  absent. Ask for `pagelen=50`.
- `GET /repositories/{workspace}/{slug}/pullrequests?state=OPEN` (repeat
  `state` for several).
- `GET /repositories/{workspace}/{slug}/pullrequests/{id}`: `id`, `title`,
  `description`, `state`, `draft`, `author`, `source.branch.name`,
  `source.commit.hash`, `destination.branch.name`, `destination.commit.hash`,
  `created_on`, `updated_on`, `links.html.href`, `participants[]` with
  `user`, `state` (`approved`, `changes_requested` or null) and
  `participated_on`.
- `GET .../pullrequests/{id}/diff` answers 302 to `/repositories/.../diff/...`,
  which returns a git diff as text.
- `GET .../pullrequests/{id}/comments`: `id`, `content.raw`, `user`,
  `created_on`, `deleted`, `parent.id`, `inline.path`, `inline.from`,
  `inline.to`.
- `GET /user`: the authenticated account.
- Errors: `{"type": "error", "error": {"message": "..."}}`.

## Task 1: client, auth, errors, viewer, list

- `NewClient(baseURL, email, token string, repo pr.Repo) *Client`.
- `Viewer` and `List` on `*Client`.
- Done when:
  - every request carries `Authorization: Basic` of `<email>:<token>`
  - a 401 becomes an error starting with Bitbucket's error message
  - `pr.Open`, `pr.Merged`, `pr.Closed` (DECLINED and SUPERSEDED) map to
    `State` `OPEN`, `MERGED`, `CLOSED`; `pr.All` lists every state
  - a list over two pages follows `next` and returns all
  - `Viewer` returns the same field as a pull request's `Author`, so
    `pr.IsOwn` is true for a pull request the viewer opened

## Task 2: get, diff, comments

- `Get`, `Diff`, `Comments` on `*Client`.
- Done when:
  - `Get` sets `HeadSHA` from the source commit, `BaseSHA` from the
    destination commit
  - participants with `approved` or `changes_requested` become `pr.Verdict`s
  - `Diff` follows the redirect, the auth header is on the redirected request,
    and `diff.Parse` reads the result into files
  - comments over two pages are all returned
  - comment mapping: no `inline` → on the pull request; `inline` with path and
    no line → on the file; `inline.to` → that line on `diff.Head`;
    `inline.from` alone → that line on `diff.Base`
  - a reply's `ReplyTo` is its parent's ID
  - a `deleted` comment is left out
