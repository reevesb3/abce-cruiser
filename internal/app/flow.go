package app

// Orchestration steps for the booking flow, factored out of main() so each
// piece is independently testable. Anything that touches the clock, network,
// or notifications takes those as injected dependencies; everything else is
// a pure function of its inputs (see flow_test.go).

import (
	"bufio"
	"errors"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"
)

func carDayURL(c car, target time.Time) string {
	return fmt.Sprintf("%s/%s?view=day&day=%d&month=%d", scheduleBase, c.Name, target.Day(), int(target.Month()))
}

// roleLabel resolves a resource id to its seat label across all cars.
func roleLabel(resourceID string) string {
	for _, c := range cars {
		for _, r := range c.Roles {
			if r.ID == resourceID {
				return r.Label
			}
		}
	}
	return resourceID
}

func minsClock(day time.Time, mins int) string {
	return day.Add(time.Duration(mins) * time.Minute).Format("3:04PM")
}

func fmtHMS(d time.Duration) string {
	d = d.Round(time.Second)
	return fmt.Sprintf("%02d:%02d:%02d", int(d.Hours()), int(d.Minutes())%60, int(d.Seconds())%60)
}

// ----------------------------- page loading ---------------------------------

// fetchWithRetry keeps calling fetchFn until it succeeds or the deadline
// passes, so a launch-time network hiccup (Wi-Fi still reconnecting after
// wake) doesn't kill an armed run.
func fetchWithRetry(fetchFn func() (string, string, error), deadline time.Time,
	retryEvery time.Duration, now func() time.Time, sleep func(time.Duration)) (string, string, error) {
	for {
		page, finalURL, err := fetchFn()
		if err == nil {
			return page, finalURL, nil
		}
		if now().After(deadline) {
			return "", "", err
		}
		logf("Day-view fetch failed (%v) - retrying in %s...", err, retryEvery)
		sleep(retryEvery)
	}
}

// pageSource retrieves the day-view HTML. finalURL is the post-redirect URL,
// used to detect a bounce to the login page (empty for non-network sources).
type pageSource func() (page, finalURL string, err error)

func fileSource(path string) pageSource {
	return func() (string, string, error) {
		b, err := os.ReadFile(path)
		return string(b), "", err
	}
}

func liveSource(client *http.Client, dayURL string) pageSource {
	return func() (string, string, error) {
		return fetch(client, http.MethodGet, dayURL, "", dayURL)
	}
}

// loadDeps carries loadPage's policies and side effects, injectable for tests.
type loadDeps struct {
	deadline    time.Time // keep retrying failed retrievals until this passes
	retryEvery  time.Duration
	now         func() time.Time
	sleep       func(time.Duration)
	reauth      func() bool
	saveCookies func()
	notify      func(title, msg string)
}

// loadPage runs the retrieval pipeline around any pageSource: retry until the
// deadline, reauthenticate once if the session has expired, and cache cookies
// after a clean (logged-in) load.
func loadPage(src pageSource, d loadDeps) (string, error) {
	page, finalURL, err := fetchWithRetry(src, d.deadline, d.retryEvery, d.now, d.sleep)
	if err != nil {
		d.notify("Cruiser: network down", "Could not reach the schedule to load slots - check the connection.")
		return "", fmt.Errorf("failed to load the schedule day view: %v", err)
	}
	if isLoginPage(page, finalURL) {
		logf("Schedule requires login - session cookie expired. Attempting reauth...")
		if d.reauth() {
			if page, finalURL, err = src(); err != nil {
				return "", fmt.Errorf("reload after reauth failed: %v", err)
			}
		} else {
			logf("WARNING: could not reauthenticate. Set up a stored login with -save-login.")
			d.notify("Cruiser: login needed", "Session expired and auto-reauth failed - check stored login.")
		}
	}
	if !isLoginPage(page, finalURL) {
		d.saveCookies()
	}
	return page, nil
}

