package app

// Tests for the flow steps extracted from main(): choice parsing, window
// derivation, messaging, the fetch-retry helper, the wait loop, and — most
// importantly — the attempt loop, driven end-to-end with fakes.

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"
)

// ----------------------------- parseChoices ---------------------------------

func TestParseChoices(t *testing.T) {
	cases := []struct {
		in   string
		n    int
		want []int
	}{
		{"3,1,4,2", 4, []int{3, 1, 4, 2}},
		{"99,3,x,1", 8, []int{3, 1}}, // garbage and out-of-range dropped
		{"A", 3, []int{1, 2, 3}},
		{"a", 2, []int{1, 2}},
		{" 2 , 1 ", 4, []int{2, 1}},
		{"", 4, nil},
		{"0,-1", 4, nil},
	}
	for _, c := range cases {
		if got := parseChoices(c.in, c.n); !reflect.DeepEqual(got, c.want) {
			t.Errorf("parseChoices(%q,%d) = %v, want %v", c.in, c.n, got, c.want)
		}
	}
}

func TestPreferenceOrder(t *testing.T) {
	opts := []seatOption{
		{Start: time.Date(2026, 8, 21, 15, 0, 0, 0, time.UTC), Car: "Car1", ResourceID: "84893", Role: "Drive-Observation"},
		{Start: time.Date(2026, 8, 21, 15, 0, 0, 0, time.UTC), Car: "Car2", ResourceID: "84951", Role: "Observation-Drive"},
		{Start: time.Date(2026, 8, 21, 17, 0, 0, 0, time.UTC), Car: "Car1", ResourceID: "84893", Role: "Drive-Observation"},
	}
	prefs, desc := preferenceOrder(opts, []int{3, 1})
	if len(prefs) != 2 || prefs[0].Start.Hour() != 17 || prefs[1].Start.Hour() != 15 {
		t.Errorf("prefs = %+v", prefs)
	}
	if desc != "5:00PM Car1 Drive-Observation > 3:00PM Car1 Drive-Observation" {
		t.Errorf("desc = %q", desc)
	}
}

// ----------------------------- deriveWindow ---------------------------------

func TestDeriveWindowDefaults(t *testing.T) {
	target := time.Date(2026, 8, 21, 0, 0, 0, 0, time.UTC)
	start, giveUp, err := deriveWindow(target, "", "")
	if err != nil {
		t.Fatal(err)
	}
	east, _ := time.LoadLocation("America/New_York")
	if got := start.In(east).Format("2006-01-02 15:04"); got != "2026-07-24 05:55" {
		t.Errorf("start = %s, want 28 days early at 05:55 ET", got)
	}
	if got := giveUp.In(east).Format("2006-01-02 15:04"); got != "2026-07-24 08:00" {
		t.Errorf("giveUp = %s", got)
	}
	// July is EDT (UTC-4), so 05:55 ET must be 09:55 UTC — proves real tz math.
	if got := start.UTC().Format("15:04"); got != "09:55" {
		t.Errorf("start UTC = %s, want 09:55", got)
	}
}

func TestDeriveWindowOverrides(t *testing.T) {
	target := time.Date(2026, 8, 21, 0, 0, 0, 0, time.UTC)
	start, _, err := deriveWindow(target, "2026-07-24 06:30:00", "")
	if err != nil {
		t.Fatal(err)
	}
	east, _ := time.LoadLocation("America/New_York")
	if got := start.In(east).Format("15:04"); got != "06:30" {
		t.Errorf("override start = %s", got)
	}
	if _, _, err := deriveWindow(target, "garbage", ""); err == nil {
		t.Error("expected error for bad override")
	}
}

// ----------------------------- messages -------------------------------------

func TestShortNoWindow(t *testing.T) {
	od := getOpenRows(fixture()) // covers 2026-09-07..08
	if got := shortNoWindow(od, day("2026-09-20")); !strings.Contains(got, "not published yet") {
		t.Errorf("beyond-range message = %q", got)
	}
	if got := shortNoWindow(od, day("2026-09-06")); !strings.Contains(got, "closed") {
		t.Errorf("closed message = %q", got)
	}
	if got := shortNoWindow(openData{}, day("2026-09-06")); !strings.Contains(got, "closed") {
		t.Errorf("empty-data message = %q", got)
	}
}

