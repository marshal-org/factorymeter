# Spec: Mergeable per-repo, per-week segments for Line

**Branch**: `50-mergeable-line-segments` · **Created**: 2026-11-25 · **Status**: Draft

**Input**: [task.md](task.md) · [brief.md](brief.md)

## Problem Statement

A host that reports on many repositories today rebuilds the whole line from every record each time it wants a report. Classifying records is the expensive part, and it grows with the history of the whole organization, not with what changed. For an organization of a few hundred repositories and half a year of history, the rebuild is too slow and too large to repeat for every report, every window and every viewer.

The host cannot keep a built line either: a built line is one piece and cannot be extended by one repository or one week. It also fixes the review mode at build time, so a host that serves both "all reviews" and "reviews by people only" has to build and keep two lines.

## Solution

The host keeps a small, self-describing segment for each repository and each week, cut once from a built line. To report, it merges the segments it needs and reads the report from the merged line. The merged line gives exactly the same report as loading all the records, in both review modes. Changing the review mode needs no rebuild and no second set of segments. Merging hundreds of repositories over half a year takes seconds, not minutes.

A synthetic organization generator ships with the library so a host, and this project, can test parity and speed at scale without real data.

## User Stories

### US1 - Report from merged segments gives the same report as a full load (Priority: P1)

A host operator has the records of many repositories. They cut one segment for each repository and week, store the bytes, and later merge any set of them into a line and ask for a report. The report is byte for byte the report a full load of the same records gives.

**Why this priority**: this is the whole point. Without exact parity no host can trust a merged line. It also carries the segment format and the merge.

**Independent Test**: build a line from a set of records, cut a segment for every repository and week that holds data, merge them all, and compare the marshalled report with the report of the full load, over the existing scenarios and over the synthetic organization.

**Acceptance Scenarios**:

1. **Given** the records of several repositories over several weeks, **When** every segment is merged, **Then** the report of the merged line equals the report of loading the records.
2. **Given** the same segments, **When** they are merged in any order, **Then** the report is the same.
3. **Given** only the segments of some repositories or some weeks, **When** they are merged, **Then** the report covers exactly those and says nothing about the others.
4. **Given** two records with exactly the same timestamp, **When** the line is loaded or merged, **Then** the order the report shows them in does not depend on the order the records or segments came in.

---

### US2 - One set of segments serves both review modes (Priority: P1)

A host serves some viewers the report with every review counted and others the report that counts only reviews by people. It stores one set of segments and picks the mode when it reports.

**Why this priority**: it removes the need to keep and cut two sets, and it is part of the parity promise.

**Independent Test**: merge the same segments once, read the report in each mode, and compare each with the full load in the same mode.

**Acceptance Scenarios**:

1. **Given** merged segments, **When** the report is read with every review counted, **Then** it equals the full load in that mode.
2. **Given** the same merged line, **When** the review mode is switched to people only, **Then** the report equals the full load in that mode, with no rebuild and no new segment.
3. **Given** a change that only an agent reviewed, **When** the mode is people only, **Then** the change reads as one with no review seen, as it does in a full load.

---

### US3 - Segments are safe to store and to merge (Priority: P2)

A host stores segment bytes for months and across upgrades of the library, and may read bytes it did not write.

**Why this priority**: a stored segment outlives the code that cut it. A wrong merge across versions would be silent and wrong.

**Independent Test**: merge segments of different versions, damaged segments and empty input, and check each is refused with a clear error and never a wrong report or a crash.

**Acceptance Scenarios**:

1. **Given** segments cut by different versions of the engine or with a different list of agent signatures, **When** they are merged, **Then** the merge is refused and the error says which differs.
2. **Given** truncated, corrupted or hostile bytes, **When** they are merged, **Then** the merge returns an error, does not crash and does not allocate without bound.
3. **Given** the same line, **When** a segment is cut twice, **Then** the bytes are identical.

---

### US4 - A host can test at scale with a synthetic organization (Priority: P3)

A host developer or a maintainer wants a realistic organization of hundreds of repositories without real data, to check parity and speed.

**Why this priority**: it is the evidence for the speed promise, and hosts can reuse it. It does not change the product behaviour.

**Independent Test**: generate the organization twice with the same seed and check the records are identical, then merge its segments and check the speed and memory limits.

**Acceptance Scenarios**:

1. **Given** a seed, **When** the organization is generated twice, **Then** both are identical.
2. **Given** 300 repositories and 26 weeks of segments, **When** they are merged and a report is read, **Then** it finishes in under 10 seconds and under 1 GB of memory.

## Edge Cases

