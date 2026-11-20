# Tickets: Linear, Jira and GitHub issues

The spec station is where a ticket waits for someone to start on it. factorymeter reads
tickets from GitHub issues always, from a Linear workspace with `sync --linear`, and from a
Jira Cloud site with `sync --jira`. This page says what it reads, how a ticket is tied to a pull
request, and how the numbers are counted.

## How to connect

| | Linear | Jira Cloud |
|---|---|---|
| Turn it on | `--linear`, or `linear:` in the [config file](../README.md#a-config-file) | `--jira acme.atlassian.net`, or `jira: {site: ...}` in the config file |
| Credentials | `LINEAR_API_KEY`: a personal API key, with the Read permission only | `JIRA_EMAIL` and `JIRA_API_TOKEN`: an account and an API token **without scopes** |
| Where to make it | Linear, Settings, Security and access, Personal API keys | id.atlassian.com, Security, API tokens, Create API token |
| Limit the read | `--linear-team ENG` or `linear: {teams: [ENG]}` | `--jira-project ENG` or `jira: {projects: [ENG]}` |
| What is read | the workspace the key belongs to | every project the account can browse |

Credentials only ever come from the environment. There is no flag for them, the config file
refuses them, and they are never printed or written to the store. A scoped Jira token does not
work: it only works through Atlassian's gateway, and factorymeter asks your site itself. A token
without scopes can do whatever its account can, so for a token that can only read, make it for an
account that only has Browse projects.

Both clients only read. The Linear client sends GraphQL queries and refuses a mutation. The Jira
client has one verb, `GET`, sends the account to the named host only, and follows no redirect.

## What is read

**Linear** (`pkg/ingest/queries/linear_issues.graphql`): for each issue its `id`, `identifier`,
`previousIdentifiers`, `title`, `url`, `createdAt`, `updatedAt`, `startedAt`, `completedAt`,
`canceledAt`, `archivedAt`, `trashed`, its state (name and type), its team key, its creator's
display name and whether the creator is an app, the bot that made it, its first 10 labels, the
first 10 attachments whose URL holds `/pull/`, and its state history. The history is read in
full: the issues query carries the first 20 moves, and the rest is read a page at a time.

**Jira** (`/rest/api/3/search/jql`): `summary`, `status`, `issuetype`, `project`, `creator`,
`labels`, `created`, `updated`, `resolution` and `resolutiondate`, and the changelog. Of the
changelog only the moves of `status` and `key` are used. When the search carries part of an
issue's changelog, the rest is read from the issue.

Not read: descriptions, comments, assignees, estimates, sprints, and email addresses. Jira can
send an email next to a name. It is not decoded.

## How a sync reads

A sync reads tickets by the time they were last updated, a stretch at a time: 7 days for Linear,
14 for Jira. The place it reached is saved after each stretch, so a sync that stops goes on from
there. The first sync then reads, once, every open ticket last touched before the history
starts. A later sync reads only what changed, plus 10 minutes of overlap.

A Jira search reads a time in the time zone of the account, which a client cannot see. So each
stretch of Jira is asked for in whole days, a day wider before and two after, and what comes
back beyond the stretch is stored as it is.

When the list of teams or projects changes, the tickets of that system are dropped and read
again, so a ticket of a team that is no longer read does not stay open in the store for good.

## How a pull request is tied to a ticket

1. **Attached by Linear.** Linear's GitHub integration attaches the pull requests of an issue to
   it. Every attachment that is a pull request URL ties that pull request to the issue.
2. **Named by key.** The branch, title and description of every pull request are searched for
   the key of a ticket, such as `ENG-12`. This is the only tie for Jira, which does not say which
   pull requests belong to an issue.

The search needs no list of prefixes. After a sync, factorymeter knows the key of every ticket
in the store, and searches only for keys with those prefixes. So `utf-8` is a key only where a
team is called UTF. `sync` prints the prefixes it found, and the Coverage part of the report
lists them.

A key counts when it stands on its own:

| Text | Ties to |
|---|---|
| `ofir/mar-12-fix-login` | MAR-12 |
| `Fix the login (MAR-12)` | MAR-12 |
| `feat/MAR-12_and_more` | MAR-12 |
| `MAR-12x`, `xMAR-12`, `MAR-123` | not MAR-12 |

Case does not matter. A ticket that moved to another team or project is also found by the key it
had before, from Linear's `previousIdentifiers` and Jira's history of `key`. When an old key now
belongs to another ticket, the ticket that has it now wins.

A key in a commit message or a comment is not read.

## How a ticket's states map

| factorymeter | Linear state type | Jira status category |
|---|---|---|
| backlog | triage, backlog | To Do with "backlog" in the status name |
| unstarted | unstarted | To Do |
| started | started | In Progress |
| done | completed | Done |
| cancelled | canceled, duplicate, or in the trash | Done with a resolution or status such as Won't Do, Duplicate, Declined or Cancelled |

A Jira epic, an issue above the level of a story or a task, is left out: it stays open while its
children are worked on. Subtasks count.

## The numbers of the spec station

**Start.** A ticket starts when it first moves to a started state, or when the first pull
request tied to it is opened, whichever is earlier. A ticket written up after the work began is
taken to start when it was written. A pull request opened after the ticket was closed did not
start it: it only names it, as in "a duplicate of ENG-12".

**Done.** The last time the ticket was moved to done. Linear keeps the first time in
`completedAt`, also after the issue was opened again and done once more, so the state history
decides.

| Number | What it is |
|---|---|
| `spec.flow` | tickets started, per week |
| `spec.wait.p50`, `spec.wait.p90` | hours from created to started, median and 90th percentile |
| `spec.rework` | the share of the tickets finished in the window that had been finished before and were opened again. In Jira, opening again a ticket whose done status is one such as Won't Do does not count |
| `spec.queue` | tickets not started at the end of the window |
| `spec.queue.oldest` | the age of the oldest of them |

A ticket nobody started that was cancelled, such as a duplicate, never stood in the queue. One
that was closed as done without a start left the queue when it was closed.

**Which repository.** A ticket of Linear or Jira counts for the repository of the first pull
request tied to it. The report of one repository counts only the tickets tied to its pull
requests. The report of the owner counts them all, and a ticket with no pull request counts in
the whole line only. Tickets are not split by agent and person.

The rules that read these numbers: `factorymeter rules spec`.

## Checked against

The Linear and Jira adapters were run against a live Linear workspace and a live Jira Cloud site
on 2026-11-20, with tickets in every state above: moved, reopened, cancelled, a duplicate, in the
trash, done without a start, an issue with a 26-move history, epics and a subtask, and pull
requests that name keys in their branch, title and description, in lower case, by an old key,
and words that only look like keys.