func TestDaySummary(t *testing.T) {
	od := getOpenRows(fixture())
	target := day("2026-09-08")
	win := getOpenWindow(od, target)
	booked := []booking{{
		Start: target.Add(7 * time.Hour), End: target.Add(9 * time.Hour),
		Resource: "84893", Owned: true, Name: "Cached Owner",
	}}
	s := daySummary(target, win, booked, 7)
	for _, want := range []string{"7:00AM-6:00PM", "(break 1:00PM-3:00PM)", "1 of 8 seats booked", "YOU already hold 7:00AM Drive-Observation"} {
		if !strings.Contains(s, want) {
			t.Errorf("daySummary missing %q in %q", want, s)
		}
	}
}

func TestRenderOptions(t *testing.T) {
	target := day("2026-09-07")
	opts := []seatOption{
		{Start: target.Add(8 * time.Hour), End: target.Add(10 * time.Hour), Car: "Car1", ResourceID: "84893", Role: "Drive-Observation"},
		{Start: target.Add(15 * time.Hour), End: target.Add(17 * time.Hour), Car: "Car2", ResourceID: "84951", Role: "Observation-Drive"},
	}
	out := renderOptions(target, opts, 2)
	for _, want := range []string{"Open 2-hour seats for Monday, September 7", "[ 1] 8:00AM - 10:00AM   Car1  Drive-Observation", "[ 2] 3:00PM - 5:00PM   Car2  Observation-Drive"} {
		if !strings.Contains(out, want) {
			t.Errorf("renderOptions missing %q in %q", want, out)
		}
	}
}

func TestDetectCar(t *testing.T) {
	if c := detectCar(fixture()); c.Name != "Car1" { // fixture uses Car1's ids
		t.Errorf("detectCar(fixture) = %s, want Car1", c.Name)
	}
	if c := detectCar(`...resource 84951 and 84952...`); c.Name != "Car2" {
		t.Errorf("detectCar(Car2 ids) = %s", c.Name)
	}
	if c := detectCar("<html>nothing recognizable</html>"); c.Name != cars[0].Name {
		t.Errorf("detectCar(unknown) = %s, want fallback %s", c.Name, cars[0].Name)
	}
}

// mineFixture (Car1): Aug 18 8-10 the user drives first (84893) with Isla Flynn
// observing (84894); Aug 20 3-5 the user observes first (84894) with the opposite
// seat still open; Aug 10 is a past booking that must be filtered out.
func mineFixture() string {
	owned := func(res string, start, end string) string {
		return fmt.Sprintf(`[%d,%d,%s,9,1,0,0,true,"me@x",1,"",1,"MeEmail","p","m","","c"],`, e(start), e(end), res)
	}
	other := func(res, name, start, end string) string {
		return fmt.Sprintf(`[%d,%d,%s,9,0,0,0,false,"%s"],`, e(start), e(end), res, name)
	}
	return "<script>ev=[" +
		owned("84893", "2026-08-18 08:00", "2026-08-18 10:00") +
		other("84894", "Isla Flynn", "2026-08-18 08:00", "2026-08-18 10:00") +
		owned("84894", "2026-08-20 15:00", "2026-08-20 17:00") +
		owned("84893", "2026-08-10 08:00", "2026-08-10 10:00") + // past
		`[0,0,0,0,0,0,0,false,""]];</script>`
}

func TestOppositeResource(t *testing.T) {
	if got := oppositeResource(cars[0], "84893"); got != "84894" {
		t.Errorf("opposite of 84893 = %s", got)
	}
	if got := oppositeResource(cars[0], "84894"); got != "84893" {
		t.Errorf("opposite of 84894 = %s", got)
	}
	if got := oppositeResource(cars[0], "99999"); got != "" {
		t.Errorf("opposite of unknown = %q", got)
	}
}