// carPage is one car's day-view HTML, ready for slot computation.
type carPage struct {
	car  car
	page string
}

// loadCarPages retrieves the day view for every car (or a single local file for
// offline testing). Cookies are loaded once up front; the first live fetch
// handles any reauth, and the refreshed session carries to the rest.
func loadCarPages(cfg config, client *http.Client, target time.Time) ([]carPage, error) {
	if cfg.fromFile != "" {
		logf("Reading day view from file: %s", cfg.fromFile)
		page, err := loadPage(fileSource(cfg.fromFile), fileLoadDeps())
		if err != nil {
			return nil, err
		}
		return []carPage{{detectCar(page), page}}, nil
	}
	if loadCookiesIntoJar(client) {
		logf("Loaded cached session cookie (OS credential store).")
	}
	pages := make([]carPage, 0, len(cars))
	for _, c := range cars {
		page, err := loadPage(liveSource(client, carDayURL(c, target)), liveLoadDeps(cfg, client))
		if err != nil {
			return nil, fmt.Errorf("%s: %w", c.Name, err)
		}
		pages = append(pages, carPage{c, page})
	}
	return pages, nil
}

// detectCar identifies which car a page belongs to by the resource ids present
// (used for the single-page -from-file path). Falls back to the first car.
func detectCar(page string) car {
	for _, c := range cars {
		for _, r := range c.Roles {
			if strings.Contains(page, r.ID) {
				return c
			}
		}
	}
	return cars[0]
}

// collectOptions computes the open seats across all cars, logging a one-line
// availability summary per car, and returns the aggregated seat list in a
// stable time-then-car order.
func collectOptions(pages []carPage, target time.Time, slotHours float64, ownerName string) []seatOption {
	var all []seatOption
	for _, cp := range pages {
		od := getOpenRows(cp.page)
		win := getOpenWindow(od, target)
		booked := getBookings(cp.page, target, ownerName)
		if win == nil {
			logf("%s: %s", cp.car.Name, shortNoWindow(od, target))
			continue
		}
		opts := getOpenOptions(target, win, booked, cp.car.Roles, slotHours, cp.car.Name)
		logf("%s: %s", cp.car.Name, daySummary(target, win, booked, len(opts)))
		all = append(all, opts...)
	}
	sortOptions(all)
	return all
}

// liveLoadDeps: interactive modes fail fast; an armed run retries for up to
// 20 minutes (Wi-Fi may still be reconnecting after the PC wakes).
func liveLoadDeps(cfg config, client *http.Client) loadDeps {
	limit := 20 * time.Minute
	if cfg.listOnly || cfg.dryRun {
		limit = time.Minute
	}
	return loadDeps{
		deadline:    time.Now().Add(limit),
		retryEvery:  cfg.retryEvery(),
		now:         time.Now,
		sleep:       time.Sleep,
		reauth:      func() bool { return restoreSession(client) },
		saveCookies: func() { saveCookiesFromJar(client) },
		notify:      toast,
	}
}

// fileLoadDeps: a local file can't be retried into existence, reauthenticated,
// or cookie-cached — single attempt (zero deadline is already past), no-op hooks.
func fileLoadDeps() loadDeps {
	return loadDeps{
		now:         time.Now,
		sleep:       func(time.Duration) {},
		reauth:      func() bool { return false },
		saveCookies: func() {},
		notify:      func(string, string) {},
	}
}

// resolveProfile prefers details scraped from the logged-in page (they're
// authoritative) and falls back to the cache. Returns whether the result came
// from a fresh scrape (callers cache it then).
func resolveProfile(page string, cached func() (profileInfo, bool)) (profileInfo, bool) {
	if p := getProfileFields(page); p.FullName != "" {
		return p, true
	}
	if p, ok := cached(); ok {
		return p, false
	}
	return profileInfo{}, false
}

// ----------------------------- availability text ----------------------------

