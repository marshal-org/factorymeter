# The store and the sync

`factorymeter sync` reads a line's history into a store on your machine. Every
later sync reads only what changed. `factorymeter report` reads the store and
nothing else, so a report never has to read the whole history from GitHub
again.

```sh
factorymeter sync owner/repo          # one repository
factorymeter sync my-org              # every active repository of an owner
factorymeter store info my-org        # what the store holds
factorymeter store path my-org        # where it is
```

`snapshot` and `trend` do not read the store. They read GitHub on every run.
See [What is built](contract.md#what-is-built).

## What a sync reads

| Record | From | How far back |
|---|---|---|
| Changes | Pull requests with their commits, reviews and draft events | Every pull request updated since the floor, and every one still open |
| CI runs | GitHub Actions workflow runs, with the workflow and the branch of each | Runs created since the floor |
| Tickets | Issues | Every issue updated since the floor, and every one still open |
| Links | The issues each pull request closes, as GitHub lists them | With the pull requests |
| Deploys | Deployments with their statuses, and published releases | Created since the floor |

The floor is `--since`, 26 weeks by default. It is cut to a whole day. A later
sync with an earlier `--since` reads the missing part. A later `--since` never
moves the floor forward: the store keeps what it has.

The records are the ones in [reference/records.md](reference/records.md).
Nothing is classified or computed at sync time, so a new signature or a new
metric works on history that is already stored.

## What a later sync costs

A sync of a repository where nothing happened makes about six requests and
writes no file.

- **Pull requests.** They are read newest update first, down to the newest
  update the store has seen. An edited, reviewed, merged or closed pull request
  has a new update time, so it is read again and replaces the stored one.
- **Issues.** They are read oldest update first, from the newest update the
  store has seen. An issue that was edited, labelled, closed or opened again
  has a new update time, so it is read again and replaces the stored one.
- **CI runs.** The last three days are read again, because a run that was in
  progress has finished since. Older runs are not read again. The runs of
  pull requests take three requests: one for each of the two pull request
  events, and one for the merge queue.
- **Deploys.** The last seven days of deployments are read again, for the same
  reason. Releases are few and are read in full.

## Tickets and links

On GitHub a ticket is an issue. A link ties a pull request to an issue it
closes: a closing word in the description, or a link made by hand on GitHub.
The link is what lets the report say when work on a ticket started.

A store written by a version that did not read links gets its window read once
more by the first sync that does. That sync compares the pull requests with
what is stored and writes the links. It happens once.

`--skip-tickets` reads no issues and no links. A token that cannot read issues
needs it, because GitHub may refuse a page of pull requests that asks which
issues they close. A later sync without the flag reads the window once more
for the links, as above.

## A store from an older version

A record gains a field now and then. A file written before the field has no
such column, and it still reads: the field is empty. The record layout version
goes up only when a field goes away or comes to mean something else.

An empty field is filled by reading once more. The first sync by a version
that knows more does it, and says so:

- **The commit each merge left.** The window of pull requests is read once
  more, as for links. The merged ones gain their merge commit, which is what
  matches a production deploy to the changes it holds.
- **The runs of the merge queue.** They are read over the range of runs the
  store holds, in one read. A repository with no merge queue costs one
  request.

Each happens once.

## A sync can stop at any time

A sync saves its place every few pages. Stop it with Ctrl-C, or let a CI job
time out, and the next sync goes on from the saved place. It does not start
over. `store info` shows `not finished` for a repository whose first sync has
not reached the floor yet.

When GitHub times out on a page of large pull requests, the sync halves the
page and tries again.

With an owner, repositories are read `--workers` at a time. One repository
that cannot be read does not stop the others. The command names it and exits
with an error.

## Limits

- **Pull requests with more than 100 commits.** Only the first 100 commits are
  read. The store notes it on the repository.
- **Very busy CI.** One sync reads at most `--max-runs` workflow runs per
  repository, 100,000 by default. When it stops there it says so, and the next
  sync reads on.
- **Which runs are CI.** The sync reads the runs of pull request events. When
  none of them ran on a commit of a stored pull request, it reads the runs of
  every event and keeps those, because that repository builds on push.
- **The merge queue.** A run of the merge queue is tied to the pull request
  its branch names, `gh-readonly-queue/<base>/pr-<number>-<commit>`. A queue
  that names its branches another way is not recognized.
- **Restarted runs.** A run that is started again keeps its number and gets a
  new attempt. An attempt is stored when a sync saw it. An attempt that came and
  went between two syncs is not.
- **Releases.** Drafts and prereleases are not deploys. A release counts as a
  production deploy at the time it was published.
- **Links.** At most 20 issues are read per pull request. A link that is later
  taken off on GitHub stays in the store. A link to an issue in a repository
  that is not synced is stored, and the ticket it points at is not.
- **Issues.** The type of an issue is not read. A closed issue is done, or
  cancelled when GitHub says it was closed as not planned or as a duplicate.
- **Deleted pull requests and deleted issues** stay in the store.

## The layout

A store is a directory. It holds one owner.

```
store.json                                     what the store holds
changes/github/<owner>__<repo>/2026-11.parquet
ci_runs/github/<owner>__<repo>/2026-11.parquet
deploys/github/<owner>__<repo>/2026-11.parquet
tickets/github/<owner>__<repo>/2026-11.parquet
links/github/<owner>__<repo>/all.parquet
```

A record lives in the file of the month it was created in, and never moves.
Links have no time of their own and share one file. A
sync rewrites only the months it changed, and each file is replaced in one
step, so a crash leaves the old file or the new one.

The store lives in the user cache directory, under `factorymeter/<owner>`.
`--store` or `FACTORYMETER_STORE` names another place. Only one sync can write
to a store at a time.

The files are plain Parquet with the column names of the record layout. Any
tool that reads Parquet can query them. With DuckDB:

```sql
select state, count(*) from 'changes/*/*/*.parquet' group by 1;
```

factorymeter itself needs no database. The binary reads and writes the files.

## What stays on your machine

Everything. A sync talks to the GitHub API with your read-only token and writes
to the store directory. A store of a private repository holds its pull request
titles, bodies, commit authors, branch names and issue titles, so treat the directory like
the repository itself.
