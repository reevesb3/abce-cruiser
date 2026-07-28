package app

// Pure-logic tests against a synthetic fixture; network paths are exercised
// manually via -test-login/-dry-run.

import (
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/zalando/go-keyring"
)

// wall-clock -> epoch the way the page stores it (local time labelled as UTC)
func e(wc string) int64 {
	for _, l := range []string{"2006-01-02 15:04", "2006-01-02"} {
		if t, err := time.ParseInLocation(l, wc, time.UTC); err == nil {
			return t.Unix()
		}
	}
	panic("bad fixture time: " + wc)
}

func fixture() string {
	// ecache: Sep 7 = 5-field 8:00-12:00 no break; Sep 8 = 7-field 7:00-18:00 break 13:00-15:00
	ecache := fmt.Sprintf("ecache = {data: [[%d,%d,1,480,720],[%d,%d,2,420,1080,780,900]]}",
		e("2026-09-07"), e("2026-09-07 23:59"), e("2026-09-08"), e("2026-09-08 23:59"))
	// bookings on Sep 7: res 84893 10-12 (other "Alex Doe"); res 84894 10-12 (OWNED, long layout)
	bookings := fmt.Sprintf(`var ev = [[%d,%d,84893,90001,0,0,0,false,"Alex Doe"],`+
		`[%d,%d,84894,90002,1,0,0,true,"owner@example.com",111,"",1,"Ignored","p","m","","c"]];`,
		e("2026-09-07 10:00"), e("2026-09-07 12:00"), e("2026-09-07 10:00"), e("2026-09-07 12:00"))
	form := `<input size="30" required="required" type="text" value="O&#39;Brien Test" name="reservation[full_name]" id="reservation_full_name" />
<input size="30" type="text" value="555-111-2222" name="reservation[phone]" id="reservation_phone" />
<input size="30" type="text" value="555-333-4444" name="reservation[mobile]" id="reservation_mobile" />`
	return "<html><head><script>\nfirst_hour = 7, last_hour = 21;\n" + ecache + "\n" + bookings + "\n</script></head><body>" + form + "</body></html>"
}

func day(s string) time.Time {
	t, _ := time.ParseInLocation("2006-01-02", s, time.UTC)
	return t
}

func TestResolveTargetDate(t *testing.T) {
	for _, in := range []string{"2026-08-24", "8/24/2026", "Aug 24, 2026"} {
		d, err := resolveTargetDate(in)
		if err != nil || d.Month() != 8 || d.Day() != 24 {
			t.Errorf("resolveTargetDate(%q) = %v, %v", in, d, err)
		}
	}
	d, err := resolveTargetDate("Aug 24") // month+day -> current year
	if err != nil || d.Month() != 8 || d.Day() != 24 {
		t.Errorf("month-day form failed: %v, %v", d, err)
	}
	if _, err := resolveTargetDate("not-a-date"); err == nil {
		t.Error("expected error for garbage input")
	}
}

func TestFromEpoch(t *testing.T) {
	got := fromEpoch(e("2026-09-07 08:00")).Format("2006-01-02 15:04")
	if got != "2026-09-07 08:00" {
		t.Errorf("fromEpoch round-trip = %s", got)
	}
}

func TestFormatSlot(t *testing.T) {
	if s := formatSlot(time.Date(2026, 8, 18, 8, 0, 0, 0, time.UTC)); s != "8/18/2026 8:00am" {
		t.Errorf("AM: got %q", s)
	}
	if s := formatSlot(time.Date(2026, 8, 18, 14, 30, 0, 0, time.UTC)); s != "8/18/2026 2:30pm" {
		t.Errorf("PM: got %q", s)
	}
}

func TestOpenRowsAndWindow(t *testing.T) {
	od := getOpenRows(fixture())
	if len(od.Rows) != 2 {
		t.Fatalf("rows = %d, want 2 (5- and 7-field)", len(od.Rows))
	}
	if !od.Min.Equal(day("2026-09-07")) || !od.Max.Equal(day("2026-09-08")) {
		t.Errorf("coverage = %v..%v", od.Min, od.Max)
	}
	w := getOpenWindow(od, day("2026-09-07"))
	if w == nil || w.Open != 480 || w.Close != 720 || w.BreakStart != 0 || w.BreakEnd != 0 {
		t.Errorf("5-field row: %+v", w)
	}
	w = getOpenWindow(od, day("2026-09-08"))
	if w == nil || w.Open != 420 || w.Close != 1080 || w.BreakStart != 780 || w.BreakEnd != 900 {
		t.Errorf("7-field row: %+v", w)
	}
	if getOpenWindow(od, day("2026-09-09")) != nil {
		t.Error("missing day must be nil - never invent hours")
	}
}

