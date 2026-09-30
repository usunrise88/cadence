package compute

import (
	"fmt"
	"slices"
	"sort"
	"strconv"
	"strings"
	"time"
	_ "time/tzdata" // availability windows name IANA zones; the container image may have no zoneinfo
)

// Window is one availability window of a card for a job kind (R19): the days it opens on, when it opens and closes
// (HH:MM; an end at or before the start closes the next day; 24:00 is midnight) and its IANA time zone (UTC when
// empty). Its JSON form is the contract's AvailabilityWindow.
type Window struct {
	Days     []string `json:"days"`
	Start    string   `json:"start"`
	End      string   `json:"end"`
	Timezone string   `json:"timezone,omitempty"`
}

// Windows maps a job kind to the windows in which jobs of that kind may run on a card. A kind without an entry may
// run any time.
type Windows map[string][]Window

// Days in Window.Days, in time.Weekday order.
var weekdays = []string{"sun", "mon", "tue", "wed", "thu", "fri", "sat"}

// ValidateWindows checks ws and returns one message per problem, keyed by a JSON pointer below the windows.
func ValidateWindows(ws Windows) map[string]string {
	bad := map[string]string{}
	for kind, list := range ws {
		if !slices.Contains(JobKinds, kind) {
			bad["/"+kind] = fmt.Sprintf("unknown job kind %q", kind)
			continue
		}
		for i, w := range list {
			at := fmt.Sprintf("/%s/%d", kind, i)
			if len(w.Days) == 0 {
				bad[at+"/days"] = "name at least one day"
			}
			for j, d := range w.Days {
				if !slices.Contains(weekdays, d) {
					bad[fmt.Sprintf("%s/days/%d", at, j)] = fmt.Sprintf("unknown day %q (mon … sun)", d)
				}
			}
			if _, err := clock(w.Start, false); err != nil {
				bad[at+"/start"] = err.Error()
			}
			if _, err := clock(w.End, true); err != nil {
				bad[at+"/end"] = err.Error()
			}
			if _, err := location(w.Timezone); err != nil {
				bad[at+"/timezone"] = err.Error()
			}
		}
	}
	return bad
}

// clock parses HH:MM into the offset from midnight; 24:00 only when allowEnd.
func clock(s string, allowEnd bool) (time.Duration, error) {
	h, m, ok := strings.Cut(s, ":")
	hh, err1 := strconv.Atoi(h)
	mm, err2 := strconv.Atoi(m)
	if !ok || len(h) != 2 || len(m) != 2 || err1 != nil || err2 != nil || mm < 0 || mm > 59 || hh < 0 ||
		hh > 24 || (hh == 24 && (mm != 0 || !allowEnd)) {
		return 0, fmt.Errorf("%q is not a time of day (HH:MM)", s)
	}
	return time.Duration(hh)*time.Hour + time.Duration(mm)*time.Minute, nil
}

func location(tz string) (*time.Location, error) {
	if tz == "" {
		return time.UTC, nil
	}
	loc, err := time.LoadLocation(tz)
	if err != nil {
		return nil, fmt.Errorf("unknown time zone %q", tz)
	}
	return loc, nil
}

type interval struct{ from, to time.Time }

// Availability reports whether a job of jobKind may run at now and, when it may, until when without a break
// (windows that touch or overlap count as one). Always is true when the kind has no windows (no end).
func (ws Windows) Availability(jobKind string, now time.Time) (open, always bool, closes time.Time) {
	list := ws[jobKind]
	if len(list) == 0 {
		return true, true, time.Time{}
	}
	var spans []interval
	for _, w := range list {
		loc, err := location(w.Timezone)
		if err != nil {
			continue // ValidateWindows refused it; an unreadable window never opens
		}
		start, err1 := clock(w.Start, false)
		end, err2 := clock(w.End, true)
		if err1 != nil || err2 != nil {
			continue
		}
		local := now.In(loc)
		for off := -1; off <= 8; off++ {
			day := time.Date(local.Year(), local.Month(), local.Day()+off, 0, 0, 0, 0, loc)
			if !slices.Contains(w.Days, weekdays[day.Weekday()]) {
				continue
			}
			from, to := addClock(day, start), addClock(day, end)
			if !to.After(from) {
				to = addClock(day.AddDate(0, 0, 1), end)
			}
			spans = append(spans, interval{from, to})
		}
	}
	sort.Slice(spans, func(i, j int) bool { return spans[i].from.Before(spans[j].from) })
	var merged []interval
	for _, s := range spans {
		if n := len(merged); n > 0 && !s.from.After(merged[n-1].to) {
			if s.to.After(merged[n-1].to) {
				merged[n-1].to = s.to
			}
			continue
		}
		merged = append(merged, s)
	}
	for _, s := range merged {
		if !now.Before(s.from) && now.Before(s.to) {
			return true, false, s.to
		}
	}
	return false, false, time.Time{}
}

// addClock is the wall-clock time d after midnight of day (daylight-saving days keep their wall clock).
func addClock(day time.Time, d time.Duration) time.Time {
	return time.Date(day.Year(), day.Month(), day.Day(), int(d/time.Hour), int(d%time.Hour/time.Minute), 0, 0, day.Location())
}
