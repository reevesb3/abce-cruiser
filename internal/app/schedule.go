package app

// Pure schedule logic: parsing the SuperSaaS day-view page, computing open
// seats, building the reservation body, and classifying POST responses.
// All functions here are pure and covered by schedule_test.go.

import (
	"fmt"
	"html"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"
)

// Epochs in the page are stored as local-wall-clock-labelled-as-UTC, so render as UTC.
func fromEpoch(e int64) time.Time { return time.Unix(e, 0).UTC() }

// Resolve a loose date string ('Aug 24', '2026-08-24', '8/24/2026') to a UTC-midnight date.
func resolveTargetDate(s string) (time.Time, error) {
	s = strings.TrimSpace(s)
	full := []string{"2006-01-02", "1/2/2006", "1-2-2006", "January 2, 2006", "Jan 2, 2006", "January 2 2006", "Jan 2 2006"}
	for _, l := range full {
		if d, err := time.ParseInLocation(l, s, time.UTC); err == nil {
			return time.Date(d.Year(), d.Month(), d.Day(), 0, 0, 0, 0, time.UTC), nil
		}
	}
	// month+day only -> assume the current year
	for _, l := range []string{"Jan 2", "January 2", "1/2"} {
		if d, err := time.ParseInLocation(l, s, time.UTC); err == nil {
			return time.Date(time.Now().Year(), d.Month(), d.Day(), 0, 0, 0, 0, time.UTC), nil
		}
	}
	return time.Time{}, fmt.Errorf("could not parse date %q (try 'Aug 24' or '2026-08-24')", s)
}

// ---------------------------------------------------------------------------
// Open-hours rows (the page's ecache): the AUTHORITATIVE per-day schedule.
// Rows are [dayStart,dayEnd,cap,openMin,closeMin] or [...,breakStart,breakEnd].

type openRow struct {
	Date                              time.Time
	Open, Close, BreakStart, BreakEnd int // minutes from midnight
}

type openData struct {
	Rows     []openRow
	Min, Max time.Time // zero when no rows
}

var (
	ecacheRe = regexp.MustCompile(`(?s)ecache\s*=\s*\{data:\s*(\[\[.*?\]\])\}`)
	rowRe    = regexp.MustCompile(`\[([^\[\]]+)\]`)
)

func getOpenRows(page string) openData {
	var od openData
	m := ecacheRe.FindStringSubmatch(page)
	if m == nil {
		return od
	}
	for _, r := range rowRe.FindAllStringSubmatch(m[1], -1) {
		f := strings.Split(r[1], ",")
		if len(f) < 5 {
			continue
		}
		ints := make([]int64, len(f))
		bad := false
		for i, s := range f {
			v, err := strconv.ParseInt(strings.TrimSpace(s), 10, 64)
			if err != nil {
				bad = true
				break
			}
			ints[i] = v
		}
		if bad {
			continue
		}
		row := openRow{Date: fromEpoch(ints[0]).Truncate(24 * time.Hour), Open: int(ints[3]), Close: int(ints[4])}
		if len(f) >= 7 {
			row.BreakStart, row.BreakEnd = int(ints[5]), int(ints[6])
		}
		od.Rows = append(od.Rows, row)
		if od.Min.IsZero() || row.Date.Before(od.Min) {
			od.Min = row.Date
		}
		if od.Max.IsZero() || row.Date.After(od.Max) {
			od.Max = row.Date
		}
	}
	return od
}

// The open window for exactly this day, or nil if the schedule has no row for it
// (closed / beyond the loaded range). We NEVER estimate — a missing row means we
// cannot claim any slot is open.
func getOpenWindow(od openData, target time.Time) *openRow {
	for i := range od.Rows {
		if od.Rows[i].Date.Equal(target) {
			return &od.Rows[i]
		}
	}
	return nil
}

// ---------------------------------------------------------------------------
// Bookings. Entries have TWO layouts — other people's are short:
//   [start,end,res,resid,0,0,0,false,"Name"]
// reservations YOU own are long (email/phone/name/comment fields):
//   [start,end,res,resid,1,0,0,true,"you@email",ts,"",1,"Full Name","phone","mobile","","comment"]
// so we match up to the name/email field and do NOT anchor on a closing ']'.

type booking struct {
	Start, End time.Time
	Resource   string
	Owned      bool
	Name       string
}

