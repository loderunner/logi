package ingest

import (
	"testing"
	"testing/synctest"
	"time"

	"github.com/stretchr/testify/require"
)

func TestParseTime(t *testing.T) {
	base := time.Date(2023, 11, 14, 22, 13, 20, 0, time.UTC)

	tests := []struct {
		name string
		in   string
		want time.Time
	}{
		{
			"RFC 3339 without a fraction",
			"2024-01-02T03:04:05Z",
			time.Date(2024, 1, 2, 3, 4, 5, 0, time.UTC),
		},
		{
			"RFC 3339 with milliseconds",
			"2024-01-02T03:04:05.123Z",
			time.Date(2024, 1, 2, 3, 4, 5, 123_000_000, time.UTC),
		},
		{
			"RFC 3339 with microseconds",
			"2024-01-02T03:04:05.123456Z",
			time.Date(2024, 1, 2, 3, 4, 5, 123_456_000, time.UTC),
		},
		{
			"RFC 3339 with nanoseconds",
			"2024-01-02T03:04:05.123456789Z",
			time.Date(2024, 1, 2, 3, 4, 5, 123_456_789, time.UTC),
		},
		{
			"RFC 3339 with an offset is converted to UTC",
			"2024-01-02T05:04:05+02:00",
			time.Date(2024, 1, 2, 3, 4, 5, 0, time.UTC),
		},
		{"epoch in seconds", "1700000000", base},
		{
			"epoch in milliseconds",
			"1700000000123",
			base.Add(123 * time.Millisecond),
		},
		{
			"epoch in nanoseconds stays exact",
			"1700000000123456789",
			base.Add(123_456_789),
		},
		{
			"a negative epoch in seconds",
			"-1",
			time.Date(1969, 12, 31, 23, 59, 59, 0, time.UTC),
		},
		{"zero", "0", time.Unix(0, 0).UTC()},
		{
			"a small number is seconds",
			"1",
			time.Unix(1, 0).UTC(),
		},
		{
			"1e11 is milliseconds",
			"100000000000",
			time.UnixMilli(100_000_000_000).UTC(),
		},
		{
			"1e14 is nanoseconds",
			"100000000000000",
			time.Unix(0, 100_000_000_000_000).UTC(),
		},
		{
			"fractional seconds",
			"1700000000.25",
			base.Add(250 * time.Millisecond),
		},
		{
			"fractional milliseconds",
			"1700000000123.5",
			base.Add(123_500_000),
		},
		{
			"a negative fraction",
			"-1.5",
			time.Date(1969, 12, 31, 23, 59, 58, 500_000_000, time.UTC),
		},
		{
			"an exponent",
			"1.7e18",
			base,
		},
		{
			"the largest time",
			"9223372036854775807",
			time.Unix(0, 9223372036854775807).UTC(),
		},
		{
			"the smallest time",
			"-9223372036854775808",
			time.Unix(0, -9223372036854775808).UTC(),
		},
		{
			"the largest RFC 3339 time",
			"2262-04-11T23:47:16.854775807Z",
			time.Unix(0, 9223372036854775807).UTC(),
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, ok := parseTime(tt.in)
			require.True(t, ok)
			require.True(t, tt.want.Equal(got), "want %v, got %v", tt.want, got)
			require.Equal(t, time.UTC, got.Location())
		})
	}

	invalid := []struct {
		name string
		in   string
	}{
		{"empty", ""},
		{"text", "yesterday"},
		{"a space instead of the T", "2024-01-02 03:04:05Z"},
		{"no offset", "2024-01-02T03:04:05"},
		{"a date only", "2024-01-02"},
		{"surrounding space", " 1700000000"},
		{"a hexadecimal integer", "0x10"},
		{"NaN", "NaN"},
		{"Inf", "Inf"},
		{"+Inf", "+Inf"},
		{"-Inf", "-Inf"},
		{"Infinity", "infinity"},
		{"seconds past the year 2262", "99999999999"},
		{"milliseconds past the year 2262", "99999999999999"},
		{"nanoseconds past the year 2262", "9223372036854775808"},
		{"nanoseconds before the year 1678", "-9223372036854775809"},
		{"a huge number", "1e30"},
		{"a huge negative number", "-1e30"},
		{"an RFC 3339 time before 1678", "1600-01-01T00:00:00Z"},
		{"an RFC 3339 time after 2262", "2263-01-01T00:00:00Z"},
		{
			"a nanosecond past the largest RFC 3339 time",
			"2262-04-11T23:47:16.854775808Z",
		},
	}

	for _, tt := range invalid {
		t.Run("rejects "+tt.name, func(t *testing.T) {
			_, ok := parseTime(tt.in)
			require.False(t, ok)
		})
	}
}

