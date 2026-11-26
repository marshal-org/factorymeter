# Segments: build once, report from many

A host that reports on many repositories does not have to build the whole line from
every record for every report. It builds a line for a repository, cuts it into
segments, one for each repository and week, stores the bytes, and later merges the
segments it needs. The report of the merged line is the report of a line built from
all the records of those repositories and weeks, byte for byte once marshalled.

```go
// Cut: from a line built from the records of one or more repositories.
for _, w := range line.Weeks("acme/api") {
	b, err := line.Segment("acme/api", w)
	// store b under the repository and the week
}

// Merge: any set of stored segments, in any order.
merged, err := measure.Merge(segments...)

// Report: pick the review mode now, with no rebuild.
rep, err := merged.WithReviews(true).Report(measure.Options{Weeks: 13, History: 26, Now: now})
```

## What a segment holds

A segment is the part of a line that belongs to one repository and one UTC Monday
week:

- A change belongs to the week it was created in, with everything that happened to it
  later: reviews, runs, merge and delivery. A change created on Sunday and merged
  two weeks later is in the segment of the Sunday.
- A production deploy belongs to the week it finished in.
- The segment also holds the repository facts, the agents and workflows it names, the
  records counted for coverage and the figures of the repository and week.

It is a small byte string: a header in the clear (format, engine version, a digest of
the agent signatures) and a compressed body. The same line gives the same bytes every
time. Segments are not a public data format: store and merge them with this library
and the same version of the engine.

## Review modes

Both review modes are in one set of segments. `WithReviews(true)` reads the line as
if only reviews by people counted at the review station, `WithReviews(false)` as if
every review counted. Neither needs a rebuild or another set of segments.

## What is refused

`Merge` refuses, and says why:

- no segments;
- bytes that are not a segment, are cut short or are damaged;
- segments of another format or engine version, or cut with another list of agent
  signatures: cut them again with the library you merge with;
- the same repository and week twice (repository names are compared without regard to
  case);
- segments of one repository that disagree on what is true of the whole repository:
  whether it is private, the time its changes are held from, whether runs tell when
  a runner picked them up, whether production deploys come from releases. Cut every
  week of a repository from one build.

Reading a segment is safe on any input: the memory it uses is bounded by the size of
the input and fixed limits.

## What parity covers

- Every report option and every drill, in both review modes.
- Merging any subset gives the report of those repositories and weeks, and the
  coverage names only them. A repository with no data in a week needs no segment for
  it. A repository with no data at all needs one empty segment, cut with any week, to
  be listed.
- Tickets are not in segments. A line with tickets is cut without them, and a merged
  line reads the ticket stations as not connected. Parity is with a load of the same
  records without tickets.

## How often to cut a week again

A segment is exact for the line it was cut from. A week whose records changed after
the cut, such as a change still open or a review that came later, is cut again from a
new build and replaces the old segment as a whole. A change belongs to its creation
week, so a week stays open until its changes close. Cut the recent weeks again with
each sync, and older weeks only when you sync them again.

## Privacy

A segment holds what the records hold: change and deploy titles and links, the names of
repositories, the ids of the agents that took part, and times. It holds no logins. Treat segment bytes as
you treat the store.

## Testing at scale

`pkg/synth` generates a seeded organization without real data. It is a
`measure.Source`, so a host can load it like any other. `FACTORYMETER_SCALE=1 go test
./pkg/measure -run TestMergeScaleGate -v` merges 300 repositories by 26 weeks and
reads a report.
