# Live tests

The live tests run factorymeter against real systems, the way a person does: they build the
binary, write a `factorymeter.yml`, run `sync`, and read the JSON of `store tickets` and `report`.
They are not part of `go test ./...` or of CI. Run them before a release that touches Linear,
Jira, the install script or `upgrade`, or when a ticket system changes its API.

```bash
cp test/live/env.example .env.live   # fill it in. git ignores it
scripts/live-test.sh                 # every live test, about 4 minutes the first time, 1 after
scripts/live-test.sh -run Releases   # only the install script and upgrade
```

## What they need

| Setting | What |
|---|---|
| `FM_LIVE_REPO` | a GitHub repository kept for testing. The run opens and merges pull requests in it |
| `GITHUB_TOKEN` | a token that can write to it. Empty takes `gh auth token` |
| `LINEAR_API_KEY` | a key of a Linear workspace kept for testing, that can make issues |
| `FM_LIVE_LINEAR_TEAM`, `FM_LIVE_LINEAR_OTHER_TEAM` | two of its teams. Tickets are made in the first, and one moves to the second |
| `FM_LIVE_JIRA_SITE`, `JIRA_EMAIL`, `JIRA_API_TOKEN` | a Jira Cloud site kept for testing, and an account with a token without scopes that can make issues |
| `FM_LIVE_JIRA_PROJECT` | a software project of the site |

factorymeter itself only reads. The tests write: they make the tickets and pull requests they
check. A setting that is missing skips the tests that need it, and says which.

## What they make

The first run makes one set of tickets and pull requests, titled and branched `fm-live`. Later
runs find them and check them again, so they write only one move a run to each system, to see an
update arrive. `FM_LIVE_RUN=fm-live-2` makes a new set under that name, to see the first sync of
fresh tickets.

| Linear | Jira | Pull requests |
|---|---|---|
| a key in a branch, a ticket opened again, one cancelled, a duplicate, one in the trash, one done with no start, one moved to another team, one only attached, one with a history of 25 moves, one in an open pull request | a key in a title, a ticket opened again, a subtask, an epic, one done with no start, one in an open pull request | a key in the branch, in the title, in the description, in lower case, by the old key of a moved ticket, look-alikes (`utf-8`, `xKAN-2`, `KAN-2x`, a number that is not there), and one with no key that Linear has attached |

## What they check

- **Links.** Each ticket is tied to the pull request it should be, and how: attached, or by key.
  No ticket is tied to the pull request of look-alikes. The epic is not a ticket.
- **States and times.** Opened again counts once, and the done time is the last one, also past
  the first page of a long history. A pull request opened after a ticket closed does not start
  it. A moved ticket keeps its old key.
- **The report.** Coverage lists both systems with the keys it searches for, and the report of
  the repository holds only the tickets tied to its pull requests.
- **An update.** A second sync reads the ticket that moved in each system, and only that.
- **Errors.** A wrong Linear key, a wrong Jira token, a team, project or site that is not there,
  and a site name without its domain each fail fast, say what to do, and never show a secret.
- **Releases.** The install script installs the newest release from github.com, and `upgrade`
  takes a build that calls itself v0.0.1 to the newest release in place.
