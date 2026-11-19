# Security policy

factorymeter reads a GitHub repository with a token you give it. It only reads, and it sends
nothing anywhere except the GitHub API and, when you run `sync --linear`, the Linear API, and
when you run `sync --jira`, the Jira site you name. A bug that breaks either of those is a
security bug.

## Reporting a vulnerability

**Please do not open a public issue.**

Report privately through GitHub:
[github.com/marshal-org/factorymeter/security/advisories/new](https://github.com/marshal-org/factorymeter/security/advisories/new)

If you cannot use GitHub, email `security@marshal.codes`.

Useful things to include:

- What an attacker can do, and what they need to already have.
- The version (`factorymeter --version`).
- Steps to reproduce, or a proof of concept.

## What to expect

| When | What |
|---|---|
| Within 2 working days | We confirm we received the report |
| Within 7 days | We tell you whether we can reproduce it and how serious we think it is |
| Within 30 days | A fix is released for confirmed issues, sooner for critical ones |

## What factorymeter promises

- **Read only.** The client sends `GET` requests and GraphQL queries. It refuses a GraphQL
  mutation before it reaches the network.
- **No telemetry.** The only host it talks to is the GitHub API. With `sync --linear` it also
  talks to the Linear API, and with `sync --jira` to the Jira site you name. It talks to no
  other host.
- **The token stays in memory.** It is never printed, logged or written to a report. The same
  holds for the Linear key, which is read from `LINEAR_API_KEY` only, and for the Jira email
  and API token, which are read from `JIRA_EMAIL` and `JIRA_API_TOKEN` only.
- **The Jira account goes to the site you name.** The site is asked over https. Plain http is
  only accepted for this machine. A redirect is not followed, so the email and the token are
  never sent on to another host.
- **`GITHUB_API_URL`, `GITHUB_GRAPHQL_URL` and `LINEAR_API_URL` name the host the token or the key
  is sent to.** They are there for GitHub Enterprise Server and for tests. Set them only to a
  host you trust with the token.
- **Reports hold no secrets**, but they do hold your numbers. The link to Lights Out carries
  the measured numbers in the URL. Share a report the way you would share the numbers.
