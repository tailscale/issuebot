# issuebot

> [!NOTE]
> Please file issues for this code in our [main open-source repository][oss].

**issuebot** listens for webhooks from GitHub for Pull Request notifications.
It checks that some commit in the pull request mentions an issue, allowing us
to better track development of the codebase. If no commit links to an issue,
issuebot marks the PR as failing checks.

There are two special cases allowing the requirement to be skipped:

  - If a commit contains "#cleanup".

  - If the author of a commit is a known automation bot.

An author is considered a bot if:

  - Its name contains the string "[bot]"

  - Its e-mail address matches the --bot-author-regxp, and its first
    parenthesized subexpression (if any) is a case-insensitive match for its
    name once spaces are replaced by "-".

If any commit contains "skip-issuebot" (and no issue is mentioned from other
commits), a stub issue will be created for the PR that you can fill out later.
This also makes the CI check pass, like with "#cleanup".

## Installation

```go
go install github.com/tailscale/issuebot/cmd/issuebot@latest
```

[oss]: https://github.com/tailscale/tailscale/issues