var bookingRe = regexp.MustCompile(`\[(\d{10}),(\d{10}),(\d+),(\d+),\d+,\d+,\d+,(true|false),"([^"]*)"`)

// getAllBookings parses every booking on the page (the day view returns a wide
// range in one fetch). ownerName is substituted for the logged-in user's own
// reservations, whose payload carries an email rather than a display name.
func getAllBookings(page string, ownerName string) []booking {
	var out []booking
	for _, m := range bookingRe.FindAllStringSubmatch(page, -1) {
		s, _ := strconv.ParseInt(m[1], 10, 64)
		e, _ := strconv.ParseInt(m[2], 10, 64)
		owned := m[5] == "true"
		name := m[6]
		if owned {
			name = ownerName
		}
		out = append(out, booking{Start: fromEpoch(s), End: fromEpoch(e), Resource: m[3], Owned: owned, Name: name})
	}
	return out
}

func getBookings(page string, target time.Time, ownerName string) []booking {
	var out []booking
	for _, b := range getAllBookings(page, ownerName) {
		if b.Start.Truncate(24 * time.Hour).Equal(target) {
			out = append(out, b)
		}
	}
	return out
}

// oppositeResource returns the other seat's resource id in a two-seat car
// (the observe-first seat for a drive-first booking, and vice versa).
func oppositeResource(c car, resID string) string {
	for i, r := range c.Roles {
		if r.ID == resID {
			return c.Roles[len(c.Roles)-1-i].ID
		}
	}
	return ""
}

// ---------------------------------------------------------------------------
// Open seats = the real slot grid the school uses (validated against 160+ live
// bookings): the break splits the day into sub-windows [Open,BreakStart] and
// [BreakEnd,Close]; slots are non-overlapping blocks of slotHours tiled from
// each sub-window's start. Per slot, each seat/role with no overlapping booking.

type role struct{ ID, Label string }

// car is one SuperSaaS schedule (a physical car) with its two capacity seats.
type car struct {
	Name  string
	Roles []role
}

type seatOption struct {
	Start, End time.Time
	Car        string // which car's schedule this seat belongs to
	ResourceID string
	Role       string
}

// getOpenOptions returns the open seats on one car's page for the target day.
// carName tags each option so aggregated cross-car lists know where to book.
func getOpenOptions(target time.Time, w *openRow, booked []booking, roles []role, slotHours float64, carName string) []seatOption {
	slotMin := int(slotHours * 60)
	var subs [][2]int
	if w.BreakEnd > w.BreakStart {
		subs = [][2]int{{w.Open, w.BreakStart}, {w.BreakEnd, w.Close}}
	} else {
		subs = [][2]int{{w.Open, w.Close}}
	}
	var opts []seatOption
	for _, sub := range subs {
		for m := sub[0]; m+slotMin <= sub[1]; m += slotMin {
			s := target.Add(time.Duration(m) * time.Minute)
			e := s.Add(time.Duration(slotMin) * time.Minute)
			for _, r := range roles {
				taken := false
				for _, b := range booked {
					if b.Resource == r.ID && s.Before(b.End) && e.After(b.Start) {
						taken = true
						break
					}
				}
				if !taken {
					opts = append(opts, seatOption{Start: s, End: e, Car: carName, ResourceID: r.ID, Role: r.Label})
				}
			}
		}
	}
	sortOptions(opts)
	return opts
}

// sortOptions orders seats by start time, then car, then resource — a stable,
// human-friendly order that stays consistent whether one car or many.
func sortOptions(opts []seatOption) {
	sort.Slice(opts, func(i, j int) bool {
		if !opts[i].Start.Equal(opts[j].Start) {
			return opts[i].Start.Before(opts[j].Start)
		}
		if opts[i].Car != opts[j].Car {
			return opts[i].Car < opts[j].Car
		}
		return opts[i].ResourceID < opts[j].ResourceID
	})
}

// ---------------------------------------------------------------------------
// Reservation POST body. Field order and encoding must match the known-good
// browser request exactly (verified against a successful live booking).

// SuperSaaS expects times like "8/24/2026 8:00am" (no leading zeros, lowercase am/pm).
func formatSlot(t time.Time) string { return t.Format("1/2/2006 3:04pm") }