// shortNoWindow is the one-line reason a car has no seats on the target day,
// used in the per-car availability summary. We never invent hours: a missing
// open-hours row means the day is beyond the published range or simply closed.
func shortNoWindow(od openData, target time.Time) string {
	if !od.Max.IsZero() && target.After(od.Max) {
		return fmt.Sprintf("not published yet (schedule only goes to %s)", od.Max.Format("Jan 2"))
	}
	return "closed / no availability that day"
}

func daySummary(target time.Time, win *openRow, booked []booking, optCount int) string {
	breakNote := ""
	if win.BreakEnd > win.BreakStart {
		breakNote = fmt.Sprintf(" (break %s-%s)", minsClock(target, win.BreakStart), minsClock(target, win.BreakEnd))
	}
	var mine []string
	for _, b := range booked {
		if b.Owned {
			mine = append(mine, fmt.Sprintf("%s %s", b.Start.Format("3:04PM"), roleLabel(b.Resource)))
		}
	}
	mineNote := ""
	if len(mine) > 0 {
		mineNote = "; YOU already hold " + strings.Join(mine, ", ")
	}
	return fmt.Sprintf("Open hours %s-%s%s.  %d of %d seats booked%s.",
		minsClock(target, win.Open), minsClock(target, win.Close), breakNote, len(booked), len(booked)+optCount, mineNote)
}

func renderOptions(target time.Time, opts []seatOption, slotHours float64) string {
	var b strings.Builder
	fmt.Fprintf(&b, "\nOpen %g-hour seats for %s:\n", slotHours, target.Format("Monday, January 2"))
	for i, o := range opts {
		fmt.Fprintf(&b, "  [%2d] %s - %s   %-4s  %s\n", i+1, o.Start.Format("3:04PM"), o.End.Format("3:04PM"), o.Car, o.Role)
	}
	b.WriteString("\n")
	return b.String()
}

// seatLabel is the compact "3:00PM Car2 Drive-Observation" form used in the
// preference list, log lines, and toasts.
func seatLabel(o seatOption) string {
	return fmt.Sprintf("%s %s %s", o.Start.Format("3:04PM"), o.Car, o.Role)
}

// renderWeek shows availability for `days` days starting at weekStart, one line
// per car per day. Each car's page is parsed once (getOpenRows) and reused
// across the whole week — no per-day refetch.
func renderWeek(pages []carPage, weekStart time.Time, days int, slotHours float64) string {
	type parsed struct {
		c    car
		page string
		od   openData
	}
	cds := make([]parsed, len(pages))
	for i, cp := range pages {
		cds[i] = parsed{cp.car, cp.page, getOpenRows(cp.page)}
	}

	var b strings.Builder
	end := weekStart.AddDate(0, 0, days-1)
	fmt.Fprintf(&b, "\nAvailability %s - %s  (%d cars, %g-hour slots; NN = open seats):\n\n",
		weekStart.Format("Mon Jan 2"), end.Format("Mon Jan 2"), len(pages), slotHours)
	for i := 0; i < days; i++ {
		d := weekStart.AddDate(0, 0, i)
		fmt.Fprintf(&b, "%s\n", d.Format("Mon Jan 2"))
		for _, p := range cds {
			fmt.Fprintf(&b, "  %-5s %s\n", p.c.Name, carDayLine(p.page, p.od, p.c, d, slotHours))
		}
	}
	fmt.Fprintf(&b, "\nNote: individual bookings load only ~30 days out, so days past that show\n"+
		"scheduled open hours (they fill in as each day's booking window nears).\n")
	return b.String()
}

