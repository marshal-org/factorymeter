package measure

// EngineVersion names how a line is built from records and read into a report.
// A segment holds what the builder wrote, so it is only the same as another
// when the same engine cut it. Raise it with any change to what the builder
// writes or to what a report reads from it, including a change of a station's
// rules. Merge refuses segments of different engines.
const EngineVersion = 1
