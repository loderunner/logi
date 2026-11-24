package ingest

import (
	"math"
	"strconv"
	"strings"
	"time"
)

// timeKeys are the candidates for the time field, by priority. A line's key
// matches without regard to case.
var timeKeys = []string{"time", "ts", "timestamp", "@timestamp"}

// Magnitude thresholds that guess the unit of a numeric epoch. A value below
// maxSeconds is in seconds, below maxMillis in milliseconds, and anything
// else in nanoseconds. Microseconds are deliberately not supported: the
// ranges of two adjacent units can't overlap, and a guess between them would
// be wrong for real data.
const (
	maxSeconds = 1e11
	maxMillis  = 1e14
)

// The range of times that fit in a BIGINT of Unix nanoseconds, which is how
// logstore stores them.
var (
	minTime = time.Unix(0, math.MinInt64)
	maxTime = time.Unix(0, math.MaxInt64)
)

// parseTime parses the text of a time field and returns it in UTC. It
// accepts RFC 3339 with any fractional precision, and a numeric epoch, as an
// integer or with a fraction, whose unit is guessed from its magnitude. It
// reports false for any other text, and for a time outside the years 1678 to
// 2262, so that the caller falls back instead of failing a write.
func parseTime(s string) (time.Time, bool) {
	t, err := time.Parse(time.RFC3339Nano, s)
	if err != nil {
		var ok bool

		t, ok = parseEpoch(s)
		if !ok {
			return time.Time{}, false
		}
	}

	if t.Before(minTime) || t.After(maxTime) {
		return time.Time{}, false
	}

	return t.UTC(), true
}

// parseEpoch parses a number of seconds, milliseconds or nanoseconds since
// the Unix epoch. It keeps an integer exact, since a float64 can't hold
// nanoseconds of the present day, and falls back to a float for a fraction.
func parseEpoch(s string) (time.Time, bool) {
	n, err := strconv.ParseInt(s, 10, 64)
	if err == nil {
		switch {
		case n > -maxSeconds && n < maxSeconds:
			return time.Unix(n, 0), true
		case n > -maxMillis && n < maxMillis:
			return time.UnixMilli(n), true
		default:
			return time.Unix(0, n), true
		}
	}

	f, err := strconv.ParseFloat(s, 64)
	if err != nil || math.IsNaN(f) || math.IsInf(f, 0) {
		return time.Time{}, false
	}

	var unit time.Duration

	switch {
	case math.Abs(f) < maxSeconds:
		unit = time.Second
	case math.Abs(f) < maxMillis:
		unit = time.Millisecond
	default:
		unit = time.Nanosecond
	}

	// A time that doesn't fit in an int64 of nanoseconds is out of range,
	// and its Duration would overflow.
	if math.Abs(f*float64(unit)) >= math.MaxInt64 {
		return time.Time{}, false
	}

	whole, frac := math.Modf(f)

	return time.Unix(0, 0).
		Add(time.Duration(whole) * unit).
		Add(time.Duration(math.Round(frac * float64(unit)))), true
}

// stamper decides the ts of each row.
//
// Rows are ordered by (ts, id). A row with no usable time must not land
// somewhere unrelated to the lines around it, so it takes the ts of the row
// before it and sorts among its neighbours by id. Before the time field is
// known, a row has nothing but its arrival time to go on, which also keeps
// the order of arrival.
//
// The time field is detected once, on the first line that has a candidate key
// with a value that parses, and then fixed for the run. Re-detecting per line
// would let one line with a stray "ts" field change the meaning of the column
// halfway through a file.
//
// When the user names the time field, it is the only candidate. The rest is
// the same: the key is fixed by the first line where its value parses, so that
// the rows before it are restamped once.
type stamper struct {
	// field is the time field the user named, or "" to detect it among
	// timeKeys.
	field string
	// key is the time field, or "" until it is detected.
	key string
	// last is the ts of the previous row.
	last time.Time
}

// stamp returns the ts of the row with the given fields. detected is true
// only for the row that fixes the time field, which tells the caller that
// the rows before it have no time of their own.
func (s *stamper) stamp(
	fields map[string]string,
) (ts time.Time, detected bool) {
	if s.key == "" {
		key, t, ok := detectTime(fields, s.field)
		if ok {
			s.key = key
			s.last = t

			return t, true
		}

		// UTC drops the monotonic reading, so the comparison with last is
		// on the wall time, which is what gets stored.
		now := time.Now().UTC()
		if now.Before(s.last) {
			now = s.last
		}

		s.last = now

		return now, false
	}

	value, ok := fields[s.key]
	if ok {
		t, ok := parseTime(value)
		if ok {
			s.last = t
		}
	}

	return s.last, false
}

// detectTime finds the time field among the fields of a line.
//
// With a named field, only the key that equals it counts, exactly: the match
// is case-sensitive, and the key may be a dotted, flattened one such as
// "log.time". The value must parse.
//
// Without one, it is the first candidate, by priority, with a value that
// parses. A key that equals the candidate wins over one that matches only
// without regard to case, and among those the smallest key wins, so that the
// result doesn't depend on map iteration.
func detectTime(
	fields map[string]string,
	field string,
) (string, time.Time, bool) {
	if field != "" {
		value, ok := fields[field]
		if !ok {
			return "", time.Time{}, false
		}

		t, ok := parseTime(value)

		return field, t, ok
	}

	best := make([]string, len(timeKeys))

	for key := range fields {
		for i, candidate := range timeKeys {
			if !strings.EqualFold(key, candidate) {
				continue
			}

			switch {
			case best[i] == candidate:
				// An exact match is final.
			case key == candidate, best[i] == "", key < best[i]:
				best[i] = key
			}
		}
	}

	for _, key := range best {
		if key == "" {
			continue
		}

		t, ok := parseTime(fields[key])
		if ok {
			return key, t, true
		}
	}

	return "", time.Time{}, false
}
