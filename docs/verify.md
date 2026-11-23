# The verify station and test machines

The verify station measures the proof that a change works on a running environment before it
merges. factorymeter reads that proof from GitHub deployments: a deployment of a commit of the
pull request, to an environment that is not production, made before the merge.

Preview environments (Vercel, Netlify, Render, a preview per pull request) usually create these
deployments already. A test machine you start yourself, such as a VM per pull request, a
throwaway cluster or a spot machine, usually does not. Then the station says it is not measured,
even when every change was tested.

## Record a deployment per round

Make the script that deploys the change to the test machine record each deploy as a GitHub
deployment. Two calls are enough.

When the change goes onto the machine:

```bash
SHA=$(git rev-parse HEAD)
ID=$(gh api -X POST repos/OWNER/REPO/deployments --jq .id --input - <<EOF
{"ref":"$SHA","environment":"test-machine","auto_merge":false,"required_contexts":[],
 "transient_environment":true,"production_environment":false}
EOF
)
gh api -X POST repos/OWNER/REPO/deployments/$ID/statuses -f state=in_progress
```

When the tests on it are done:

```bash
gh api -X POST repos/OWNER/REPO/deployments/$ID/statuses -f state=success   # or failure
```

When the machine is torn down, `-f state=inactive` marks it gone. A deployment that never got a
result is counted as a wait and not as a pass or a fail.

Details that matter:

- **`ref` is the full SHA.** The deployment then names exactly the commit under test, and
  factorymeter joins it to the pull request by that commit.
- **`auto_merge` is false.** Otherwise GitHub merges the default branch into the branch before
  it deploys.
- **`required_contexts` is empty.** Otherwise GitHub refuses the deployment while CI is still
  running on the commit.
- **The environment name must not contain `prod`, `production` or `live`.** Those names count as
  production, and a deployment to them is a release, not a proof.
- **One deployment per round.** Each push that is tested again gets its own deployment, also on
  the same machine. Do not reuse one deployment for the next round.
- **The token needs `deployments: write`** on the repository. factorymeter itself only reads.

## What the station counts

For each deployment of a change:

| Number | From | To |
|---|---|---|
| `verify.wait` | the commit being written | the deployment being created |
| `verify.work` | the deployment being created | its first `success` |
| `verify.rework` | | the share of finished deployments that reported `failure` or `error` |
| `verify.flow` | | each change once, at its first deployment that passed |

So a change that fails two rounds and passes the third counts as one change verified, with two
rounds of rework. A deployment made after the merge, a deployment of the branch the change merges
into, and a GitHub Pages site are not proofs and are left out. The full definitions are in
[the metric reference](reference/metrics.md).

## Why not commit statuses

Many test setups already post a commit status or a check with the result. factorymeter does not
read those for verify. A status is a verdict that its writer can post at any time and in any
order. It does not say when the change went onto a machine, so the time a round took cannot be
measured from it. One verdict can also cover tests that ran on no machine at all. A deployment
has a fixed life (created, in progress, then success or failure), so its times mean the same in
every repository.
