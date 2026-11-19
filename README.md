<h1 align="center">factorymeter</h1>

<p align="center">
  <strong>Find where your delivery line waits, now that agents write the code.</strong><br>
  One command reads your GitHub history, splits it by agent and human pull requests, and names the constraint.
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
factorymeter report owner/repo                      # one repository, the last 30 days
factorymeter report my-org --since 12w              # every active repository of an owner
factorymeter report owner/repo --html report.html   # also write a one-page HTML report
```

It only reads. It needs a read-only GitHub token, it runs on your machine, and it sends nothing
anywhere except to the GitHub API.

---

## Why

Coding agents open pull requests faster than people do. The line does not get faster by the same
amount. The work piles up somewhere else: waiting for a first review, waiting for CI, waiting for
someone to press merge.

Counting how much of the code agents wrote does not tell you what to fix. factorymeter measures
each stage a pull request passes through and tells you which one holds the most time. That stage
is your constraint. Speeding up any other stage changes little.

| Question | What factorymeter reports |
|---|---|
| Where does work wait? | The **constraint**: the stage that holds the largest share of the time between open and merge |
| Do agent pull requests wait longer than human ones? | Every number side by side for agent and human pull requests |
| How much of the work do agents do? | **Disclosed agent share**, which is a floor, and says so |
| What should I try first? | A link that opens the [Lights Out](https://thesoftwarefactory.site/lights-out/) simulator on your own numbers |

## What you get

This is the top of a real report. It was run on
[`marshal-org/runlease`](https://github.com/marshal-org/runlease) on 2026-11-18, and it took 10
GitHub API calls and 18 seconds.

```text
factorymeter  marshal-org/runlease  2026-10-19 to 2026-11-18 (30 days)

Your pull requests spend most of their time ready and waiting for a merge that no other
person reviews: a median of 16 min, and 53% of the time from open to merge.

- The constraint is the merge station (ready, merged with no human review). It holds 53%
  of the time agent and human pull requests spend between open and merge.
- There are too few pull requests to compare agents with humans at the constraint. It
  takes 5 of each, and this window has 1 agent and 43 human.
- Disclosed agent share is 2%: 1 of the 44 pull requests merged by agents and people.
  This is a floor, because an agent that leaves no trace counts as human.

Agent and human pull requests
                                  Agent    Human  Automation
  Pull requests merged                1       43           9
  Merged per week                   0.2     10.0         2.1
  Merge rate                        25%     100%         90%
  Open to merge, median           5 min   19 min      59 min
  Time to first review, median      n/a      n/a         n/a
  Merged with no human review      100%     100%        100%
  Rework rate                       n/a      n/a         n/a
  CI time per run, median       4.2 min  5.6 min     5.8 min
  CI runs per pull request          1.0      2.6         1.0
  CI runs that failed                0%      10%         89%
  Size, median lines changed       1358      363          10

Where the time goes
  Stage                                            Station  Share    Agent median    Human median  Agent PRs  Human PRs
  In draft                                            code     4%  under a minute  under a minute          1         43
  Ready, merged with no human review (constraint)    merge    53%           5 min          16 min          1         43
  CI running                                            ci    43%           4 min          12 min          1         43
