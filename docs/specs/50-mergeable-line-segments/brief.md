# Brief: mergeable per-repo, per-week segments for Line

Source: [issue #50](https://github.com/marshal-org/factorymeter/issues/50). The design in the issue is approved and is not reopened. This brief settles the six places where it is silent.

## Grounding
- Review mode is applied while the line is built. `emit` filters the counted reviews when `humanReviewsOnly` is set (`pkg/measure/line.go`). Review observations carry no human or agent tag, so a built line cannot switch mode.
- The streams that change with the mode are review wait, review queue stays, review work, rework, rounds, held and skipped, and the merge queue stays, wait and held. The shares of who reviewed do not change.
- `LoadFrom` reads ticket sources first, then repositories in scope order, then ticket projects (`pkg/measure/load.go`). Item, agent, repo and flow indexes are handed out on first use.
- Observations are stable-sorted by time only. Stays are stable-sorted by start time when read. Equal times keep emit order, so the oldest items, drill-down order and notes order can depend on load order.
- Per-repo counters are whole-history values, not weekly sums: `ciWait` is a threshold over all first runs, `ships` and `shipsByCommit` come from all production deploys, `environments` is a set.
- A ticket's observations depend on the first change that links it, in any repo and any week (`Builder.linked`).
- There is no calendar-week rule in the report. It uses rolling 7-day windows ending at `until`.
- There are no golden files and no benchmarks in `pkg/measure`. The existing goldens are the table-driven scenarios in `measure_test.go` and `load_test.go`. `TestTheOrderOfRecordsChangesNothing` already compares the marshalled report for record order.
- `github.com/klauspost/compress` is already in the module graph (indirect), so zstd needs no new module.
- Go does not let another module import a `testdata` directory.

## Approaches
- A. Freeze the built observations per (repo, week) and merge by remapping indexes. This is the approved design.
- B. Freeze the filtered raw records and rebuild on merge. It saves nothing, because classification is the cost.
- C. Cache the whole report per window. It does not help a host that reports over many windows and lines.

## Round 1
### Q1: How does the human-only review mode work when the line comes from segments?
- A. Tag at build, choose at report time
- B. Keep review inputs, derive at merge
- C. Two segment sets, one per mode
- **Recommended: A.** One segment serves both modes, and LoadFrom shares the path, so parity does not depend on two code paths agreeing.

### Q2: Which week does a change belong to?
- A. Week the change was created
- B. Merge week, creation week while open
- C. Week of last activity
- **Recommended: A.** One key that never moves. This is the closest call: B puts most activity in the current week at the cost of a key that changes once.

### Q3: Are tickets part of the segments?
- A. Out of scope for now, with a follow-up issue
- B. In scope: ticket segments
- **Recommended: A.** Tickets join across repos and weeks and are not in the approved shape or the generator.

### Q4: Does parity hold for any input, including exact timestamp ties?
- A. Make tie-breaks total in both paths
- B. Parity except exact ties
- **Recommended: A.** A few lines remove the asterisk. LoadFrom can change only where the order was arbitrary.

### Q5: What encoding and compression does a segment use?
- A. Custom binary, zstd
- B. Custom binary, stdlib flate
- C. gob or JSON, zstd
- **Recommended: A.** Fast to decode and small. zstd is already in the module graph.

### Q6: Where does the synthetic generator live?
- A. Importable package `pkg/synth`
- B. Files in `testdata`
- **Recommended: A.** Hosts can import it. A `testdata` directory cannot be imported.

### Answers
`Q1:A  Q2:A  Q3:A  Q4:A  Q5:A  Q6:A` (the operator chose "Approve, all recommended")

### Deltas
- None. Every answer matches the recommendation.

### Decisions
- Review observations are tagged at build with the mode they count in. The mode is a report-time switch on the Line. `Builder.HumanReviewsOnly()` stays as a shortcut.
- A change belongs to the UTC Monday week of its creation time. `Segment` takes any instant in the week.
- Tickets are out of scope. A merged line has no ticket sources. A follow-up issue covers ticket segments.
- Tie-breaks are total in LoadFrom and Merge alike, so parity holds for any input.
- A segment is a custom binary encoding compressed with zstd. The compress module becomes a direct dependency.
- The generator is an importable package, `pkg/synth`, that implements `measure.Source`.
- Merge refuses segments of different engine or registry versions.

## Alignment
Questions accepted as recommended: 6 of 6 (100%). Changed: 0. Risky questions changed: none.
