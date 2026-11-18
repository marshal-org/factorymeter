# Security policy

factorymeter reads a GitHub repository with a token you give it. It only reads, and it sends
nothing anywhere except the GitHub API. A bug that breaks either of those is a security bug.

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
- **No telemetry.** The only host it talks to is the GitHub API.
- **The token stays in memory.** It is never printed, logged or written to a report.
- **Reports hold no secrets**, but they do hold your numbers. The link to Lights Out carries
  the measured numbers in the URL. Share a report the way you would share the numbers.