func TestGetAllBookingsVsDayFilter(t *testing.T) {
	all := getAllBookings(fixture(), "Owner")
	day7 := getBookings(fixture(), day("2026-09-07"), "Owner")
	if len(all) < len(day7) || len(day7) != 2 {
		t.Errorf("all=%d day7=%d", len(all), len(day7))
	}
}

func TestCollectMySlots(t *testing.T) {
	pages := []carPage{{cars[0], mineFixture()}}
	slots := collectMySlots(pages, "Me", day("2026-08-15"))
	if len(slots) != 2 {
		t.Fatalf("slots = %d, want 2 (past one filtered)", len(slots))
	}
	// Aug 18: user drives first, partner is Isla Flynn
	if slots[0].Start.Day() != 18 || slots[0].MyRole != "Drive-Observation" || slots[0].Partner != "Isla Flynn" {
		t.Errorf("slot0 = %+v", slots[0])
	}
	// Aug 20: user observes first, opposite seat open
	if slots[1].Start.Day() != 20 || slots[1].MyRole != "Observation-Drive" || slots[1].Partner != "" {
		t.Errorf("slot1 = %+v", slots[1])
	}
}

func TestRenderMySlots(t *testing.T) {
	slots := collectMySlots([]carPage{{cars[0], mineFixture()}}, "Me", day("2026-08-15"))
	out := renderMySlots(slots)
	for _, want := range []string{
		"Your booked lessons:",
		"Tue Aug 18   8:00AM-10:00AM   Car1",
		"you drive first   .   with Isla Flynn",
		"you observe first, drive second   .   opposite seat still open",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("renderMySlots missing %q\n--- got ---\n%s", want, out)
		}
	}
	if renderMySlots(nil) == "" || !strings.Contains(renderMySlots(nil), "No booked lessons") {
		t.Error("empty case should explain there are none")
	}
}

func TestDefaultWeekStart(t *testing.T) {
	// 28 days out from tomorrow = today + 29
	got := defaultWeekStart(time.Date(2026, 7, 25, 14, 30, 0, 0, time.Local))
	if want := day("2026-08-23"); !got.Equal(want) {
		t.Errorf("defaultWeekStart = %s, want %s", got.Format("2006-01-02"), want.Format("2006-01-02"))
	}
}

// weekFixtureCar1 covers Aug 23 (8-12, one 8-10 seat booked), Aug 24 (8-12,
// the 10-12 block fully booked), and Aug 26 (3-7pm, nothing booked). Aug 25 has
// no row (closed); Aug 27+ are past the max (not published).
func weekFixtureCar1() string {
	ecache := fmt.Sprintf("ecache = {data: [[%d,%d,1,480,720],[%d,%d,1,480,720],[%d,%d,1,900,1140]]}",
		e("2026-08-23"), e("2026-08-23 23:59"),
		e("2026-08-24"), e("2026-08-24 23:59"),
		e("2026-08-26"), e("2026-08-26 23:59"))
	bookings := fmt.Sprintf(`ev = [`+
		`[%d,%d,84893,1,0,0,0,false,"A"],`+ // Aug 23 8-10 res 84893 booked
		`[%d,%d,84893,2,0,0,0,false,"B"],[%d,%d,84894,3,0,0,0,false,"C"]`+ // Aug 24 10-12 both booked
		`];`,
		e("2026-08-23 08:00"), e("2026-08-23 10:00"),
		e("2026-08-24 10:00"), e("2026-08-24 12:00"), e("2026-08-24 10:00"), e("2026-08-24 12:00"))
	return "<script>" + ecache + "\n" + bookings + "</script>"
}

func TestCarDayLine(t *testing.T) {
	page := weekFixtureCar1()
	od := getOpenRows(page)
	c := cars[0]
	cases := map[string]string{
		"2026-08-23": "8:00AM-10:00AM (1), 10:00AM-12:00PM (2)", // one 8-10 seat taken
		"2026-08-24": "8:00AM-10:00AM (2)",                      // 10-12 block full
		"2026-08-25": "closed / no availability that day",       // no row, within range
		"2026-08-26": "3:00PM-5:00PM (2), 5:00PM-7:00PM (2)",    // afternoon day, nothing booked
	}
	for d, want := range cases {
		if got := carDayLine(page, od, c, day(d), 2); got != want {
			t.Errorf("carDayLine(%s) = %q, want %q", d, got, want)
		}
	}
	// beyond the published range
	if got := carDayLine(page, od, c, day("2026-08-28"), 2); !strings.Contains(got, "not published yet") {
		t.Errorf("beyond-range = %q", got)
	}
}

