# Task

Source: https://github.com/marshal-org/factorymeter/issues/50

## Body
## Goal

Make a `Line` buildable from frozen per-repo, per-week **segments**, so a host that reports often (Marshal's Factory Lens) classifies and decodes each record once instead of on every report.

## Why

A host that recomputes a 26-week report loads and classifies the whole history each time. Measured on a 6-repo line with 2,651 changes and 19k reviews, the load is about 6s and the report is 0.18s. Load grows linearly with history, so it dominates at hundreds of repos.

## Shape (approved)

- `Line.Segment(repo, week) ([]byte, error)` serializes the `obs`, `stays`, items, agents, repo info, flow runs and `seen` of one repo-week in a versioned, compressed encoding. The engine version and registry version go in the header.
- `measure.Merge(segments ...[]byte) (*Line, error)` remaps item, agent and repo indexes and appends. `Report` on a merged line is unchanged.
- Review observations stay tagged human or agent, so `HumanReviewsOnly` applies at merge or report time and never needs a rebuild.
- Cross-week joins (a deploy shipping a change from an earlier week, CI runs before the merge week) are kept in the segment of the change's week. The host's freeze rule decides when a week is final. See the Marshal issue.

## Done when

- A parity test shows that `Report(Merge(segments of all weeks))` is byte-equal to `Report(LoadFrom(all records))` over the synthetic generator below and the existing goldens, in both review modes.
- A synthetic generator in `testdata` (300 repos, 1,500 changes a week, 26 weeks, realistic reviews, commits, CI runs and deploys) is reusable by hosts.
- Benchmarks: merging 300×26 segments and reporting finishes under 10s and 1 GB RSS.

## Comments
(none)

## Operator instructions (2026-11-25, host-specific details left out because this repo is public)
Design is approved; do not re-open it. Decisions: segments hold engine obs/stays/items/agents/repo info/flow runs per (repo, week), with a versioned header carrying the engine and registry version; `measure.Merge` remaps indexes; review observations keep the human/agent tag so `HumanReviewsOnly` needs no rebuild. The gate that matters: a parity test where `Report(Merge(all segments))` is byte-equal to `Report(LoadFrom(all records))` over the goldens and the new synthetic generator (300 repos, 1,500 changes/week, 26 weeks, realistic reviews/commits/CI/deploys), in both review modes, plus the merge benchmark (300x26 segments merge+report under 10s and 1 GB RSS). The repo is public: no customer names, no host internals in code, docs or commits; follow CONTRIBUTING (DCO sign-off, no AI bylines). Stop at the merge gate; release follows the repo's release-please flow after merge.

## Proposed flow
**full** — a new public API (`Line.Segment`, `measure.Merge`), a new versioned binary encoding, and a byte-equality parity contract: the encoding and index remapping are the design-sensitive part, even though the shape is approved.
Confidence: high — new public API and file format point clearly to full; the operator already approved the design, so the brief gate is expected to be a short confirm of the issue's decisions.
Skipped: nothing.
