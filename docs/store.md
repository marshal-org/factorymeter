# The store and the sync

`factorymeter sync` reads a line's history into a store on your machine. Every
later sync reads only what changed. The store is what the new report will read,
so a report never has to read the whole history from GitHub again.

```sh
factorymeter sync owner/repo          # one repository
factorymeter sync my-org              # every active repository of an owner
factorymeter store info my-org        # what the store holds
factorymeter store path my-org        # where it is
```

`report` and `trend` do not read the store yet. They move over in the next
release. See [What is built](contract.md#what-is-built).

## What a sync reads

| Record | From | How far back |
|---|---|---|
| Changes | Pull requests with their commits, reviews and draft events | Every pull request updated since the floor, and every one still open |
| CI runs | GitHub Actions workflow runs | Runs created since the floor |
| Deploys | Deployments with their statuses, and published releases | Created since the floor |

The floor is `--since`, 26 weeks by default. It is cut to a whole day. A later
sync with an earlier `--since` reads the missing part. A later `--since` never
moves the floor forward: the store keeps what it has.

The records are the ones in [reference/records.md](reference/records.md).
Nothing is classified or computed at sync time, so a new signature or a new
metric works on history that is already stored.

## What a later sync costs

A sync of a repository where nothing happened makes about five requests and
writes no file.

- **Pull requests.** They are read newest update first, down to the newest
  update the store has seen. An edited, reviewed, merged or closed pull request
  has a new update time, so it is read again and replaces the stored one.
- **CI runs.** The last three days are read again, because a run that was in
  progress has finished since. Older runs are not read again.
- **Deploys.** The last seven days of deployments are read again, for the same
  reason. Releases are few and are read in full.

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
- **Restarted runs.** A run that is started again keeps its number and gets a
  new attempt. An attempt is stored when a sync saw it. An attempt that came and
  went between two syncs is not.
- **Releases.** Drafts and prereleases are not deploys. A release counts as a
  production deploy at the time it was published.
- **Deleted pull requests** stay in the store.

## The layout

A store is a directory. It holds one owner.

```
store.json                                     what the store holds
changes/github/<owner>__<repo>/2026-11.parquet
ci_runs/github/<owner>__<repo>/2026-11.parquet
deploys/github/<owner>__<repo>/2026-11.parquet
```

A record lives in the file of the month it was created in, and never moves. A
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
titles, bodies, commit authors and branch names, so treat the directory like
the repository itself.