func TestRenderWeek(t *testing.T) {
	pages := []carPage{
		{cars[0], weekFixtureCar1()},
		{cars[1], "<html>no schedule</html>"}, // Car2: everything closed
	}
	out := renderWeek(pages, day("2026-08-23"), 7, 2)
	for _, want := range []string{
		"Availability Sun Aug 23 - Sat Aug 29",
		"Sun Aug 23\n  Car1  8:00AM-10:00AM (1), 10:00AM-12:00PM (2)\n  Car2  closed",
		"Mon Aug 24\n  Car1  8:00AM-10:00AM (2)",
		"Wed Aug 26\n  Car1  3:00PM-5:00PM (2), 5:00PM-7:00PM (2)",
		"bookings load only ~30 days out",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("renderWeek missing:\n%q\n--- full output ---\n%s", want, out)
		}
	}
	// 7 day headers present
	if n := strings.Count(out, "  Car1  "); n != 7 {
		t.Errorf("expected 7 Car1 lines, got %d", n)
	}
}

func TestCollectOptionsAcrossCars(t *testing.T) {
	// Car1 page = the fixture (Sep 7: 8-12, both 10-12 seats booked -> 8-10 free);
	// Car2 page = a synthetic 3-7pm day with nothing booked.
	car2 := fmt.Sprintf(`<html><script>ecache = {data: [[%d,%d,1,900,1140]]}</script></html>`,
		e("2026-09-07"), e("2026-09-07 23:59"))
	pages := []carPage{
		{cars[0], fixture()},
		{cars[1], car2},
	}
	opts := collectOptions(pages, day("2026-09-07"), 2, "Owner")
	// Car1: 8-10 x2 seats; Car2: 3-5,5-7 x2 seats = 6 total
	if len(opts) != 6 {
		t.Fatalf("total options = %d, want 6", len(opts))
	}
	// stable order: morning Car1 seats first, then afternoon Car2 seats
	if opts[0].Car != "Car1" || opts[0].Start.Hour() != 8 {
		t.Errorf("first option = %+v", opts[0])
	}
	if opts[len(opts)-1].Car != "Car2" || opts[len(opts)-1].Start.Hour() != 17 {
		t.Errorf("last option = %+v", opts[len(opts)-1])
	}
}

// ----------------------------- fetchWithRetry -------------------------------

func TestFetchWithRetry(t *testing.T) {
	clock := time.Date(2026, 7, 27, 6, 30, 0, 0, time.UTC)
	now := func() time.Time { return clock }
	slept := 0
	sleep := func(d time.Duration) { slept++; clock = clock.Add(d) }

	calls := 0
	fetchFn := func() (string, string, error) {
		calls++
		if calls < 3 {
			return "", "", errors.New("wifi not up yet")
		}
		return "PAGE", "url", nil
	}
	page, _, err := fetchWithRetry(fetchFn, clock.Add(time.Minute), 3*time.Second, now, sleep)
	if err != nil || page != "PAGE" || calls != 3 || slept != 2 {
		t.Errorf("page=%q err=%v calls=%d slept=%d", page, err, calls, slept)
	}

	// deadline exceeded -> surfaces the fetch error
	calls = 0
	alwaysFail := func() (string, string, error) { return "", "", errors.New("down") }
	if _, _, err := fetchWithRetry(alwaysFail, clock.Add(5*time.Second), 3*time.Second, now, sleep); err == nil {
		t.Error("expected error after deadline")
	}
}

// ----------------------------- loadPage -------------------------------------

type loadHarness struct {
	pages    []string // scripted source responses, consumed in order
	errs     []error  // scripted source errors (nil = use page)
	calls    int
	reauthOK bool
	reauths  int
	saves    int
	notices  []string
}