// carDayLine summarizes one car's open seats on one day: the open time blocks
// with their open-seat counts, or "full" / a closed/not-published reason.
func carDayLine(page string, od openData, c car, day time.Time, slotHours float64) string {
	win := getOpenWindow(od, day)
	if win == nil {
		return shortNoWindow(od, day)
	}
	opts := getOpenOptions(day, win, getBookings(page, day, ""), c.Roles, slotHours, c.Name)
	if len(opts) == 0 {
		return "full"
	}
	var order []string
	count := map[string]int{}
	for _, o := range opts {
		k := fmt.Sprintf("%s-%s", o.Start.Format("3:04PM"), o.End.Format("3:04PM"))
		if count[k] == 0 {
			order = append(order, k)
		}
		count[k]++
	}
	parts := make([]string, len(order))
	for i, k := range order {
		parts[i] = fmt.Sprintf("%s (%d)", k, count[k])
	}
	return strings.Join(parts, ", ")
}

// ----------------------------- my slots -------------------------------------

// mySlot is one reservation the logged-in user holds, plus who (if anyone) has
// the opposite seat in the same car and time block.
type mySlot struct {
	Start, End time.Time
	Car        string
	MyRole     string // seat label of the user's booking
	Partner    string // name in the opposite seat, or "" if it's still open
}

// seatPhrase turns a seat label into plain English.
func seatPhrase(label string) string {
	switch label {
	case "Drive-Observation":
		return "drive first"
	case "Observation-Drive":
		return "observe first, drive second"
	}
	return label
}

// collectMySlots finds the user's own bookings across all cars (from `from`
// onward) and pairs each with the person in the opposite seat.
func collectMySlots(pages []carPage, ownerName string, from time.Time) []mySlot {
	var out []mySlot
	for _, cp := range pages {
		all := getAllBookings(cp.page, ownerName)
		// index by resource + start so we can find the opposite seat's booking
		byKey := make(map[string]booking, len(all))
		for _, b := range all {
			byKey[b.Resource+"@"+b.Start.Format(time.RFC3339)] = b
		}
		for _, b := range all {
			if !b.Owned || b.Start.Before(from) {
				continue
			}
			oppID := oppositeResource(cp.car, b.Resource)
			partner := ""
			if p, ok := byKey[oppID+"@"+b.Start.Format(time.RFC3339)]; ok {
				partner = p.Name
			}
			out = append(out, mySlot{
				Start: b.Start, End: b.End, Car: cp.car.Name,
				MyRole: roleLabel(b.Resource), Partner: partner,
			})
		}
	}
	sort.Slice(out, func(i, j int) bool {
		if !out[i].Start.Equal(out[j].Start) {
			return out[i].Start.Before(out[j].Start)
		}
		return out[i].Car < out[j].Car
	})
	return out
}

func renderMySlots(slots []mySlot) string {
	if len(slots) == 0 {
		return "\nNo booked lessons found (within the ~30-day range the schedule loads).\n"
	}
	var b strings.Builder
	b.WriteString("\nYour booked lessons:\n\n")
	for _, s := range slots {
		partner := "with " + s.Partner
		if s.Partner == "" {
			partner = "opposite seat still open"
		}
		fmt.Fprintf(&b, "  %s   %s-%s   %s\n", s.Start.Format("Mon Jan 2"), s.Start.Format("3:04PM"), s.End.Format("3:04PM"), s.Car)
		fmt.Fprintf(&b, "       you %s   .   %s\n", seatPhrase(s.MyRole), partner)
	}
	return b.String()
}

// ----------------------------- preferences ----------------------------------

var choiceSplitRe = regexp.MustCompile(`[,\s]+`)

// parseChoices turns "3,1,4,2" into 1-based indexes, dropping garbage and
// out-of-range values; "A"/"a" means all n options in listed order.
func parseChoices(input string, n int) []int {
	input = strings.TrimSpace(input)
	if strings.EqualFold(input, "a") {
		picks := make([]int, n)
		for i := range picks {
			picks[i] = i + 1
		}
		return picks
	}
	var picks []int
	for _, tok := range choiceSplitRe.Split(input, -1) {
		if v, err := strconv.Atoi(tok); err == nil && v >= 1 && v <= n {
			picks = append(picks, v)
		}
	}
	return picks
}

