# What factorymeter measures

Every number in the report is defined here. If a number surprises you, this page says
exactly what was counted, so you can check it by hand.

## The window and the cohort

- The **window** is the last 30 days unless you pass `--since` and `--until`.
- The **cohort** is every pull request **merged inside the window**. Stage times, review
  times, CI numbers and sizes are computed over the cohort.
- Merge rate also counts pull requests **closed without merging** inside the window.
- "Waiting for a first review right now" counts pull requests that are open, not a draft,
  and have no review by another person at the end of the window.

## Agent, human, automation

Each pull request gets one class. The rules live in one file,
[`signatures/registry.yaml`](../signatures/registry.yaml).

| Class | Rule |
|---|---|
| **Agent** | At least one signature matches at `high` or `medium` confidence. |
| **Automation** | The author is a listed dependency or release bot, or any other bot account. |
| **Human** | Everything else. |

A signature looks at one of six places: the account that opened the pull request, the
login of a commit author, the email of a commit author, a commit trailer, the pull request
description, or the branch name.

| Confidence | What it means |
|---|---|
| `high` | The agent's own account opened the pull request or wrote a commit. |
| `medium` | A disclosure the tool writes by default: a trailer, a footer or a badge. |
| `low` | A naming habit such as a branch prefix. It never counts on its own. |

**Disclosed agent share** is agent pull requests divided by agent plus human pull requests,
over the cohort. Automation is left out of both sides. It is a floor: an agent that leaves
no trace counts as human.

**Possible agent pull requests** matched only a `low` signature. They count as human, and
the report says how many there were.

## Stages

A merged pull request is cut into stages. Each stage belongs to a station of the line.

| Stage | Station | The clock runs from, to |
|---|---|---|
| In draft | code | opened as a draft, until marked ready for review |
| Waiting for first review | review | ready for review, until the first review by another person |
| First review to approval | review | first review, until the last approval before merge (or merge, when nobody approved) |
| Approved, waiting to merge | merge | last approval, until merge |
| Ready, merged with no human review | merge | ready for review, until merge, when no other person reviewed |
| CI running | ci | time between open and merge with at least one GitHub Actions run in flight |

Rules that decide the edge cases:

- **Ready time.** The first "ready for review" or "converted to draft" event decides. If
  the first event is "ready for review", the pull request started as a draft and was ready
  at that time. Otherwise it was ready when it was opened.
- **A review counts** only when a person other than the author wrote it, before the merge.
  Reviews by bots, by the author, and after the merge do not count.
- A review that arrives while the pull request is still a draft gives a wait of zero.

### Two ways to time a stage

Each stage is timed twice, because one number cannot answer both questions.

- **Wall-clock** is what the pull request timeline shows. The medians in the report, the
  headline, and time to first review are wall-clock. Nights and weekends count.
- **Exclusive** takes the time a CI run was in flight out of the other stages and counts
  it as CI. The exclusive stages of a pull request add up to its time from open to merge.
  Shares and the constraint use exclusive time.

In the JSON export these are `stage_hours` and `stage_exclusive_hours`.

## The constraint

The constraint is the stage that holds the most exclusive time across agent and human pull
requests together. Automation is left out.

Before the times are added up, each pull request's time in a stage is **capped at that
stage's 90th percentile**. One pull request that sat for three months cannot name the
constraint on its own.

The **share** of a stage is its capped total divided by the capped total of all stages.

### The headline

The headline compares agent and human pull requests at the constraint.

- It compares only when there are at least **5** pull requests of each class in the stage.
- It compares medians. When either median is zero it compares means and says so.
- A ratio of **1.5 or more** in either direction is reported as a gap. Below that the
  report says the two are close.
- With too few pull requests to compare, the headline names the constraint and its size
  and makes no claim about agents and humans.

The report can also name the widest gap away from the constraint. It does so only when the
longer of the two waits is at least one hour.

## The table of classes

| Row | Definition |
|---|---|
| Pull requests merged | Size of the cohort for the class. |
| Merged per week | Merged, divided by the length of the window in weeks. |
| Merge rate | Merged / (merged + closed without merging), both inside the window. |
| Open to merge, median | Opened until merged, wall-clock. |
| Time to first review, median | Ready until the first review by another person. Only reviewed pull requests count. |
| Merged with no human review | Share of merged pull requests that no other person reviewed. |
| Rework rate | Of the reviewed pull requests, the share that review sent back: a review asked for changes, or a commit landed after the first review. A merge commit that pulls in the base branch is not rework. |
| CI time per run, median | See below. |
| CI runs per pull request | Mean number of CI runs. A re-run of a failed attempt counts as one more run. |
| CI runs that failed | Share of CI runs that failed, timed out, or needed a second attempt. |
| Size, median lines changed | Additions plus deletions. |

## CI

factorymeter reads GitHub Actions only.

- **One CI run** is everything Actions did for one pushed commit of the pull request. Its
  time is the time of the longest workflow for that commit, from start to finish.
- Runs that were skipped or cancelled are left out.
- Runs are read for the `pull_request` and `pull_request_target` events. Only when a
  repository has none of those does factorymeter fall back to runs of every event.
- Runs are read back to one window before the start of the window. A pull request that was
  open before then is missing its earliest runs. The report states the date.

## Deploys per week

The first source that has data wins:

1. Deployments to an environment whose name looks like production (`prod`, `production`,
   `live`). When several match, the one with the most deployments.
2. Published releases that are not drafts or prereleases.
3. Deployments to `github-pages`.

The report says which source it used.

## Numbers for Lights Out

The report ends with a link to the [Lights Out](https://thesoftwarefactory.site/lights-out/)
simulator, filled in with measured numbers.

| Simulator input | Measured as |
|---|---|
| Developers | Distinct people who had a pull request merged in the window. |
| Pull requests merged per week | Agent and human pull requests merged per week. |
| CI minutes per run | Median CI time per run. |
| Hours to first review | Median time to first review. |
| Deploys per week | As above. |
| CI runs per pull request | Mean CI runs per pull request. |
| Rework rate | Rework rate, when at least 5 pull requests were reviewed. |
| AI in use today | "Coding agents" when at least one agent pull request merged. |
| Test environments, product managers | Not on GitHub. Fill them in yourself. |

Each number is clamped to the range the simulator's form accepts.

The numbers ride after the `#` in the link. A browser never sends that part of an address
to a server, so opening the link shares nothing. The simulator reads the numbers, fills in
its form and removes them from the address bar. The only thing in the query string is
`utm_source=factorymeter`, which tells the site where the visit came from.

## What factorymeter cannot see

- **Agents that leave no trace.** A local agent that writes no trailer, footer or badge
  looks like its user. This is why the share is called *disclosed*.
- **Squash merges** can drop commit trailers from the merge commit. factorymeter reads the
  commits of the pull request, not the merge commit, and at most the first 100 of them.
- **Branch prefixes** such as `claude/` or `codex/` are typed by people too. A prefix alone
  is low confidence and does not count.
- **Work outside pull requests.** Direct pushes to the main branch are not measured.
- **CI outside GitHub Actions.**
- **Line-level authorship.** A pull request is agent or human as a whole.