func (h *loadHarness) src() pageSource {
	return func() (string, string, error) {
		i := h.calls
		h.calls++
		var err error
		if i < len(h.errs) {
			err = h.errs[i]
		}
		page := ""
		if i < len(h.pages) {
			page = h.pages[i]
		}
		return page, "https://www.supersaas.com/schedule/x", err
	}
}

func (h *loadHarness) deps(deadline time.Time) loadDeps {
	return loadDeps{
		deadline:    deadline,
		retryEvery:  time.Second,
		now:         time.Now,
		sleep:       func(time.Duration) {},
		reauth:      func() bool { h.reauths++; return h.reauthOK },
		saveCookies: func() { h.saves++ },
		notify:      func(title, _ string) { h.notices = append(h.notices, title) },
	}
}

func TestLoadPageCleanLoadCachesCookies(t *testing.T) {
	h := &loadHarness{pages: []string{fixture()}}
	page, err := loadPage(h.src(), h.deps(time.Now().Add(time.Minute)))
	if err != nil || !strings.Contains(page, "ecache") {
		t.Fatalf("page=%.40q err=%v", page, err)
	}
	if h.reauths != 0 || h.saves != 1 {
		t.Errorf("reauths=%d saves=%d, want 0/1", h.reauths, h.saves)
	}
}

func TestLoadPageReauthRecovers(t *testing.T) {
	h := &loadHarness{pages: []string{pageLogin, fixture()}, reauthOK: true}
	page, err := loadPage(h.src(), h.deps(time.Now().Add(time.Minute)))
	if err != nil || !strings.Contains(page, "ecache") {
		t.Fatalf("page=%.40q err=%v", page, err)
	}
	if h.reauths != 1 || h.saves != 1 || h.calls != 2 {
		t.Errorf("reauths=%d saves=%d calls=%d, want 1/1/2", h.reauths, h.saves, h.calls)
	}
}

func TestLoadPageReauthFailureDoesNotCacheCookies(t *testing.T) {
	h := &loadHarness{pages: []string{pageLogin}, reauthOK: false}
	if _, err := loadPage(h.src(), h.deps(time.Now().Add(time.Minute))); err != nil {
		t.Fatalf("err=%v (a login page is still a page)", err)
	}
	if h.saves != 0 {
		t.Error("must NOT cache cookies for a logged-out page")
	}
	if len(h.notices) != 1 || !strings.Contains(h.notices[0], "login needed") {
		t.Errorf("notices = %v", h.notices)
	}
}

func TestLoadPageNetworkDownNotifies(t *testing.T) {
	h := &loadHarness{errs: []error{errors.New("down")}}
	_, err := loadPage(h.src(), h.deps(time.Time{})) // zero deadline: single attempt
	if err == nil || h.calls != 1 {
		t.Fatalf("err=%v calls=%d", err, h.calls)
	}
	if len(h.notices) != 1 || !strings.Contains(h.notices[0], "network down") {
		t.Errorf("notices = %v", h.notices)
	}
}

func TestFileSource(t *testing.T) {
	p := filepath.Join(t.TempDir(), "day.html")
	if err := os.WriteFile(p, []byte(fixture()), 0o644); err != nil {
		t.Fatal(err)
	}
	page, err := loadPage(fileSource(p), fileLoadDeps())
	if err != nil || !strings.Contains(page, "ecache") {
		t.Fatalf("page=%.40q err=%v", page, err)
	}
	if _, err := loadPage(fileSource(filepath.Join(t.TempDir(), "missing.html")), fileLoadDeps()); err == nil {
		t.Error("missing file must error (single attempt, no retry loop)")
	}
}

// ----------------------------- resolveProfile -------------------------------