func promptChoices() string {
	fmt.Print("Enter your preferred slot numbers in order (e.g. 3,1,2), or 'A' for all in listed order: ")
	line, _ := bufio.NewReader(os.Stdin).ReadString('\n')
	return strings.TrimSpace(line)
}

// preferenceOrder maps validated picks onto the option list and builds the
// human-readable "X > Y > Z" description.
func preferenceOrder(opts []seatOption, picks []int) ([]seatOption, string) {
	prefs := make([]seatOption, 0, len(picks))
	var desc []string
	for _, n := range picks {
		prefs = append(prefs, opts[n-1])
		desc = append(desc, seatLabel(opts[n-1]))
	}
	return prefs, strings.Join(desc, " > ")
}

// ----------------------------- attack window --------------------------------

// deriveWindow computes when to start and stop hammering, in the schedule's
// timezone (US Eastern): defaults are 05:55–08:00 ET on the day the 28-day
// booking window opens, overridable via the -start-eastern/-giveup-eastern flags.
func deriveWindow(target time.Time, startOverride, giveupOverride string) (time.Time, time.Time, error) {
	east, err := time.LoadLocation("America/New_York")
	if err != nil {
		return time.Time{}, time.Time{}, fmt.Errorf("could not load Eastern timezone: %v", err)
	}
	openDay := target.AddDate(0, 0, -28)
	start := time.Date(openDay.Year(), openDay.Month(), openDay.Day(), 5, 55, 0, 0, east)
	giveUp := time.Date(openDay.Year(), openDay.Month(), openDay.Day(), 8, 0, 0, 0, east)
	if startOverride != "" {
		if start, err = time.ParseInLocation("2006-01-02 15:04:05", startOverride, east); err != nil {
			return time.Time{}, time.Time{}, fmt.Errorf("bad -start-eastern: %v", err)
		}
	}
	if giveupOverride != "" {
		if giveUp, err = time.ParseInLocation("2006-01-02 15:04:05", giveupOverride, east); err != nil {
			return time.Time{}, time.Time{}, fmt.Errorf("bad -giveup-eastern: %v", err)
		}
	}
	return start, giveUp, nil
}

// waitForWindow blocks (via the injected sleeper) until start, emitting a
// countdown through progress.
func waitForWindow(start time.Time, now func() time.Time, sleep func(time.Duration), progress func(string)) {
	for {
		left := start.Sub(now())
		if left <= 0 {
			break
		}
		progress(fmt.Sprintf("\rWaiting... %s until attempts begin ", fmtHMS(left)))
		s := left - time.Second
		if s > 30*time.Second {
			s = 30 * time.Second
		}
		if s < time.Second {
			s = time.Second
		}
		sleep(s)
	}
	progress("\n")
}

// ----------------------------- attempt loop ---------------------------------

type attemptOutcome int

const (
	outcomeBooked      attemptOutcome = iota // positive confirmation received
	outcomeNeedsVerify                       // unrecognized page: stopped to avoid double-booking
	outcomeAllTaken                          // every preferred seat was gone
	outcomeGaveUp                            // hit the give-up time
)

// attemptDeps is everything runAttempts needs from the outside world, so the
// loop's decision logic is testable with fakes.
type attemptDeps struct {
	post     func(slot seatOption) (page, finalURL string, err error)
	saveHTML func(attempt int, page string) string // returns saved path (for log messages)
	reauth   func() bool
	sleep    func(time.Duration)
	now      func() time.Time
	notify   func(title, msg string)
}