func TestGetOpenRowsSkipsMalformed(t *testing.T) {
	// A too-short row (3 fields) and a row with a non-numeric field are both
	// skipped; only the valid 5-field row survives — and no panic on garbage.
	page := fmt.Sprintf("ecache = {data: [[%d,%d,1],[%d,%d,1,480,720],[%d,%d,1,xx,720]]}",
		e("2026-09-06"), e("2026-09-06 23:59"), // 3 fields -> skipped
		e("2026-09-07"), e("2026-09-07 23:59"), // valid
		e("2026-09-08"), e("2026-09-08 23:59")) // non-numeric openMin -> skipped
	od := getOpenRows(page)
	if len(od.Rows) != 1 {
		t.Fatalf("rows = %d, want 1 (short row + non-numeric row skipped)", len(od.Rows))
	}
	if !od.Rows[0].Date.Equal(day("2026-09-07")) || od.Rows[0].Open != 480 {
		t.Errorf("surviving row = %+v", od.Rows[0])
	}
	if !od.Min.Equal(day("2026-09-07")) || !od.Max.Equal(day("2026-09-07")) {
		t.Errorf("coverage = %v..%v, want single day 2026-09-07", od.Min, od.Max)
	}
	// A page with no ecache at all yields empty data, not a panic.
	if got := getOpenRows("<html>no ecache here</html>"); len(got.Rows) != 0 || !got.Min.IsZero() {
		t.Errorf("no-ecache page = %+v, want empty", got)
	}
}

func TestGetBookings(t *testing.T) {
	bk := getBookings(fixture(), day("2026-09-07"), "Cached Owner")
	if len(bk) != 2 {
		t.Fatalf("bookings = %d, want 2", len(bk))
	}
	var other, owned *booking
	for i := range bk {
		if bk[i].Resource == "84893" {
			other = &bk[i]
		} else if bk[i].Resource == "84894" {
			owned = &bk[i]
		}
	}
	if other == nil || other.Owned || other.Name != "Alex Doe" {
		t.Errorf("other booking: %+v", other)
	}
	if owned == nil || !owned.Owned || owned.Name != "Cached Owner" {
		t.Errorf("owned booking: %+v", owned)
	}
	if got := getBookings(fixture(), day("2026-09-08"), "x"); len(got) != 0 {
		t.Errorf("other-day filter failed: %d", len(got))
	}
}

func TestGetOpenOptions(t *testing.T) {
	od := getOpenRows(fixture())

	// no-break day: 2 blocks x 2 seats minus 2 booked = 2 free (8-10 both seats)
	w := getOpenWindow(od, day("2026-09-07"))
	bk := getBookings(fixture(), day("2026-09-07"), "Cached Owner")
	opts := getOpenOptions(day("2026-09-07"), w, bk, cars[0].Roles, 2, "Car1")
	var set []string
	for _, o := range opts {
		if o.Car != "Car1" {
			t.Errorf("option not tagged with car: %+v", o)
		}
		set = append(set, o.Start.Format("15:04")+"|"+o.ResourceID)
	}
	if got := strings.Join(set, ","); got != "08:00|84893,08:00|84894" {
		t.Errorf("no-break day seats = %s", got)
	}

	// break day: tiles from each sub-window; skips 13:00 (break) and 17:00 (past close)
	w = getOpenWindow(od, day("2026-09-08"))
	opts = getOpenOptions(day("2026-09-08"), w, nil, cars[0].Roles, 2, "Car1")
	if len(opts) != 8 {
		t.Fatalf("break day options = %d, want 8 (4 blocks x 2 seats)", len(opts))
	}
	starts := map[string]bool{}
	for _, o := range opts {
		starts[o.Start.Format("15:04")] = true
	}
	for _, want := range []string{"07:00", "09:00", "11:00", "15:00"} {
		if !starts[want] {
			t.Errorf("missing start %s", want)
		}
	}
	if starts["13:00"] || starts["17:00"] {
		t.Errorf("break/close not respected: %v", starts)
	}
}

