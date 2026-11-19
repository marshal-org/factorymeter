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
anywhere except to the GitHub API. When you ask for it, it also reads the tickets of a Linear
workspace, from the Linear API, or of a Jira Cloud site, from that site.

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

### A Linear key, if your tickets live in Linear

`sync --linear` also reads the tickets of a Linear workspace. Make a personal API key in Linear
under Settings, Security and access, and put it in `LINEAR_API_KEY`. Read access is all it
needs. There is no flag for the key, so it never lands in your shell history. factorymeter never
prints it and never writes it to a file.

```bash
export LINEAR_API_KEY=...                      # a personal API key, read only
factorymeter sync my-org --linear              # GitHub, then every team the key can see
factorymeter sync my-org --linear-team ENG --linear-team OPS   # only these teams
```

A key belongs to one workspace, and that is the workspace that is read. The client sends
queries only and refuses a mutation, the same as the GitHub client.

### A Jira account, if your tickets live in Jira

`sync --jira <site>` also reads the tickets of a Jira Cloud site. Make an API token under
Manage account, Security, API tokens, and put it in `JIRA_API_TOKEN`. Put the email of that
account in `JIRA_EMAIL`. An account that can browse the projects is all it needs. There is no
flag for either, so they never land in your shell history. factorymeter never prints them and
never writes them to a file.

```bash
export JIRA_EMAIL=...                          # the account the token belongs to
export JIRA_API_TOKEN=...                      # an API token of that account
factorymeter sync my-org --jira acme.atlassian.net                      # every project the account can see
factorymeter sync my-org --jira acme.atlassian.net --jira-project ENG   # only this project
```

The client has one verb, `GET`, and sends the account to the site you name and to no other
host. It does not follow a redirect. Jira Data Center is not read.

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
factorymeter sync my-org --linear              # also read the tickets of a Linear workspace
factorymeter sync my-org --jira acme.atlassian.net   # also read the tickets of a Jira Cloud site
```

In a terminal, `sync` first says what it is about to read and where it stores it. Then it
shows each step as it goes, with a bar and an estimate of the time left. The estimate comes
from the pace of this sync, and before that from the time the last sync took. When GitHub's
rate limit is reached, the sync says so, counts down the wait and goes on by itself. At the
end it says what the store now holds and which command to run next.

In a pipe or a CI log there is no live view: `sync` writes one line per event to stderr and
a table to stdout. `--plain` gives the same lines in a terminal, and `--quiet` leaves the
progress out.

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

`serve` shows the report as a page, read from the store. It has four pages:

1. **Overview**: the constraint as the headline, the numbers of the whole line, and **the line**:
   the seven stations in order. Each station is graded flowing, watch or strained, with a colored
   mark and a word. Pick a station and its numbers open right below the line: a chart per number
   with up to 26 weeks of history, the reasons for its grade, and, for a station that is not
   connected, what it needs. A median and its 90th percentile share one chart.
   Above the line, the page names the constraint and the station next in line. Each station
   lists the recommendations about it, with a link to the full ranked list.
2. **Where to invest**: the recommendations, ranked by the hours a week each one frees.
3. **Agents and people**, side by side, then by agent, and for an owner by repository.
4. **Coverage**: what was loaded, and the limits of what it could see.

Every number has an info mark. Point at it, or tab to it, to read what it counts. Each value says
what it is drawn from, such as the average week of the window or the median of its items, and
what it was in the window of the same length before.

A station gets the worst of three grades. Load: 20% or more of the hours changes spend on the
line is watch, 40% or more is strained. Rework: 20% or more of its items failed or sent back is
watch, 40% or more is strained. Wait: a median wait that got worse than in the window before is
watch.

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
- **A ticket is a GitHub issue, a Linear issue with `sync --linear`, or a Jira issue with
  `sync --jira`.** A GitHub issue has no started state, so it starts when the first pull request
  that closes it is opened. An issue closed with no such pull request never counts as started.
- **A Linear ticket starts when it first enters a started state, or when the first pull request
  linked to it is opened, whichever is earlier.** A pull request is linked when Linear has it
  attached, or when its branch, title or description names the key of the ticket, such as
  `ENG-123`. A word that only looks like a key is taken for one.
- **A Linear ticket has no repository of its own.** It counts for the repository of the first
  pull request linked to it. The report of one repository counts only the tickets linked to its
  pull requests. The report of the owner counts them all.
- **The Linear adapter is proven against Linear's published schema and a stand-in server, not
  yet against a live workspace.** Tell us what you see.
- **A Jira ticket starts when it first enters a status of the category In Progress, or when the
  first pull request linked to it is opened, whichever is earlier.** A pull request is linked
  when its branch, title or description names the key of the ticket, such as `ENG-123`. Jira
  does not say which pull requests belong to an issue, so the key is the only tie. Like a Linear
  ticket, a Jira ticket counts for the repository of the first pull request linked to it.
- **Epics are not tickets.** An issue above the level of a story or a task is left out. Subtasks
  are counted.
- **The Jira adapter is proven against Atlassian's published API description and a stand-in
  server, not yet against a live site.** Jira Cloud only. Tell us what you see.
- **CI means GitHub Actions.** Other CI systems are not read.
- **A run of the merge queue belongs to one pull request.** The queue runs CI on a branch it makes
  and names after a pull request. The run counts for that one, also when the branch carries the
  pull requests ahead of it in the queue.
- **A deploy is matched to changes by its commit.** A production deploy of the commit a merge left
  holds what merged into that branch up to that merge. Of any other deploy only the time is known,
  and it is taken to hold what merged before it started.
- **A pull request is agent or human as a whole.** There is no line-level attribution.
- **Work that skips pull requests is not measured.**

It has not been tested against GitHub Enterprise Server. It reads `GITHUB_API_URL` and
`GITHUB_GRAPHQL_URL` if you want to try.

## Run it on a schedule

factorymeter is also a GitHub Action. Each run syncs the store, writes the report to the job
summary, and uploads the HTML page, the JSON and the Markdown as a build artifact. It asks for
read permissions only.

The Action keeps the store between runs as a second build artifact. The first run reads 26
weeks of history. Every run after it starts from the store the run before saved and reads only
what changed, so a weekly run takes a few requests.

```yaml
name: factorymeter
on:
  schedule:
    - cron: "23 6 * * 1"   # Mondays, 06:23 UTC
  workflow_dispatch:

permissions:
  contents: read
  pull-requests: read
  issues: read
  actions: read
  deployments: read

jobs:
  report:
    runs-on: ubuntu-latest
    steps:
      - uses: marshal-org/factorymeter@v0.1.0   # or pin a commit SHA
        with:
          since: 13w
```

| Input | Default | Meaning |
|---|---|---|
| `target` | the repository the workflow runs in | `owner/repo`, or an owner for all of its active repositories |
| `since` | `13w` | the window the report covers, in whole weeks |
| `history` | `26w` | how far back the first run reads. The report compares the window with the one before it, so give at least twice the window |
| `token` | the job's own token | a token that can read the target. An owner-wide report needs your own read-only token |
| `linear-api-key` | empty | a Linear personal API key. With it the tickets of Linear are read too. Pass it from a secret |
| `linear-teams` | empty | the keys of the Linear teams to read, separated by commas or spaces. Empty reads every team the key can see |
| `jira-site` | empty | the host of a Jira Cloud site, such as `acme.atlassian.net`. With it, `jira-email` and `jira-api-token`, the tickets of Jira are read too |
| `jira-email` | empty | the email of the Jira account the API token belongs to. Pass it from a secret |
| `jira-api-token` | empty | an API token of a Jira account that can read the site. Pass it from a secret |
| `jira-projects` | empty | the keys of the Jira projects to read, separated by commas or spaces. Empty reads every project the account can see |
| `summary` | `true` | write the report to the job summary |
| `artifact` | `true` | upload the HTML, JSON and Markdown as a build artifact |
| `artifact-name` | `factorymeter-report` | name of the artifact that holds the report |
| `store` | `true` | keep the store between runs. With `false` every run reads the whole history and nothing is kept |
| `store-artifact-name` | `factorymeter-store` | name of the artifact that holds the store. The owner of the target is added to it |

Outputs: `headline`, `constraint`, `constraint-share`, `agent-share`, `recommendation`,
`restored`, `requests`, and the paths `html`, `json`, `markdown` and `store`. `constraint` and
`constraint-share` are empty when the window has too little to name a constraint.

What to know about the store artifact:

- **It is as private as your repository.** Anyone who can read the repository the workflow runs
  in can download its artifacts. The store of a private repository holds its pull request
  titles and logins. So when the workflow runs in a public repository and the target has a
  private repository in it, the Action does not save the store, writes the report without
  names, and says so in a warning. Tickets of Linear and of Jira always count as private, so
  the same holds for a store with such tickets in a public repository.
- **It is only taken from a run of your own repository.** A pull request from a fork can upload
  an artifact under any name. The Action never restores one of those.
- **A run that stopped still saves what it read.** The next run goes on from there.
- **GitHub deletes artifacts after 90 days, or sooner if your settings say so.** After that the
  next run reads the whole history again. Nothing else is lost.
- **To start over,** delete the artifact, or run once with `store: false`.

The job needs `actions: read` to find the store, and the GitHub CLI, which GitHub's own runners
have. Without either, the run still works: it reads the whole history and prints a warning.

To read Linear as well, give the key from a secret:

```yaml
      - uses: marshal-org/factorymeter@v0.1.0
        with:
          linear-api-key: ${{ secrets.LINEAR_API_KEY }}
          linear-teams: ENG, OPS      # leave out to read every team
```

When the secret is empty, as it is for a pull request from a fork, Linear is not read and the
run goes on.

To read Jira as well, give the site, and the account from secrets:

```yaml
      - uses: marshal-org/factorymeter@v0.1.0
        with:
          jira-site: acme.atlassian.net
          jira-email: ${{ secrets.JIRA_EMAIL }}
          jira-api-token: ${{ secrets.JIRA_API_TOKEN }}
          jira-projects: ENG, OPS     # leave out to read every project
```

When one of the three is empty, Jira is not read, a warning names what is empty, and the run
goes on.

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
  store does hold the logins GitHub returned. It stays on your machine, or, when the Action
  runs, in a build artifact of your own repository.

## Contributing

The most useful contribution is a new or corrected signature. See
[CONTRIBUTING.md](CONTRIBUTING.md). Security reports go to [SECURITY.md](SECURITY.md).

## License

[Apache-2.0](LICENSE).