func TestStamper(t *testing.T) {
	// day returns the time at the given second of 2024-01-02.
	day := func(sec int) time.Time {
		return time.Date(2024, 1, 2, 0, 0, sec, 0, time.UTC)
	}

	rfc := func(sec int) string {
		return day(sec).Format(time.RFC3339Nano)
	}

	t.Run(
		"detects the time field on the first line that has one",
		func(t *testing.T) {
			var s stamper

			ts, detected := s.stamp(
				map[string]string{"time": rfc(1), "msg": "a"},
			)
			require.True(t, detected)
			require.True(t, day(1).Equal(ts))
			require.Equal(t, "time", s.key)

			ts, detected = s.stamp(map[string]string{"time": rfc(2)})
			require.False(
				t,
				detected,
				"only the line that fixes the key reports it",
			)
			require.True(t, day(2).Equal(ts))
		},
	)

	t.Run(
		"prefers time, then ts, timestamp and @timestamp",
		func(t *testing.T) {
			fields := map[string]string{
				"@timestamp": rfc(4),
				"timestamp":  rfc(3),
				"ts":         rfc(2),
				"time":       rfc(1),
			}

			for _, want := range []struct {
				key string
				sec int
			}{
				{"time", 1}, {"ts", 2}, {"timestamp", 3}, {"@timestamp", 4},
			} {
				var s stamper

				ts, detected := s.stamp(fields)
				require.True(t, detected)
				require.Equal(t, want.key, s.key)
				require.True(t, day(want.sec).Equal(ts))

				delete(fields, want.key)
			}
		},
	)

	t.Run("matches keys without regard to case", func(t *testing.T) {
		var s stamper

		ts, detected := s.stamp(map[string]string{"TimeStamp": rfc(1)})
		require.True(t, detected)
		require.Equal(t, "TimeStamp", s.key)
		require.True(t, day(1).Equal(ts))
	})

	t.Run("prefers an exact-case match", func(t *testing.T) {
		// Run many times: the result must not depend on map iteration.
		for range 50 {
			var s stamper

			ts, _ := s.stamp(map[string]string{
				"Time": rfc(1),
				"TIME": rfc(2),
				"time": rfc(3),
				"tIme": rfc(4),
			})
			require.Equal(t, "time", s.key)
			require.True(t, day(3).Equal(ts))
		}
	})

	t.Run(
		"takes the smallest key when none is an exact match",
		func(t *testing.T) {
			for range 50 {
				var s stamper

				ts, _ := s.stamp(map[string]string{
					"tIme": rfc(1),
					"Time": rfc(2),
					"TIME": rfc(3),
				})
				require.Equal(t, "TIME", s.key)
				require.True(t, day(3).Equal(ts))
			}
		},
	)

	t.Run(
		"a candidate that doesn't parse gives way to the next",
		func(t *testing.T) {
			var s stamper

			ts, detected := s.stamp(map[string]string{
				"time": "not a time",
				"ts":   rfc(5),
			})
			require.True(t, detected)
			require.Equal(t, "ts", s.key)
			require.True(t, day(5).Equal(ts))
		},
	)

	t.Run("locks the key once", func(t *testing.T) {
		var s stamper

		_, detected := s.stamp(map[string]string{"ts": rfc(1)})
		require.True(t, detected)

		// A higher priority candidate appears, and is ignored.
		ts, detected := s.stamp(map[string]string{"time": rfc(9), "ts": rfc(2)})
		require.False(t, detected)
		require.True(t, day(2).Equal(ts))

		// A line with only another candidate takes the previous ts.
		ts, detected = s.stamp(map[string]string{"time": rfc(9)})
		require.False(t, detected)
		require.True(t, day(2).Equal(ts))
		require.Equal(t, "ts", s.key)
	})

	t.Run("a value that doesn't parse doesn't lock", func(t *testing.T) {
		synctest.Test(t, func(t *testing.T) {
			var s stamper

			ts, detected := s.stamp(map[string]string{"time": "soon"})
			require.False(t, detected)
			require.Empty(t, s.key)
			require.True(t, time.Now().UTC().Equal(ts), "arrival time")

			_, detected = s.stamp(map[string]string{"time": rfc(1)})
			require.True(t, detected)
			require.Equal(t, "time", s.key)
		})
	})

	t.Run("falls back to the previous ts after detection", func(t *testing.T) {
		var s stamper

		_, _ = s.stamp(map[string]string{"time": rfc(7)})

		for _, fields := range []map[string]string{
			{"msg": "no time field"},
			{"time": "garbage"},
			{"time": ""},
			{"_raw": "not json"},
		} {
			ts, detected := s.stamp(fields)
			require.False(t, detected)
			require.True(t, day(7).Equal(ts), "ts must repeat exactly")
		}

		// An older time is still the line's own time.
		ts, _ := s.stamp(map[string]string{"time": rfc(3)})
		require.True(t, day(3).Equal(ts))

		ts, _ = s.stamp(map[string]string{"msg": "x"})
		require.True(t, day(3).Equal(ts))
	})

	t.Run("accepts epochs and numeric strings", func(t *testing.T) {
		var s stamper

		ts, detected := s.stamp(map[string]string{"ts": "1704153600"})
		require.True(t, detected)
		require.True(t, day(0).Equal(ts))
	})

	t.Run("uses the arrival time before detection", func(t *testing.T) {
		synctest.Test(t, func(t *testing.T) {
			var s stamper

			first, detected := s.stamp(map[string]string{"_raw": "banner"})
			require.False(t, detected)
			require.True(t, time.Now().UTC().Equal(first))

			time.Sleep(time.Second)

			second, detected := s.stamp(map[string]string{"msg": "x"})
			require.False(t, detected)
			require.True(t, first.Add(time.Second).Equal(second))
		})
	})

	t.Run(
		"never goes before the previous ts before detection",
		func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				future := time.Now().UTC().Add(time.Hour)
				s := stamper{last: future}

				ts, detected := s.stamp(map[string]string{"_raw": "x"})
				require.False(t, detected)
				require.True(t, future.Equal(ts))
			})
		},
	)
}