```

The full report goes on to list which agents it found, the numbers for the simulator, and the
limits of what it could see. Every definition is in [docs/metrics.md](docs/metrics.md).

The next version of the report is built on a fixed contract: seven stations, a
registry of metric ids with exact definitions, and a JSON layout with a schema.
[docs/contract.md](docs/contract.md) describes it, and `factorymeter metrics`
prints the registry.

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
access to: Metadata, Contents, Pull requests, Actions and Deployments. It needs no write
permission of any kind. A public repository needs no permissions at all, only a token for the
rate limit.

The client refuses to send a GraphQL mutation and only sends `GET` requests to the REST API, so
it cannot change anything even with a token that could.

## The three outputs

| Output | How | For |
|---|---|---|
| Terminal | the default | reading it now |
| HTML | `--html report.html` | one self-contained page to share. No scripts, no outside assets |
| JSON | `--json report.json`, or `--format json` | your own dashboards. `--detail` adds one row per pull request |

`--format markdown` prints the report as Markdown, which is what the GitHub Action writes to the
job summary.

Other useful flags:

```bash
factorymeter report owner/repo --since 2026-01-01 --until 2026-03-31   # a fixed window
factorymeter report owner/repo --save-raw raw.json                     # keep what was collected
factorymeter report --from-raw raw.json                                # rebuild the report offline
factorymeter classify owner/repo#123                                   # why one pull request counted
factorymeter signatures list                                           # every signature in use
```

## The trend

One report is one snapshot. `trend` cuts a longer window into equal periods, measures each one
the way the report does, and shows what changed.

```bash
factorymeter trend owner/repo                               # the last 12 weeks, week by week
factorymeter trend owner/repo --since 26w --every 2w        # half a year in two-week periods
factorymeter trend owner/repo --html trend.html --json trend.json
factorymeter trend --from-raw raw.json --every 4w           # another cut of the same data, offline
```

It follows every number of the report: pull requests merged per week, the disclosed agent share,
merge rate, deploys, the time from open to merge, each stage's share of that time, review, CI,
size, and each disclosed agent. It says whether the constraint moved.

The terminal and Markdown outputs give both ends, the change and a small bar line per number.
The HTML page draws one chart per number, agent against human, with the full table under each
group. Like the report, it is one file with no scripts and no outside assets.

The change is measured between the first third and the last third of the periods, so one odd
week does not decide it. [docs/metrics.md](docs/metrics.md#the-trend) has the rules.

## Keep the history on disk

A report reads the whole window from GitHub each time. `sync` reads it once into a store on
your machine, and after that only what changed.

```bash
factorymeter sync owner/repo      # the first sync reads 26 weeks; a later one reads what changed
factorymeter sync my-org          # every active repository of an owner
factorymeter store info my-org    # what the store holds
```

The store is a directory of Parquet files. A sync that is stopped goes on where it left off.
`report` and `trend` move to the store in the next release. [docs/store.md](docs/store.md) has
the details.

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

The report prints these limits every time. They are not fine print.

- **Disclosed agent share is a floor.** Cloud agents sign their work with their own account, so
  they are easy to see. A local agent is seen only when it leaves a trailer, a footer or a badge,
  and a person can turn those off. Those pull requests count as human.
- **Squash merges can drop commit trailers.** factorymeter reads the commits of each pull
  request, not the merge commit, and at most the first 100 commits.
- **A branch prefix alone is not proof.** People type `claude/` and `codex/` by hand.
- **Times are wall-clock.** Nights and weekends count.
- **CI means GitHub Actions.** Other CI systems are not read.
- **A pull request is agent or human as a whole.** There is no line-level attribution.
- **Work that skips pull requests is not measured.**

It has not been tested against GitHub Enterprise Server. It reads `GITHUB_API_URL` and
`GITHUB_GRAPHQL_URL` if you want to try.

## Run it on a schedule

factorymeter is also a GitHub Action. It writes the report to the job summary and uploads the
HTML and JSON as a build artifact. It asks for read permissions only.

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

The report ends with a link to [Lights Out](https://thesoftwarefactory.site/lights-out/), a
simulator of a delivery line. The link fills its "My factory" form with your measured numbers, so
you can see what happens to your line when you add review capacity, faster CI or more agents.

Your numbers ride after the `#` in the link. A browser never sends that part of an address to a
server, so opening the link shares nothing.

## What it does not do

- It does not write to GitHub. No comments, no labels, no checks.
- It is not a hosted service. There is no account and no dashboard to log in to.
- It sends no telemetry.
- It does not name people. The report holds counts and times, not authors or reviewers.

## Contributing

The most useful contribution is a new or corrected signature. See
[CONTRIBUTING.md](CONTRIBUTING.md). Security reports go to [SECURITY.md](SECURITY.md).

## License

[Apache-2.0](LICENSE).
