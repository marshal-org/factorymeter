package ingest

import (
	"context"
	"time"
)

// ticketWindow is the stretch of update times a ticket source holds in the
// store. A source saves it between runs, so a sync reads only what changed.
type ticketWindow struct {
	// Floor is the update time the tickets are held from. High is the update
	// time they are held up to. Every ticket updated between the two is in
	// the store.
	Floor time.Time `json:"floor"`
	High  time.Time `json:"high"`
	// Open is set once the open tickets last updated before the floor are
	// stored. The queue in front of the spec station needs them.
	Open bool `json:"open"`
}

// windowRead brings the window of a ticket source up to now. It knows nothing
// of the source: span does the reading.
type windowRead struct {
	o Options
	w *ticketWindow
	// slice is the stretch of update times one read covers. A source is read
	// a slice at a time, and the place a sync has reached is saved after
	// each, so a sync that stops goes on from the last finished slice.
	slice time.Duration
	// overlap is read again at the start of each sync, for tickets the source
	// stamped a little before it showed them.
	overlap time.Duration
	// span reads every ticket updated in [from, to) and stores it. A zero
	// bound is no bound. It may read more than it was asked for.
	span func(ctx context.Context, from, to time.Time) error
	// save writes the window to the store.
	save func() error
}

// updated reads the tickets updated since the floor that the store does not
// hold yet. A source keeps the floor of its first sync. A later floor only
// counts when it is earlier, and then the stretch below the old one is read.
func (x windowRead) updated(ctx context.Context) error {
	target := x.o.Since
	switch {
	case x.w.Floor.IsZero():
		x.w.Floor, x.w.High = target, target
		if err := x.save(); err != nil {
			return err
		}
	case target.Before(x.w.Floor):
		// The tickets updated since the old floor are stored, so read the
		// stretch below it.
		if err := x.stretch(ctx, target, x.w.Floor, nil); err != nil {
			return err
		}
		x.w.Floor = target
		if err := x.save(); err != nil {
			return err
		}
	}

	from := x.w.High.Add(-x.overlap)
	if from.Before(x.w.Floor) {
		from = x.w.Floor
	}
	return x.stretch(ctx, from, time.Time{}, func(reached time.Time) error {
		if reached.After(x.w.High) {
			x.w.High = reached
		}
		return x.save()
	})
}

// stretch reads the tickets updated from a time up to another, a slice at a
// time and oldest first. A zero end means up to now, and then the last slice
// has no upper bound, so a ticket updated while the sync runs is still read.
// reached is told the update time the store holds every ticket up to, after
// each slice.
func (x windowRead) stretch(ctx context.Context, from, to time.Time, reached func(time.Time) error) error {
	end := to
	if end.IsZero() {
		end = x.o.Now
	}
	for a := from; a.Before(end); a = a.Add(x.slice) {
		b, at := a.Add(x.slice), a.Add(x.slice)
		if !b.Before(end) {
			b, at = to, end
		}
		if err := x.span(ctx, a, b); err != nil {
			return err
		}
		if reached != nil {
			if err := reached(at); err != nil {
				return err
			}
		}
	}
	return nil
}
