<h1 align="center">factorymeter</h1>

<p align="center">
  <strong>Find where your delivery line waits, now that agents write the code.</strong><br>
  It reads your GitHub history, follows each change through seven stations, splits it by agents and people, and names the constraint.
</p>

<p align="center">
  <a href="https://github.com/marshal-org/factorymeter/actions/workflows/ci.yml"><img alt="CI" src="https://github.com/marshal-org/factorymeter/actions/workflows/ci.yml/badge.svg"></a>
  <a href="https://github.com/marshal-org/factorymeter/releases"><img alt="Release" src="https://img.shields.io/github/v/release/marshal-org/factorymeter"></a>
  <a href="LICENSE"><img alt="License: Apache 2.0" src="https://img.shields.io/badge/license-Apache--2.0-blue"></a>
  <a href="https://pkg.go.dev/github.com/marshal-org/factorymeter"><img alt="Go Reference" src="https://pkg.go.dev/badge/github.com/marshal-org/factorymeter.svg"></a>
</p>

<p align="center">
  Built by <a href="https://marshal.codes/?utm_source=github&amp;utm_medium=readme&amp;utm_campaign=factorymeter&amp;utm_content=top"><strong>Marshal</strong></a>: AI coding agents at full speed, with full receipts.<br>
  Marshal runs coding agents in isolated pods inside your own VPC. factorymeter is how we look at our own delivery line.
</p>

```bash
factorymeter sync owner/repo      # read the history into a store on your machine
factorymeter report owner/repo    # the line, station by station, with 26 weeks of history
factorymeter sync my-org && factorymeter report my-org   # every active repository of an owner
```

It only reads. It needs a read-only GitHub token, it runs on your machine, and it sends nothing
anywhere except to the GitHub API.

---

## Why

Coding agents open pull requests faster than people do. The line does not get faster by the same
amount. The work piles up somewhere else: waiting for a first review, waiting for CI, waiting for
someone to press merge.

Counting how much of the code agents wrote does not tell you what to fix. factorymeter follows
each change through seven stations, `spec` → `code` → `ci` → `verify` → `review` → `merge` →
`release`, and tells you which one holds the most time. That station is your constraint.
Speeding up any other station changes little.

| Question | What factorymeter reports |
|---|---|
| Where does work wait? | The **constraint**: the station that holds the largest share of the hours changes spend on the line |
| What happens at each station? | The same five numbers for every station: how much flows out, the wait, the work, the rework and the queue |
| Do agent changes wait longer than human ones? | Every number side by side for agents, people and automation |
| Is it getting better? | Every number against the weeks before, with one point per week for up to 26 weeks |
| How much of the work do agents do? | **Disclosed agent share**, which is a floor, and says so |

## What you get

