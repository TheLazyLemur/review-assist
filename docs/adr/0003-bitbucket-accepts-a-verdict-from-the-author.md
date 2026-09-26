# Bitbucket accepts a verdict from the author

On an own pull request, review-assist posts a comment headed with the verdict
instead of the verdict, because GitHub refuses one from the author: its API
answers `Can not approve your own pull request` and `Can not request changes
on your own pull request`. Bitbucket Cloud does not refuse. So the own-PR rule
applies where the code host refuses a verdict from the author (today,
GitHub); on Bitbucket the verdict is sent, and the pull request
shows the approval or the request for changes. The code host reports this
through the port (`AcceptsVerdictFromAuthor`), so the core stays blind to the
platform, as ADR 0002 requires.

Checked on 2026-09-25 in a throwaway repository, as the account that opened
the pull request:

```
POST .../pullrequests/1/approve
200 {"type": "participant", "approved": true, "state": "approved", "role": "PARTICIPANT"}

POST .../pullrequests/1/request-changes
200 {"type": "participant", "approved": false, "state": "changes_requested", "role": "PARTICIPANT"}
```

The pull request then listed its author as a participant in that state.

## Considered Options

- **Keep the comment on Bitbucket too**, so both platforms behave the same.
  Rejected: an approval that Bitbucket would record becomes a plain comment,
  and the pull request shows no approval.
- **Check the platform in the core.** Rejected: ADR 0002 keeps the core
  platform-blind. The code host answers through the port instead.
