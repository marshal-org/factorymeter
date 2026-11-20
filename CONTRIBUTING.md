# Contributing to factorymeter

Thank you for helping. This page says how to get a change merged.

## Ways to contribute

- **Add or fix an agent signature.** This is the most useful contribution. See below.
- **Report a wrong number.** Open an issue with the command you ran and the pull request that
  was counted wrong. A link to a public pull request is the best bug report.
- **Fix a bug or improve the report.** For anything larger than a small fix, open an issue first
  so we can agree on the shape.
- **Report a security problem privately.** See [SECURITY.md](SECURITY.md). Do not open a public
  issue.

## Getting set up

You need Go (the version in `go.mod`) and a GitHub token that can read public repositories. The
GitHub CLI's login is enough.

```bash
git clone https://github.com/marshal-org/factorymeter
cd factorymeter
go build -o factorymeter ./cmd/factorymeter
./factorymeter sync marshal-org/runlease
./factorymeter report marshal-org/runlease
```

The unit tests need no token and no network. The live tests run the binary against a real
GitHub repository, Linear workspace and Jira Cloud site, and are run by hand:
[test/live/README.md](test/live/README.md).

## Before you open a pull request

Run what CI runs:

```bash
gofmt -l .                      # must print nothing
go vet ./...
go test -count=1 ./...
go run ./cmd/factorymeter signatures validate
scripts/check-dashes.sh         # no em dashes or en dashes anywhere
```

Then:

- **Write tests.** New behaviour needs a test. The tests run against `httptest` servers and
  recorded data, never against the live API.
- **Say what you ran.** If your change affects a number, say in the pull request which
  repository you ran it on and what the report printed before and after.
- **Keep it small.** One logical change per pull request.
- **Plain words.** Short sentences in code comments, messages and docs. No em dashes.

## Add or change a signature

A signature is one way to tell that an agent wrote a pull request. All of them live in
[`signatures/registry.yaml`](signatures/registry.yaml), and the top of that file explains the
kinds, the matchers and the confidence levels.

Every signature needs a real, public pull request that it matches. A test fails without one.

1. Find a public pull request the agent wrote. Read it yourself.
2. Add the signature to `signatures/registry.yaml`. Set `verified` to today's date.
3. Record the pull request as a fixture:

   ```bash
   go run ./cmd/factorymeter classify owner/repo#123 \
     --record pkg/signature/testdata/prs --note "what this pull request proves"
   ```

   The fixture keeps only what classification needs. It drops the title, reviews, commit author
   names and personal emails.
4. Open the fixture and check the `expect` block. It states the right answer, so it must be
   what you saw with your own eyes, not only what the tool printed.
5. Run `go test ./pkg/signature/`.

Pick the confidence honestly:

- **high**: the agent's own account opened the pull request or wrote a commit.
- **medium**: a disclosure the tool writes by default, such as a trailer, a footer or a badge.
- **low**: a naming habit, such as a branch prefix. A low match alone never counts.

A fixture of a human pull request that must stay human is welcome too. False positives are
worse than misses here.

## Commit messages

We use [Conventional Commits](https://www.conventionalcommits.org/). The type decides the next
version number, so choose it honestly.

```
feat(signatures): detect a new agent by its commit trailer
fix(metrics): do not count a bot review as a first review
docs(readme): explain the token permissions
```

Types: `feat`, `fix`, `docs`, `refactor`, `test`, `chore`, `ci`. A breaking change takes a `!`
after the type and a `BREAKING CHANGE:` footer. Do not edit `CHANGELOG.md` or push tags. The
release tooling does both.

## Sign your work

Every commit needs a `Signed-off-by` line. It certifies the
[Developer Certificate of Origin](https://developercertificate.org/): that you wrote the change or
have the right to submit it under the project's license.

```bash
git commit -s -m "fix(report): keep table columns aligned"
```

There is no contributor license agreement to sign.

## Rules that hold for every change

1. **Read only.** factorymeter never writes to GitHub. The client refuses GraphQL mutations and
   sends only `GET` requests to the REST API. Do not add a write.
2. **No telemetry.** Nothing is sent anywhere except to the GitHub API.
3. **The token stays in memory.** Never print it, log it or write it to a file.
4. **Reports hold numbers, not people.** The report does not name authors or reviewers.
5. **Say what you cannot see.** A number that is a floor, or a guess, is labelled as one.

## Review

A maintainer reviews every pull request. Changes to `signatures/`, `pkg/github/` and `.github/`
get a slower, more careful read.

The first workflow run for a new contributor waits for a maintainer to approve it. That is a
safety setting, not a judgement on your change.

## Conduct

Be kind and assume good faith. The [Code of Conduct](CODE_OF_CONDUCT.md) applies everywhere the
project does.