This is the top of a real report. It was run on
[`marshal-org/runlease`](https://github.com/marshal-org/runlease) on 2026-11-19, from a store that
held 73 changes, 319 CI runs, 55 deploys and 6 issues.

```text
DELIVERY LINE: MARSHAL-ORG/RUNLEASE
===================================

13 weeks, 2026-08-20 to 2026-11-19. Compared with the 13 weeks before. History: 26 weeks.

Release is the constraint. It holds 77 percent of the hours changes spend on the line.

Over the 13 weeks to 2026-11-19, changes stood 12 hours a week at release and 2.5 a week left it. Before the merge, code holds the most: 56 percent of those hours.

THE LINE

  Station               Status         Out a week   Wait  Work  Rework  Queue    Held  Out, by week
  Spec                  measured            0.2 ▲   7m ~          0% ~   2 ▲!          ▁▁▁▁▁▁▁▁▅▁▁▁▁▁▁▁▁▁▁▁▅▁▁▁▁█
  Code                  measured            3.8 ▲           0m   6% ▲!      0  13% ▲!  ▁▂▁▁▁▁▁▁▁▁▁▁▁▁▁▁▁▁▁▁▁▁▁▁▁█
  CI                    measured            8.0 ▲      -    6m      9%             5%  ▁▁▁▁▁▁▁▁▁▁▁▁▁▁▁▁▁▁▁▁▁▁▁▁▁█
  Verify                not connected
  Review                measured              0.0      -     -       -      0          ▁▁▁▁▁▁▁▁▁▁▁▁▁▁▁▁▁▁▁▁▁▁▁▁▁▁
  Merge                 measured            3.5 ▲  16m ▼            0%      0    6% ▼  ▁▂▁▁▁▁▁▁▁▁▁▁▁▁▁▁▁▁▁▁▁▁▁▁▁█
  Release (constraint)  partial             2.5 ▲   4.1h     -       -      0     77%  ▁▁▁▁▁▁▁▁▁▁▁▁▁▁▁▁▁▁▁▁▁▁▁▁▁█

Wait and work are medians. Held is the station's share of the hours changes spend on the line. ▲ and ▼ show a move against the weeks before, ! a move for the worse, ~ a value drawn from fewer than 5 items.

  * Verify: Fewer than 5 deployments to an environment that is not production were found on a commit of a change before it merged.
  * Review: No change in the store was reviewed by another person. A change that waits here may be waiting to merge.
  * Release: Published releases stand in for production deploys in marshal-org/runlease.

WHERE TO INVEST

  #  What to do                                                Station  Hours freed a week  Confidence
  1  Release more often                                        Release                   0  high
  2  Prove changes on a running environment before they merge  Verify                       high
  3  Have agents run the checks before they push               CI                           medium

Hours freed is the waiting that goes away each week if the station gets back to the pace of its own best four weeks. It is empty where a gain cannot be sized, and 0 where no four weeks of the window did better. Confidence is how much data stands behind the numbers. `factorymeter rules` lists every rule and when it fires.

1. Release more often

Release is the constraint: it holds 77 percent of the hours changes spend on the line. A change is held 5.0 hours here on average, and no four weeks of the window did better. The gain has to come from a change in how the station works.

  * Changes were held 12 hours a week at release. No four weeks of the window did better.
  * Share of the line's time held at release: 77 percent.
  * Wait for a release, median: 4.1 hours.
  * Production deploys: 0.6 a week. It was 0.0 a week in the 13 weeks before.
  * Changes merged and not released: 0. It was 0 at the end of the 13 weeks before.
  * Practice: https://thesoftwarefactory.site/playbook/find-the-constraint/
```

**Where to invest** is the part to act on. Each line is a rule that fired on this line's own
numbers. A rule says what to do, shows the numbers behind it, and where it can, names the
changes to look at first. A gain is sized against the station's own best four weeks, not
against an outside benchmark. `factorymeter rules` prints every rule and the exact condition
under which it fires, and [docs/reference/rules.md](docs/reference/rules.md) has the same list.

The report goes on with the other recommendations, agents next to people, then each station
number by number with its history, then what was loaded and the limits of what it could see. A
station that cannot be measured says what to connect. It is never shown as zero.

Every number has a fixed id and an exact definition. `factorymeter metrics` prints them, and
[docs/contract.md](docs/contract.md) says what you can rely on.

## Install

With Go:

```bash
go install github.com/marshal-org/factorymeter/cmd/factorymeter@latest
```

Or download a binary for Linux, macOS or Windows from the
[releases page](https://github.com/marshal-org/factorymeter/releases). Each release has a
`checksums.txt`, a cosign signature and a build provenance attestation.

Or build from source:

```bash
git clone https://github.com/marshal-org/factorymeter
cd factorymeter
go build -o factorymeter ./cmd/factorymeter
```

## The token

factorymeter looks for a token in `GITHUB_TOKEN`, then `GH_TOKEN`, then asks the GitHub CLI
(`gh auth token`) if you are logged in. It never prints the token and never writes it to a file.

For a private repository, make a
[fine-grained token](https://github.com/settings/personal-access-tokens/new) with **read-only**
access to: Metadata, Contents, Pull requests, Issues, Actions and Deployments. It needs no write
permission of any kind. A token that cannot read issues works with `sync --skip-tickets`. A public repository needs no permissions at all, only a token for the
rate limit.

The client refuses to send a GraphQL mutation and only sends `GET` requests to the REST API, so
it cannot change anything even with a token that could.

## Sync, then report

`sync` reads the history from GitHub into a store on your machine. The first sync reads 26
weeks. A later one reads only what changed. `report` reads the store and nothing else, so it
takes no token, makes no request and can run as often as you like.

```bash
factorymeter sync my-org                       # every active repository of an owner
factorymeter report my-org                     # the last 13 weeks against the 13 before
factorymeter report my-org --since 26w         # a longer window
factorymeter report owner/repo --until 4w      # the line as it stood four weeks ago
factorymeter serve my-org                      # the same report as a page in the browser
factorymeter store info my-org                 # what the store holds
```

Run both once a week and the report follows the line week after week. A sync that is stopped
goes on where it left off. The store is a directory of Parquet files:
[docs/store.md](docs/store.md) has the details.

| Output | How | For |
|---|---|---|
| Terminal | the default | reading it now |
| Markdown | `--format markdown` | a job summary or a pull request |
| One HTML page | `--html line.html`, or `--format html` | keeping it, or publishing it from a job. It is the page `serve` shows |
| JSON | `--json line.json`, or `--format json` | your own dashboards. The layout is `factorymeter/v1`, with a [JSON Schema](docs/reference/factorymeter-v1.schema.json) |
| Summary | add `--summary` | sharing. It leaves out every repository name, title and link, so the file holds station numbers only |

Other useful commands:

```bash
factorymeter metrics review                # the numbers of one station, with definitions
factorymeter rules                         # every recommendation and when it is made
factorymeter rules review                  # the rules of one station
factorymeter classify owner/repo#123       # why one pull request counted as agent or human
factorymeter signatures list               # every signature in use
```

## See it as a page

```bash
factorymeter serve my-org          # then open http://127.0.0.1:7377
```

`serve` shows the report as a page, read from the store. The page is laid out the way the line
runs:

1. **The constraint**, as the headline, with the numbers of the whole line next to it.
2. **The line**: the seven stations in order, each with what leaves it a week, the wait, and its
   share of the hours changes spend on the line.
3. **Where to invest**: the recommendations, ranked by the hours a week each one frees.
4. **Agents and people**, side by side, then by agent, and for an owner by repository.
5. **Each station**, number by number, one chart per number with up to 26 weeks of history. The
   weeks before the window are dimmed. Each station also has its numbers week by week as a table.
6. **Coverage**: what was loaded, and the limits of what it could see.

The page switches between 4, 13 and 26 weeks, and for an owner it narrows to one repository. A
sync that runs while the server is up shows on the next reload. `/report.json` answers with the
same report as `factorymeter/v1` JSON.

`report --html line.html` writes the same page to one file. The file carries its own style and
script and loads nothing from the network, so it can be kept, sent, or published from a job.
With `--summary` it holds station numbers only.

What `serve` does and does not do:

- It reads the store and nothing else. It takes no token and sends nothing.
- It listens on this machine only, and it answers only requests made to `localhost` or a
  loopback address. That check keeps a web page you visit from reading the line through a name
  it points at your machine.
- It has no login. To reach it from another machine, give `--addr` an address that machine can
  reach and name the host with `--allow-host`. Then everyone who can reach the address can read
  the line.
- The page allows only its own style and script by their hashes. A title or a name in the data
  cannot run as script.

## Without a store

The first generation of factorymeter is still here. `snapshot` and `trend` read a window straight
from GitHub on every run and keep nothing. They measure pull requests between open and merge, and
write a one-page HTML report.

```bash
factorymeter snapshot owner/repo --html report.html        # the last 30 days
factorymeter trend owner/repo --since 26w --every 2w       # the same numbers over time
factorymeter snapshot owner/repo --save-raw raw.json       # keep what was collected
factorymeter trend --from-raw raw.json --every 4w          # another cut, offline
```

[docs/metrics.md](docs/metrics.md) defines their numbers. They are not the numbers of the
contract: `report` is where new work goes.

## How agents are detected

Every rule is in one open file: [`signatures/registry.yaml`](signatures/registry.yaml). Version 1
knows six agents: GitHub Copilot coding agent, OpenAI Codex, Cursor, Claude Code, Devin and
Jules.

A signature is one of: the account that opened the pull request, a commit author, a commit
trailer such as `Co-authored-by`, a footer or badge in the description, or a branch prefix. Each
has a confidence level:

| Confidence | Meaning | Counts as agent? |
|---|---|---|
| high | The agent's own account opened the pull request or wrote a commit | yes |
| medium | A disclosure the tool writes by default: a trailer, a footer, a badge | yes |
| low | A naming habit, such as a `codex/` branch prefix | no, reported as "possible" only |

Each signature is tested against a recorded, real, public pull request. A signature with no
such pull request fails the build. To add an agent, see [CONTRIBUTING.md](CONTRIBUTING.md).

Dependency bots and release bots are a third class, **automation**. They are left out of both the
agent and the human numbers.

## What it cannot see

The report prints the limits that apply every time. They are not fine print.

- **Disclosed agent share is a floor.** Cloud agents sign their work with their own account, so
  they are easy to see. A local agent is seen only when it leaves a trailer, a footer or a badge,
  and a person can turn those off. Those pull requests count as human.
- **Squash merges can drop commit trailers.** factorymeter reads the commits of each pull
  request, not the merge commit, and at most the first 100 commits.
- **A branch prefix alone is not proof.** People type `claude/` and `codex/` by hand.
- **Times are hours on the clock.** Nights and weekends count.
- **Writing time starts at the first commit of a change.** Work before that commit is not seen.
- **A ticket is a GitHub issue.** An issue has no started state, so it starts when the first pull
  request that closes it is opened. An issue closed with no such pull request never counts as
  started. Linear and Jira are not read yet.
- **CI means GitHub Actions.** Other CI systems are not read.
- **A pull request is agent or human as a whole.** There is no line-level attribution.
- **Work that skips pull requests is not measured.**

It has not been tested against GitHub Enterprise Server. It reads `GITHUB_API_URL` and
`GITHUB_GRAPHQL_URL` if you want to try.

## Run it on a schedule

factorymeter is also a GitHub Action. It writes a report to the job summary and uploads the
HTML and JSON as a build artifact. It asks for read permissions only. The Action still runs
`snapshot`. It moves to `sync` and `report`, with the store kept between runs, in a later
release.

```yaml
name: factorymeter
on:
  schedule:
    - cron: "23 6 * * 1"   # Mondays, 06:23 UTC
  workflow_dispatch:

permissions:
  contents: read
  pull-requests: read
  actions: read
  deployments: read

jobs:
  report:
    runs-on: ubuntu-latest
    steps:
      - uses: marshal-org/factorymeter@v0.1.0   # or pin a commit SHA
        with:
          since: 30d
```

| Input | Default | Meaning |
|---|---|---|
| `target` | the repository the workflow runs in | `owner/repo`, or an owner for all of its active repositories |
| `since` | `30d` | start of the window |
| `token` | the job's own token | a token that can read the target. An owner-wide report needs your own read-only token |
| `summary` | `true` | write the report to the job summary |
| `artifact` | `true` | upload the HTML, JSON and Markdown as a build artifact |
| `artifact-name` | `factorymeter-report` | name of the artifact |

Outputs: `headline`, `constraint`, `constraint-share`, `disclosed-agent-share`, `lights-out-url`,
and the paths `html`, `json` and `markdown`. `constraint` and `constraint-share` are empty when fewer
than 5 pull requests merged in the window.

The Action builds the binary from the commit you pinned, so the code that runs is the code you
can read.

## Try a fix before you make it

`snapshot` ends with a link to [Lights Out](https://thesoftwarefactory.site/lights-out/), a
simulator of a delivery line. The link fills its "My factory" form with your measured numbers, so
you can see what happens to your line when you add review capacity, faster CI or more agents.

Your numbers ride after the `#` in the link. A browser never sends that part of an address to a
server, so opening the link shares nothing.

## What it does not do

- It does not write to GitHub. No comments, no labels, no checks.
- It is not a hosted service. There is no account and no dashboard to log in to.
- It sends no telemetry.
- It does not name people. The report holds counts and times, not authors or reviewers. The
  store on your machine does hold the logins GitHub returned, and it stays on your machine.

## Contributing

The most useful contribution is a new or corrected signature. See
[CONTRIBUTING.md](CONTRIBUTING.md). Security reports go to [SECURITY.md](SECURITY.md).

## License

[Apache-2.0](LICENSE).