func TestBuildBody(t *testing.T) {
	body := buildBody(time.Date(2026, 8, 24, 8, 0, 0, 0, time.UTC), 2, "84893", "Cached Owner", "555-111-2222", "555-333-4444")
	// exact field order + RFC3986 encoding (must match the known-good browser request)
	var keys []string
	for _, kv := range strings.Split(body, "&") {
		keys = append(keys, strings.SplitN(kv, "=", 2)[0])
	}
	wantOrder := "reservation%5Bstart_time%5D,reservation%5Bfinish_time%5D,reservation%5Bfull_name%5D,reservation%5Bphone%5D,reservation%5Bmobile%5D,reservation%5Bfield_1_r%5D,reservation%5Bresource_id%5D,button,reservation%5Bxpos%5D,reservation%5Bypos%5D"
	if got := strings.Join(keys, ","); got != wantOrder {
		t.Errorf("field order = %s", got)
	}
	for _, want := range []string{
		"reservation%5Bstart_time%5D=8%2F24%2F2026%208%3A00am",
		"reservation%5Bfinish_time%5D=8%2F24%2F2026%2010%3A00am",
		"reservation%5Bresource_id%5D=84893",
		"reservation%5Bfull_name%5D=Cached%20Owner",
		"reservation%5Bphone%5D=555-111-2222",
	} {
		if !strings.Contains(body, want) {
			t.Errorf("body missing %q\nbody: %s", want, body)
		}
	}
}

func TestEscapeData(t *testing.T) {
	if got := escapeData("8/24/2026 8:00am"); got != "8%2F24%2F2026%208%3A00am" {
		t.Errorf("escapeData = %s", got)
	}
	if got := escapeData("A-b_c.d~"); got != "A-b_c.d~" {
		t.Errorf("unreserved chars must pass through: %s", got)
	}
	// Multibyte UTF-8 is encoded one byte at a time (é = 0xC3 0xA9), which is
	// correct RFC 3986 output — the byte-wise loop in escapeData is intentional.
	if got := escapeData("José"); got != "Jos%C3%A9" {
		t.Errorf("multibyte UTF-8 = %s, want Jos%%C3%%A9", got)
	}
}

func TestClassify(t *testing.T) {
	cases := map[string]string{
		"Your reservation has been created successfully":                                       "success",
		"prohibited this reservation ... cannot be made more than 28 days in advance":          "retry",
		"We are sorry, but something went wrong":                                               "retry",
		"Sorry, this slot is no longer available":                                              "taken",
		"prohibited this reservation ... problems with the following fields: Name is required": "validation",
		"<div>just a schedule</div>":                                                           "unknown",
	}
	for in, want := range cases {
		if got := classify(in); got != want {
			t.Errorf("classify(%q) = %s, want %s", in, got, want)
		}
	}
}

func TestProfileFields(t *testing.T) {
	p := getProfileFields(fixture())
	if p.FullName != "O'Brien Test" { // HTML entity decoded
		t.Errorf("FullName = %q", p.FullName)
	}
	if p.Phone != "555-111-2222" || p.Mobile != "555-333-4444" {
		t.Errorf("phone/mobile = %q/%q", p.Phone, p.Mobile)
	}
	if q := getProfileFields("<html>no form here</html>"); q.FullName != "" {
		t.Errorf("absent form must yield blanks, got %q", q.FullName)
	}
}

func TestKeyringRoundTrip(t *testing.T) {
	const svc = "SuperSaaSSniper.test"
	if err := keyring.Set(svc, "probe", "v1\nv2"); err != nil {
		t.Skipf("keyring unavailable here: %v", err)
	}
	t.Cleanup(func() { _ = keyring.Delete(svc, "probe") })
	got, err := keyring.Get(svc, "probe")
	if err != nil || got != "v1\nv2" {
		t.Errorf("round-trip = %q, %v", got, err)
	}
}