- A repository with no data in a week → it needs no segment for that week, and merging without one changes nothing.
- A repository with no data at all → the host cuts one empty segment for it, and the merged line then lists the repository as a full load does.
- A week with data in only some repositories → each repository has its own segment, the others do not need one.
- A change created in one week and merged or reviewed in a later one → it belongs to the week it was created in, and its later events are in that segment.
- A deploy → it belongs to the week of the deploy itself, not to the week of the changes it shipped.
- A segment asked for with any instant in the week → the week is the UTC Monday week that holds the instant.
- Merge called with no segments → an error.
- The same segment given twice → an error, because it would count everything twice.
- Segments of one repository whose repository-wide facts differ, such as the signal that runs tell when a runner picked them up, or whether production deploys come from releases → the merge is refused and says the weeks of that repository must be cut from one build.
- The same repository and week twice → refused. Repository names are compared without regard to case.
- A line that holds tickets → a segment cuts the repository part, changes and deploys, and leaves the tickets out without an error. A merged line has no ticket sources, so the ticket stations read as not connected. Parity is claimed against a full load of the same records without tickets.
- Two weeks of one repository cut from lines built at different times → each segment is exact for the line it was cut from. The host cuts again the weeks it wants refreshed.

## Functional Requirements

- **FR-001**: A built line MUST be able to produce a segment for one repository and one week.
- **FR-002**: Segments MUST be mergeable into a line, in any order, with the same result.
- **FR-003**: The report of a merged line MUST equal, byte for byte once marshalled, the report of loading all the records of the same repositories and weeks, for every report option, in both review modes.
- **FR-004**: The review mode MUST be chosen when the report is read, from the same line, with no rebuild.
- **FR-005**: Both review modes MUST be answerable from one build and one set of segments.
- **FR-006**: A change MUST belong to the UTC Monday week of its creation time, and a deploy to the week of its own time.
- **FR-007**: The order in which equal-time observations, items and notes are read MUST NOT depend on the order of the records or of the segments.
- **FR-008**: A segment MUST be a compact, versioned, self-describing byte form that records which version of the engine and which list of agent signatures cut it, and MUST be the same bytes when cut twice from one line.
- **FR-009**: Merge MUST refuse segments of different engine versions, of different registry versions, of an unknown format, a repeated repository and week, an empty list, and damaged bytes, with an error that says why.
- **FR-010**: Reading a segment MUST be safe on any input: its memory use is bounded by the size of the input and fixed limits, and it never crashes.
- **FR-011**: The per-repository figures a report states, such as changes, merged changes, runs, production deploys, environments, notes and sync times, MUST add up or combine to what a full load gives.
- **FR-012**: The library MUST provide a seeded, deterministic generator of a synthetic organization that is a source of records like any other, with repositories, changes, reviews by people and by agents, commits, CI runs and deploys.
- **FR-013**: The library MUST NOT change the report of any existing scenario, except an order that only the load order decided. Each such change is listed in the pull request.
- **FR-014**: The library MUST document how a host cuts, stores and merges segments, what is and is not covered, and how often a week should be cut again.

## Key Entities

- **Line**: every observation of a set of repositories. It is built from records, or merged from segments, and is reported over any window.
- **Segment**: the frozen part of a line that belongs to one repository and one week, as bytes. It holds what the line needs to put that week back.
- **Week**: a UTC Monday week. A change is in the week it was created, a deploy in the week of the deploy.
- **Review mode**: whether every review counts at the review station, or only reviews by people.
- **Synthetic organization**: a seeded source of records that stands in for a real organization.

## Success Criteria

- **SC-001**: For every existing scenario and for the synthetic organization, the report of the merged segments equals the report of the full load, in both review modes.
- **SC-002**: Merging 300 repositories by 26 weeks of segments and reading one report takes under 10 seconds and under 1 GB of resident memory.
- **SC-003**: Merging the same segments in different orders, and loading the same records in different orders, gives the same report.
- **SC-004**: Every damaged, truncated or mismatched input is refused with an error, with no crash.
- **SC-005**: No report of an existing scenario changes, except an order that only the load order decided, and each of those is listed.

## Out of Scope

- Ticket segments: tickets join across repositories and weeks, and are not in the approved shape or in the generator. A follow-up issue covers them.
- A store, a cache or a scheduler for segments: the host owns where bytes live and when a week is cut again.
- Incremental sync of records: this issue is about what is built from records, not about reading them.
- A command line for segments: the library API is the whole surface.

## Assumptions

- The host builds a line for a repository from the records of that repository as they stand, then cuts the segments it wants. A segment is exact for the line it was cut from, and a week that is cut again later is replaced as a whole.
- A line merged from segments has no ticket sources, so parity is claimed for lines without tickets.
- The list of agent signatures of a host does not change between the cut and the merge of a segment. When it does, merge says so.
- The design in the issue is final. The six gaps in it were settled in the brief.
