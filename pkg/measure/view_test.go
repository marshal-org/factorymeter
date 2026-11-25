package measure

// seenObs and seenStays are what the report reads of a stream in the review
// mode of the line.
func (l *Line) seenObs(stream string) []obs {
	var in []obs
	for _, o := range l.obs[stream] {
		if seenIn(o.mode, l.humanReviewsOnly) {
			in = append(in, o)
		}
	}
	return in
}

func (l *Line) seenStays(queue string) []stay {
	var in []stay
	for _, s := range l.stays[queue] {
		if seenIn(s.mode, l.humanReviewsOnly) {
			in = append(in, s)
		}
	}
	return in
}
