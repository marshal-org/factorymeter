# Tasks: Mergeable per-repo, per-week segments for Line

**Input**: [spec.md](spec.md) · [plan.md](plan.md)

**MVP**: Phase 3 (US1). Finish it, prove it on its own, then continue.

## Phase 1: Setup

**Purpose**: the dependency every story needs

- [x] T001 FR-008 G-015: make the zstd package a direct dependency and keep the module files tidy (go.mod, go.sum)

## Phase 2: Foundational

**Purpose**: the line changes that make parity possible. Every existing test stays green after each task.

- [x] T002 FR-013 G-013: record the current marshalled reports and drills of every existing scenario as a guard test, so any change from the work below is seen and judged (pkg/measure/segment_guard_test.go)
- [x] T003 FR-006 G-006: give every item the moment and the repository that place it in a week, a change at its creation and a deploy at its finish (pkg/measure/line.go)
- [x] T004 FR-004 G-003: add the report-time view for the review mode, route every raw read of a review stream through it (the report, the advice, the limits text), keep the human-only builder switch as a way to pick the view, and keep the parts cache per view. All observations are still "both", so nothing changes yet (pkg/measure/report.go, pkg/measure/advise.go, pkg/measure/line.go, pkg/measure/load.go, pkg/measure/agg.go)
- [x] T005 FR-005 G-003: emit the review and merge stations in both modes for a change with an agent review, and tag only what differs (pkg/measure/line.go, pkg/measure/agg.go)
- [x] T006 FR-007 G-005: sort the builder inputs of a repository into a total order before classifying, and build the first-wins and last-wins maps over that order (pkg/measure/line.go)
- [x] T007 FR-007 G-005: write the canonical pass that normalises times to UTC, sorts and renumbers repositories, agents, items and flows, and totally orders every stream, and end the builder in it. List each existing report that changes, and why (pkg/measure/canonical.go, pkg/measure/line.go)
- [x] T008 FR-011 G-006: keep coverage and sources by repository, and the repository figures as parts attributed to the week of the thing they count (pkg/measure/line.go, pkg/measure/report.go)
- [x] T009 FR-008 G-007: add the signature digest to the registry and keep the digest of the builder's registry on the line, and add the engine version constant (pkg/signature/registry.go, pkg/measure/line.go, pkg/measure/engine.go)

**Checkpoint**: no story work starts until this phase is done and G-013 passes.

## Phase 3: US1 - Report from merged segments gives the same report as a full load (P1, MVP)

**Goal**: cut, store and merge segments with exact parity.

**Independent Test**: G-001, G-004, G-005, G-006.

- [x] T010 [P] [US1] G-001: write TestSegmentParityScenarios, which cuts every segment of each existing scenario without its tickets, merges them and compares the marshalled report and drills with the full load of the same records (pkg/measure/segment_parity_test.go)
- [x] T011 [P] [US1] G-004: write TestMergeOrderChangesNothing, with shuffled segment orders (pkg/measure/segment_order_test.go)
- [x] T012 [P] [US1] G-005: write TestTiesAreTotal, with equal timestamps built by hand in reviews, runs, deploys, items and changes, loaded and merged in different orders (pkg/measure/segment_ties_test.go)
- [x] T013 [P] [US1] G-006: write TestSegmentWeeksAndFigures: creation week, deploy week, a change that spans weeks, any instant in a week, empty weeks, a repository with no data, a repeated week refused, per-repository coverage on a subset merge, a line with tickets cut without them (pkg/measure/segment_weeks_test.go)
- [x] T014 [US1] G-001: write the segment encoding and decoding, with the header, the string table, exact nanosecond times, a fixed zstd level and local indexes (pkg/measure/segment_codec.go)
- [x] T015 [US1] FR-001 FR-006 G-006: add Weeks, which lists the weeks a repository has data in, and Segment, which cuts one repository and one week and partitions the line without overlap or loss (pkg/measure/segment.go)
- [x] T016 [US1] FR-002 FR-003 G-001: add Merge, which unions the tables, remaps indexes, adds the figures, checks the repository facts and ends in the canonical pass (pkg/measure/merge.go)

