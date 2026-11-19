# The contract

The contract is what you can rely on when you read factorymeter with a program,
or when you compare one week's report with another's.

**Status: draft.** `factorymeter/v1` is not frozen until the first public
release. Until then a definition can still change without a new id.
`factorymeter report` writes this layout from the store. `snapshot` and `trend`
are the first generation and do not: [metrics.md](metrics.md) defines their
numbers. See [What is built](#what-is-built).

## Three layers

| Layer | What it fixes | Defined in | Reference |
|---|---|---|---|
| Records | What is kept about a line: tickets, changes, CI runs, deploys and the links between them | `pkg/record` | [reference/records.md](reference/records.md) |
| Metrics | Every number, with a stable id and an exact definition | `pkg/line` | [reference/metrics.md](reference/metrics.md), [reference/metrics.json](reference/metrics.json) |
| Report | The layout of the JSON a report writes | `pkg/contract` | [reference/report.md](reference/report.md), [reference/factorymeter-v1.schema.json](reference/factorymeter-v1.schema.json) |

The recommendations sit on top of the metrics. A rule reads metrics only, has
a stable id like a metric, and says in words when it fires. The rules are
defined in `pkg/line` and listed in [reference/rules.md](reference/rules.md).
The numbers a rule compares with are constants in the code, and the sentence
is built from them, so the two cannot drift apart.

The layers are separate on purpose. A source adapter (GitHub and Linear today,
Jira next) only writes records. A metric only reads records. A page only reads
the report. So a new source needs no change to a metric, and a new metric needs
no change to a source or to the page.

## The line

Every layer is built around the same seven stations, in the order work passes
through them:

`spec` → `code` → `ci` → `verify` → `review` → `merge` → `release`

Each station is asked the same five questions: how much flows through it, how
long items wait for it, how long it works on them, how much it sends back, and
how much is queued in front of it. A station that cannot answer one of them
says why, instead of reporting a guess.
[reference/metrics.md](reference/metrics.md) has every answer's definition.

## What you can rely on

1. **A metric id never changes meaning.** `review.wait.p50` is the same
   measurement in every release. If the definition has to change, the metric
   gets a new id.
2. **Inside one version, fields are only added.** A report marked
   `factorymeter/v1` always has every field the schema lists. A reader must
   ignore a field it does not know.
3. **A field is never left out.** A value that does not exist is `null`, an
   empty list or an empty string. You never have to tell "missing" from
   "absent".
4. **A number says how much stands behind it.** Every value carries the count
   of items it is drawn from, and is marked thin under the metric's minimum
   sample.
5. **A station that is not connected says so.** With no tickets in the store,
   `spec` is reported as `not_connected`. It is not reported as zero.
6. **Breaking changes get a new version.** They would ship as
   `factorymeter/v2`, with `v1` still written for a while.
7. **A rule id names the same recommendation.** `review.slow` is the same
   advice in every release. The number a rule compares with can be tuned, and
   the rule always says the number it uses.

## How a report is put together

`factorymeter report` reads the store and nothing else. The same records always
give the same report.

**The window.** A value is measured over the whole window, 13 weeks by default.
`--since 26w` makes it longer. The window ends at the last sync, or at
`--until`. It is always a whole number of weeks. Next to each value stand the
value of the window before it, and one point for every week the store holds, up
to 26. When the store does not reach back far enough, the window is cut to what
it holds and the report says so.

**When a thing counts.** A station's numbers count an item in the week it left
the station. The numbers of the whole line count a change in the week it
merged. A queue is the state at the end of the window or of the week.

**Who it counts.** Every change is an agent's, a person's or automation's. Each
number is given for the three classes and, when one line has several
repositories or agents, for each of them. The numbers of the whole line leave
automation out, so a dependency bot does not move them.

**Thin values.** A value drawn from fewer than 5 items is marked thin. It is
shown, it carries no trend, and it cannot make a station the constraint.

**The trend.** The value is compared with the window before. The move is
`better`, `worse` or `flat` by the direction the metric calls good, and
`unknown` when either side is thin or the metric has no good direction.

| Unit | A move is flat when it is under |
|---|---|
| Share | 2 points |
| Hours | 10 percent, or 3 minutes |
| A count in a queue | 10 percent, or 2 items |
| Everything else | 10 percent |

**The constraint.** Each station holds a change for some hours: the wait plus
the work. The constraint is the station with the largest share of those hours
over the window. `spec` is never named, because a ticket can wait for reasons
the line does not own. None is named when fewer than 5 changes by agents and
people merged in the window.

One change that stood for a year would decide the share alone. So before the
hours are added up, each item is cut off at the 90th percentile of the items of
the same station in the same period. The cut is made per period, so the weekly
points of `<station>.held.total` do not add up to the value of the window.

**A station that cannot be measured.** It is `not_connected`, or `partial` with
the reason in `missing`. Its numbers are `null`, never zero. Four cases come
up on GitHub alone:

- `spec` is not connected while the store holds no tickets. On GitHub a ticket
  is an issue. An issue has no started state, so it starts when the first pull
  request that closes it is opened. A Linear ticket starts when it first enters
  a started state, or when the first pull request linked to it is opened,
  whichever is earlier. See [store.md](store.md#tickets-from-linear).
- `verify` counts a deployment to an environment that is not production, made
  from a commit of a change before the change merged. Deployments of the
  branch a change merges into, and GitHub Pages, do not count. With fewer than
  5 of them, `verify` is not connected.
- `ci.wait` is left empty when the runs are stamped as started the moment they
  are created. Then the wait for a runner cannot be seen.
- `release` uses deployments to a production environment. A repository with
  none falls back to its published releases, and the station is `partial`.
- `release` is measured from the first production deploy in the store. A
  change merged before it is not counted at release, and the report says how
  many were left out. The store cannot tell a line that did not release from
  one whose releases were not recorded yet.

**Which deploy ships a change.** A production deploy of the commit a merge left
on its branch holds the changes merged into that branch up to that merge, and
none merged after it. A deploy of an older commit, such as a rollback, ships
nothing new. Of a deploy whose commit is no merge commit in the store, only the
time is known: it is taken to hold what merged before it started. A change
merged into a branch that is never deployed ships with the first deploy whose
commit merged after it. The report says how many deploys were matched by their
commit.

**The merge queue.** A merge queue runs CI on a commit of its own, on a branch
named after the pull request. Those runs are a CI round of that change, next to
the rounds of its own commits. The time they run counts for `ci`, not for
`merge`.

**The summary.** `--summary` writes the same report with every repository name,
title and link left out. What is left is station numbers.

## What is generated

The files under [reference/](reference/) are generated from the Go definitions.
Do not edit them by hand.

```sh
go run ./cmd/factorymeter contract write docs/reference
```

A test compares the files in the repository with what the code generates, so a
definition and its documentation cannot drift apart. CI fails when they differ,
and the failure prints the command above.

To read the contract without the repository:

```sh
factorymeter metrics                  # every metric id, unit and name
factorymeter metrics review           # one station, with definitions
factorymeter metrics review.wait.p50  # one metric
factorymeter metrics --format json    # the registry for a program
factorymeter contract schema          # the JSON Schema of the report
```

## Adding a metric

1. Add it to the registry in `pkg/line/catalog.go`. The registry test checks
   the id, the unit, the station and the definition.
2. Run `go run ./cmd/factorymeter contract write docs/reference`.
3. Add the function that computes it from records, with a test whose expected
   value is worked out by hand.

A metric in the registry with nothing that computes it is reported as not
measured. It is never reported as zero.

## What is built

| Part | State |
|---|---|
| Stations, metric registry, record types, report types, generated reference, JSON Schema | Built |
| Store and incremental sync: `factorymeter sync`, [store.md](store.md) | Built |
| `factorymeter report`: `factorymeter/v1` from the store, by station, with weekly history | Built |
| Recommendations: rules with stable ids, sized against the line's own best four weeks, [reference/rules.md](reference/rules.md) | Built |
| A local page: `factorymeter serve`, and the same page as one file: `factorymeter report --html` | Built |
| Tickets from GitHub Issues, the links from changes to the tickets they close, and the rules of the spec station | Built |
| CI and release depth: failing workflows by name, merge queue runs, deploys matched by commit | Built |
| The store kept between runs of the GitHub Action, [store.md](store.md#the-store-in-the-github-action) | Built |
| Tickets from Linear: `factorymeter sync --linear`, [store.md](store.md#tickets-from-linear). Checked against Linear's schema and a stand-in server, not yet against a live workspace | Built |
| Tickets from Jira | Next |

`snapshot` and `trend` are the first generation. They read GitHub on every run,
and [metrics.md](metrics.md) describes what they print.