// RFC 3986 escape, equivalent to .NET Uri.EscapeDataString for our data
// (spaces -> %20, / -> %2F, : -> %3A; unreserved chars kept literal).
func escapeData(s string) string {
	var b strings.Builder
	for _, c := range []byte(s) {
		if (c >= 'A' && c <= 'Z') || (c >= 'a' && c <= 'z') || (c >= '0' && c <= '9') ||
			c == '-' || c == '_' || c == '.' || c == '~' {
			b.WriteByte(c)
		} else {
			fmt.Fprintf(&b, "%%%02X", c)
		}
	}
	return b.String()
}

func buildBody(start time.Time, slotHours float64, resID, fullName, phone, mobile string) string {
	pairs := [][2]string{
		{"reservation[start_time]", formatSlot(start)},
		{"reservation[finish_time]", formatSlot(start.Add(time.Duration(slotHours * float64(time.Hour))))},
		{"reservation[full_name]", fullName},
		{"reservation[phone]", phone},
		{"reservation[mobile]", mobile},
		{"reservation[field_1_r]", ""},
		{"reservation[resource_id]", resID},
		{"button", ""},
		{"reservation[xpos]", "136"},
		{"reservation[ypos]", "912"},
	}
	parts := make([]string, 0, len(pairs))
	for _, p := range pairs {
		parts = append(parts, escapeData(p[0])+"="+escapeData(p[1]))
	}
	return strings.Join(parts, "&")
}

// ---------------------------------------------------------------------------
// Response classification. Every pattern carries (?i) so matching is
// case-insensitive against the schedule's varied wording.

var (
	successRe  = regexp.MustCompile(`(?i)successfully|has been (created|booked|saved|scheduled)|reservation (was|has been)|your reservation (has|is)|Thank you`)
	retryRe    = regexp.MustCompile(`(?i)\d+ days in advance|cannot be made (more than|before)|not yet (open|available)|too (early|far)|in the past|Log ?in|Sign ?in|password|captcha|something went wrong|temporarily unavailable|try again later|Service Unavailable|Internal Server Error`)
	takenRe    = regexp.MustCompile(`(?i)already (been )?(booked|taken|reserved)|no longer available|not available|is full|fully booked|waiting ?list|overlaps|double.?book`)
	wrapRe     = regexp.MustCompile(`(?i)prohibited this reservation|problems with the following`)
	fieldMsgRe = regexp.MustCompile(`(?is)problems with the following field[^<]*</[^>]+>\s*<[^>]+>(.*?)</`)
	tagStripRe = regexp.MustCompile(`<[^>]+>`)
	spaceRe    = regexp.MustCompile(`\s+`)
)

// classify a POST response: success | retry | taken | validation | unknown.
func classify(page string) string {
	switch {
	case successRe.MatchString(page) && !wrapRe.MatchString(page):
		return "success" // positive confirmation, no error wrapper
	case retryRe.MatchString(page):
		return "retry" // window not open yet / transient / auth blip
	case takenRe.MatchString(page):
		return "taken" // this seat is gone -> next choice
	case wrapRe.MatchString(page):
		return "validation" // some other field error -> next choice
	}
	return "unknown" // unrecognized page -> stop & verify by hand
}

// Human-readable validation error, if extractable.
func validationMessage(page string) string {
	m := fieldMsgRe.FindStringSubmatch(page)
	if m == nil {
		return "unknown validation error"
	}
	return strings.TrimSpace(spaceRe.ReplaceAllString(tagStripRe.ReplaceAllString(m[1], " "), " "))
}

// ---------------------------------------------------------------------------
// Profile scrape: SuperSaaS pre-fills reservation[full_name/phone/mobile] on
// the logged-in day view — that's where the user's contact details come from
// (never hardcoded), cached in the OS credential store as fallback.

type profileInfo struct{ FullName, Phone, Mobile string }

func getProfileFields(page string) profileInfo {
	get := func(field string) string {
		tagRe := regexp.MustCompile(`(?is)<input[^>]*name="reservation\[` + field + `\]"[^>]*>`)
		tag := tagRe.FindString(page)
		if tag == "" {
			return ""
		}
		v := regexp.MustCompile(`value="([^"]*)"`).FindStringSubmatch(tag)
		if v == nil {
			return ""
		}
		return html.UnescapeString(v[1])
	}
	return profileInfo{FullName: get("full_name"), Phone: get("phone"), Mobile: get("mobile")}
}
