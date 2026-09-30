package notify

import (
	"fmt"
	"strings"
	"time"
)

// Clock is a local time of day in minutes after midnight.
type Clock int

// ParseClock reads "HH:MM" (24-hour clock).
func ParseClock(s string) (Clock, error) {
	t, err := time.Parse("15:04", s)
	if err != nil || len(s) != 5 {
		return 0, fmt.Errorf("%q is not a time of day as HH:MM (24-hour clock)", s)
	}
	return Clock(t.Hour()*60 + t.Minute()), nil
}

// MustClock is ParseClock for values already validated (defaults.yaml, stored settings); an invalid one is 00:00.
func MustClock(s string) Clock {
	c, _ := ParseClock(s)
	return c
}

// Of returns the clock of t in loc.
func Of(t time.Time, loc *time.Location) Clock {
	l := t.In(loc)
	return Clock(l.Hour()*60 + l.Minute())
}

// InQuietHours reports whether t falls inside the quiet hours q in loc. Start == End is an empty window; Start after
// End spans midnight (22:00–08:00).
func InQuietHours(q QuietHours, t time.Time, loc *time.Location) bool {
	if !q.Enabled {
		return false
	}
	start, err1 := ParseClock(q.Start)
	end, err2 := ParseClock(q.End)
	if err1 != nil || err2 != nil || start == end {
		return false
	}
	now := Of(t, loc)
	if start < end {
		return now >= start && now < end
	}
	return now >= start || now < end
}

// Today returns the moment clock c falls on the local day of t, in loc.
func Today(t time.Time, c Clock, loc *time.Location) time.Time {
	l := t.In(loc)
	return time.Date(l.Year(), l.Month(), l.Day(), int(c)/60, int(c)%60, 0, 0, loc)
}

// Next returns the first moment clock c occurs at or after t, in loc.
func Next(t time.Time, c Clock, loc *time.Location) time.Time {
	at := Today(t, c, loc)
	if at.Before(t) {
		l := t.In(loc).AddDate(0, 0, 1)
		at = time.Date(l.Year(), l.Month(), l.Day(), int(c)/60, int(c)%60, 0, 0, loc)
	}
	return at
}

// NextWeekday returns the first moment of weekday wd at clock c at or after t, in loc.
func NextWeekday(t time.Time, wd time.Weekday, c Clock, loc *time.Location) time.Time {
	at := Today(t, c, loc)
	for i := 0; i < 8; i++ {
		if at.Weekday() == wd && !at.Before(t) {
			return at
		}
		l := at.AddDate(0, 0, 1)
		at = time.Date(l.Year(), l.Month(), l.Day(), int(c)/60, int(c)%60, 0, 0, loc)
	}
	return at
}

// ParseWeekday reads an English weekday name in lower case (defaults.yaml backups.restore_test_weekday).
func ParseWeekday(s string) (time.Weekday, bool) {
	for d := time.Sunday; d <= time.Saturday; d++ {
		if strings.ToLower(d.String()) == s {
			return d, true
		}
	}
	return time.Sunday, false
}