**Checkpoint**: US1 works alone and passes G-001, G-004, G-005 and G-006.

## Phase 4: US2 - One set of segments serves both review modes (P1)

**Goal**: pick the review mode at report time from merged segments.

**Independent Test**: G-003, G-002.

- [x] T017 [US2] G-003: write TestSegmentReviewModes with a change reviewed only by an agent, in both modes, from one merged line (pkg/measure/segment_modes_test.go)
- [x] T018 [US2] FR-004 FR-005 G-003: make the parity tests run in both modes, and make the merged line expose the view (pkg/measure/merge.go, pkg/measure/segment_parity_test.go)

**Checkpoint**: US1 and US2 pass their rows.

## Phase 5: US3 - Segments are safe to store and to merge (P2)

**Goal**: refusals, bounds and stable bytes.

**Independent Test**: G-007, G-008, G-009.

- [x] T019 [P] [US3] G-007: write TestSegmentVersionsAndRefusals for mixed format, engine and digest, an empty list, a repeated repository and week in any case, and disagreeing repository facts (pkg/measure/segment_refuse_test.go)
- [x] T020 [P] [US3] G-008: write TestSegmentHostileBytes with truncations, flipped bytes, huge counts, out of range indexes and a compression bomb, asserting an error, no panic and bounded allocation (pkg/measure/segment_hostile_test.go)
- [x] T021 [P] [US3] G-009: write TestSegmentBytesAreStable, cutting twice (pkg/measure/segment_codec_test.go)
- [x] T022 [US3] FR-009 FR-010 G-007 G-008: add the version checks, the count and index checks and the decompressed size caps (pkg/measure/segment_codec.go, pkg/measure/merge.go)

**Checkpoint**: US1 to US3 pass their rows.

## Phase 6: US4 - A host can test at scale with a synthetic organization (P3)

**Goal**: the generator, the scale gate and the benchmark. Tests that use the generator are in the external test package.

**Independent Test**: G-010, G-011, G-012, G-002.

- [x] T023 [P] [US4] G-010: write the generator tests: same seed same records, different seed different records, valid for the source interface, the shape of reviews, commits, runs and deploys (pkg/synth/synth_test.go)
- [x] T024 [US4] FR-012 G-010: write the seeded generator as a source of records, sized by repositories, changes per week and weeks (pkg/synth/synth.go)
- [x] T025 [US4] G-002: write TestSegmentParitySynth at a small size on every run, in both modes (pkg/measure/segment_synth_test.go)
- [x] T026 [US4] SC-001 SC-002 G-011: write TestMergeScaleGate, which cuts the segments of 300 repositories by 26 weeks, merges them, reads a report, asserts the time and the peak memory, prints the largest segment, then checks parity with the full load (pkg/measure/segment_scale_test.go)
- [x] T027 [P] [US4] G-012: write BenchmarkMergeReport (pkg/measure/segment_bench_test.go)

**Checkpoint**: all stories pass their rows.

## Phase 7: Polish

**Purpose**: docs, hygiene and the follow-up

- [x] T028 [P] FR-014 G-014: write the host guide, with how to cut, store and merge, the cut contract, how often to cut a week again, what parity covers and the privacy of segment bytes (docs/segments.md, README.md)
- [x] T029 [P] FR-014 G-014: write the doc comments of the new API (pkg/measure/segment.go, pkg/measure/merge.go)
- [x] T030 G-015 G-013: run gofmt, vet, the tidy check and the dash check, and put the mutation checks of the guards and the list of changed existing orders in the pull request body
- [x] T031 FR-014: open the follow-up issue for ticket segments and link it from the pull request

## Dependencies

- T014 to T016 need Phase 2.
- T025 to T027 need T024.
- T022 changes the files of T014 and T016, so it runs after them.
- T004 comes before T005, because tagged observations must be read through the view first.