func TestResolveProfile(t *testing.T) {
	// scraped from page wins
	p, scraped := resolveProfile(fixture(), func() (profileInfo, bool) {
		return profileInfo{FullName: "Cache Hit"}, true
	})
	if !scraped || p.FullName != "O'Brien Test" {
		t.Errorf("scrape path: %+v scraped=%v", p, scraped)
	}
	// no form -> cache fallback
	p, scraped = resolveProfile("<html/>", func() (profileInfo, bool) {
		return profileInfo{FullName: "Cache Hit", Phone: "1"}, true
	})
	if scraped || p.FullName != "Cache Hit" {
		t.Errorf("cache path: %+v scraped=%v", p, scraped)
	}
	// neither
	p, _ = resolveProfile("<html/>", func() (profileInfo, bool) { return profileInfo{}, false })
	if p.FullName != "" {
		t.Errorf("empty path: %+v", p)
	}
}

// ----------------------------- waitForWindow --------------------------------

func TestWaitForWindow(t *testing.T) {
	clock := time.Date(2026, 7, 27, 6, 54, 0, 0, time.UTC)
	start := time.Date(2026, 7, 27, 6, 55, 0, 0, time.UTC)
	now := func() time.Time { return clock }
	var sleeps []time.Duration
	sleep := func(d time.Duration) { sleeps = append(sleeps, d); clock = clock.Add(d) }
	var progress []string
	waitForWindow(start, now, sleep, func(s string) { progress = append(progress, s) })

	if clock.Before(start) {
		t.Error("returned before the window opened")
	}
	for _, d := range sleeps {
		if d > 30*time.Second || d < time.Second {
			t.Errorf("sleep out of bounds: %v", d)
		}
	}
	if len(progress) == 0 || !strings.Contains(progress[0], "Waiting...") {
		t.Errorf("no countdown emitted: %v", progress)
	}
}

// ----------------------------- runAttempts ----------------------------------

const (
	pageSuccess = "Your reservation has been created successfully"
	pageRetry   = "Reservations cannot be made more than 28 days in advance"
	pageTaken   = "Sorry, this slot is no longer available"
	pageLogin   = `<form><input type="password" name="password" /></form>`
	pageUnknown = "<div>weird empty page</div>"
)

type attemptHarness struct {
	clock    time.Time
	posted   []string // resource ids in POST order
	pages    []string // scripted responses, consumed in order
	errs     []error  // scripted errors (nil = use page)
	reauths  int
	reauthOK bool
	notices  []string
	sleeps   int
}

func (h *attemptHarness) deps() attemptDeps {
	return attemptDeps{
		post: func(slot seatOption) (string, string, error) {
			i := len(h.posted)
			h.posted = append(h.posted, slot.ResourceID)
			var err error
			if i < len(h.errs) {
				err = h.errs[i]
			}
			page := ""
			if i < len(h.pages) {
				page = h.pages[i]
			}
			return page, "https://www.supersaas.com/schedule/x", err
		},
		saveHTML: func(a int, p string) string { return fmt.Sprintf("mem_%d", a) },
		reauth:   func() bool { h.reauths++; return h.reauthOK },
		sleep:    func(d time.Duration) { h.sleeps++; h.clock = h.clock.Add(d) },
		now:      func() time.Time { return h.clock },
		notify:   func(title, msg string) { h.notices = append(h.notices, title) },
	}
}

func testPrefs() []seatOption {
	d := day("2026-08-24")
	return []seatOption{
		{Start: d.Add(8 * time.Hour), End: d.Add(10 * time.Hour), Car: "Car1", ResourceID: "84893", Role: "Drive-Observation"},
		{Start: d.Add(8 * time.Hour), End: d.Add(10 * time.Hour), Car: "Car1", ResourceID: "84894", Role: "Observation-Drive"},
	}
}

func newHarness(pages ...string) *attemptHarness {
	return &attemptHarness{clock: day("2026-08-24"), pages: pages, reauthOK: true}
}

func TestRunAttemptsImmediateSuccess(t *testing.T) {
	h := newHarness(pageSuccess)
	out, attempts := runAttempts(testPrefs(), day("2026-08-24"), h.clock.Add(time.Hour), 3*time.Second, h.deps())
	if out != outcomeBooked || attempts != 1 {
		t.Errorf("out=%v attempts=%d", out, attempts)
	}
	if len(h.notices) != 1 || !strings.HasPrefix(h.notices[0], "BOOKED") {
		t.Errorf("notices = %v", h.notices)
	}
}