// runAttempts hammers choice #1 until it books or is taken, then falls through
// the remaining choices. A login page mid-run triggers reauth and retries the
// SAME choice (an auth blip must not burn a preference). An unrecognized page
// stops the loop: the POST may have gone through, and continuing risks
// duplicate bookings.
func runAttempts(prefs []seatOption, target, giveUp time.Time, retryEvery time.Duration, d attemptDeps) (attemptOutcome, int) {
	attempt, idx := 0, 0
	for idx < len(prefs) && d.now().Before(giveUp) {
		attempt++
		slot := prefs[idx]
		label := seatLabel(slot)

		page, finalURL, err := d.post(slot)
		if err != nil {
			logf("Attempt %d [%s]: request error: %v", attempt, label, err)
			d.sleep(retryEvery)
			continue
		}
		htmlFile := d.saveHTML(attempt, page)

		if isLoginPage(page, finalURL) {
			logf("Attempt %d [%s]: session expired - reauthenticating...", attempt, label)
			if !d.reauth() {
				d.sleep(retryEvery)
			}
			continue
		}

		switch classify(page) {
		case "success":
			logf("Attempt %d [%s]: SUCCESS. Response saved to %s", attempt, label, htmlFile)
			d.notify(fmt.Sprintf("BOOKED: %s %s", target.Format("Jan 2"), label),
				"SuperSaaS reservation submitted - check the response HTML / your email to confirm.")
			return outcomeBooked, attempt
		case "retry":
			logf("Attempt %d [%s]: not open yet (matched %q) - retrying in %s",
				attempt, label, retryRe.FindString(page), retryEvery)
			d.sleep(retryEvery)
		case "taken":
			logf("Attempt %d [%s]: slot taken - moving to next choice.", attempt, label)
			idx++
		case "validation":
			logf("Attempt %d [%s]: rejected (%q) - moving to next choice.", attempt, label, validationMessage(page))
			idx++
		case "unknown":
			logf("Attempt %d [%s]: UNKNOWN result - no known phrase. Saved to %s. VERIFY MANUALLY.", attempt, label, htmlFile)
			d.notify("SuperSaaS: needs verification",
				"Attempt returned an unrecognized page - possibly booked. Check the logs and your email NOW.")
			return outcomeNeedsVerify, attempt
		}
	}
	if idx >= len(prefs) {
		return outcomeAllTaken, attempt
	}
	return outcomeGaveUp, attempt
}

// realAttemptDeps wires runAttempts to the live client, filesystem, and toasts.
// dayURLByCar maps each car name to its day-view POST URL, so a preference list
// spanning multiple cars books each seat against the right schedule.
func realAttemptDeps(client *http.Client, dayURLByCar map[string]string, slotHours float64, prof profileInfo) attemptDeps {
	return attemptDeps{
		post: func(slot seatOption) (string, string, error) {
			body := buildBody(slot.Start, slotHours, slot.ResourceID, prof.FullName, prof.Phone, prof.Mobile)
			url := dayURLByCar[slot.Car]
			return fetch(client, http.MethodPost, url, body, url)
		},
		saveHTML: func(attempt int, page string) string {
			p := filepath.Join(logDir, fmt.Sprintf("attempt_%03d.html", attempt))
			_ = os.WriteFile(p, []byte(page), 0o644)
			return p
		},
		reauth: func() bool { return restoreSession(client) },
		sleep:  time.Sleep,
		now:    time.Now,
		notify: toast,
	}
}

func reportOutcome(out attemptOutcome, notify func(title, msg string)) {
	switch out {
	case outcomeAllTaken:
		logf("All preferred slots were taken before the booking went through.")
		notify("Cruiser: all choices gone", "Every preferred slot was taken. Check logs/signup-log.txt.")
	case outcomeGaveUp:
		logf("Gave up: reached cutoff time without a successful booking.")
		notify("Cruiser: FAILED", "No successful booking by cutoff. Check logs/signup-log.txt.")
	}
	// booked / needs-verify already logged and notified inside the loop
}

var errNoName = errors.New("no reservation name available - could not read your profile from the schedule and nothing is cached. Run once while logged in (-save-login set up)")