func TestRunAttemptsRetryThenSuccess(t *testing.T) {
	h := newHarness(pageRetry, pageRetry, pageSuccess)
	out, attempts := runAttempts(testPrefs(), day("2026-08-24"), h.clock.Add(time.Hour), 3*time.Second, h.deps())
	if out != outcomeBooked || attempts != 3 || h.sleeps != 2 {
		t.Errorf("out=%v attempts=%d sleeps=%d", out, attempts, h.sleeps)
	}
	// retries must stay on the SAME seat
	if !reflect.DeepEqual(h.posted, []string{"84893", "84893", "84893"}) {
		t.Errorf("posted = %v", h.posted)
	}
}

func TestRunAttemptsTakenFallsThrough(t *testing.T) {
	h := newHarness(pageTaken, pageSuccess)
	out, _ := runAttempts(testPrefs(), day("2026-08-24"), h.clock.Add(time.Hour), 3*time.Second, h.deps())
	if out != outcomeBooked {
		t.Errorf("out = %v", out)
	}
	// choice #1 taken -> moved to choice #2
	if !reflect.DeepEqual(h.posted, []string{"84893", "84894"}) {
		t.Errorf("posted = %v", h.posted)
	}
}

func TestRunAttemptsLoginReauthDoesNotBurnChoice(t *testing.T) {
	h := newHarness(pageLogin, pageSuccess)
	out, _ := runAttempts(testPrefs(), day("2026-08-24"), h.clock.Add(time.Hour), 3*time.Second, h.deps())
	if out != outcomeBooked || h.reauths != 1 {
		t.Errorf("out=%v reauths=%d", out, h.reauths)
	}
	// the SAME seat is retried after reauth — an auth blip must not burn a preference
	if !reflect.DeepEqual(h.posted, []string{"84893", "84893"}) {
		t.Errorf("posted = %v", h.posted)
	}
}

func TestRunAttemptsUnknownStops(t *testing.T) {
	h := newHarness(pageUnknown, pageSuccess) // success is queued but must never be reached
	out, attempts := runAttempts(testPrefs(), day("2026-08-24"), h.clock.Add(time.Hour), 3*time.Second, h.deps())
	if out != outcomeNeedsVerify || attempts != 1 {
		t.Errorf("out=%v attempts=%d — unknown page must stop the loop (double-booking risk)", out, attempts)
	}
	if len(h.notices) != 1 || !strings.Contains(h.notices[0], "verification") {
		t.Errorf("notices = %v", h.notices)
	}
}

func TestRunAttemptsAllTaken(t *testing.T) {
	h := newHarness(pageTaken, pageTaken)
	out, _ := runAttempts(testPrefs(), day("2026-08-24"), h.clock.Add(time.Hour), 3*time.Second, h.deps())
	if out != outcomeAllTaken {
		t.Errorf("out = %v", out)
	}
}

func TestRunAttemptsGiveUp(t *testing.T) {
	h := newHarness()
	// every attempt is a retry page; give up after 10 seconds of clock
	for i := 0; i < 100; i++ {
		h.pages = append(h.pages, pageRetry)
	}
	out, attempts := runAttempts(testPrefs(), day("2026-08-24"), h.clock.Add(10*time.Second), 3*time.Second, h.deps())
	if out != outcomeGaveUp {
		t.Errorf("out = %v", out)
	}
	if attempts < 3 || attempts > 5 {
		t.Errorf("attempts = %d, expected ~4 in a 10s window at 3s cadence", attempts)
	}
}

func TestRunAttemptsRequestErrorRetriesSameSeat(t *testing.T) {
	h := newHarness("", pageSuccess)
	h.errs = []error{errors.New("connection reset"), nil}
	out, _ := runAttempts(testPrefs(), day("2026-08-24"), h.clock.Add(time.Hour), 3*time.Second, h.deps())
	if out != outcomeBooked {
		t.Errorf("out = %v", out)
	}
	if !reflect.DeepEqual(h.posted, []string{"84893", "84893"}) {
		t.Errorf("posted = %v", h.posted)
	}
}
