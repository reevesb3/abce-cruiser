package app

// Cruiser — AB Driver's Ed slot grabber (cross-platform: Windows + macOS).
// Lists open lesson slots and books one the moment the 28-day window opens.
//
//   cruiser -save-login                     # one-time: store login in the OS credential store
//   cruiser -test-login                     # verify the stored login works
//   cruiser -week                           # a week of availability across all cars
//   cruiser -date "Aug 24" -list            # list open seats (read-only)
//   cruiser -date "Aug 24" -choices 1,2 -dry-run
//   cruiser -date "Aug 24" -choices 1,2     # arm: waits for the 28-day window, then books
//
// main() only parses flags and dispatches; the flow steps live in flow.go and
// the schedule/auth logic in schedule.go / auth.go — all unit-tested.

import (
	"errors"
	"flag"
	"fmt"
	"net/http"
	"time"
	_ "time/tzdata" // embed IANA tz data — Windows has no system zoneinfo
)

// ----------------------------- CONFIG ---------------------------------------
// Not personal data — someone reusing this points these at their own schedule.
const (
	scheduleBase = "https://www.supersaas.com/schedule/ABDriverEd"            // + "/CarN"
	loginURL     = "https://www.supersaas.com/schedule/login/ABDriverEd/Car1" // login is account-wide; any car works
)

// Each car is its own SuperSaaS schedule; each 2-hour block has TWO capacity
// seats (a "drive first" and an "observe first, drive second"), booked by
// resource id. Listing considers every car; a booking targets one car's page.
var cars = []car{
	{"Car1", []role{{"84893", "Drive-Observation"}, {"84894", "Observation-Drive"}}},
	{"Car2", []role{{"84951", "Drive-Observation"}, {"84952", "Observation-Drive"}}},
	{"Car3", []role{{"84954", "Drive-Observation"}, {"84955", "Observation-Drive"}}},
}

// -----------------------------------------------------------------------------

type config struct {
	date          string
	slotHours     float64
	choices       string
	startEastern  string
	giveupEastern string
	retrySeconds  int
	listOnly      bool
	dryRun        bool
	week          bool
	mine          bool
	fromFile      string
	saveLogin     bool
	testLogin     bool
	help          bool
}

func (c config) retryEvery() time.Duration { return time.Duration(c.retrySeconds) * time.Second }

func parseFlags() config {
	var c config
	flag.StringVar(&c.date, "date", "", "target day, e.g. 'Aug 24' or '2026-08-24'")
	flag.Float64Var(&c.slotHours, "slot-hours", 2, "lesson length in hours")
	flag.StringVar(&c.choices, "choices", "", "preference order, e.g. '3,1,4,2' ('A' = all in listed order)")
	flag.StringVar(&c.startEastern, "start-eastern", "", "override attack-window start, 'YYYY-MM-DD HH:MM:SS' Eastern")
	flag.StringVar(&c.giveupEastern, "giveup-eastern", "", "override give-up time, 'YYYY-MM-DD HH:MM:SS' Eastern")
	flag.IntVar(&c.retrySeconds, "retry-seconds", 3, "delay between attempts")
	flag.BoolVar(&c.listOnly, "list", false, "list open seats and exit (no booking)")
	flag.BoolVar(&c.week, "week", false, "show a week of availability across all cars (default start: 28 days out from tomorrow; override with -date)")
	flag.BoolVar(&c.mine, "mine", false, "show your booked lessons and who's in the opposite seat of each")
	flag.BoolVar(&c.dryRun, "dry-run", false, "show the plan and attack window, but do not wait or book")
	flag.StringVar(&c.fromFile, "from-file", "", "read the day view from a local HTML file (implies -list)")
	flag.BoolVar(&c.saveLogin, "save-login", false, "store your SuperSaaS login in the OS credential store, then exit")
	flag.BoolVar(&c.testLogin, "test-login", false, "verify the stored login reauthenticates, then exit")
	flag.BoolVar(&c.help, "help", false, "show full usage help (all flags), then exit")
	// Show the full usage on -h or an unrecognized flag, too.
	flag.Usage = func() { printBanner(); printUsage(true) }
	flag.Parse()
	if c.fromFile != "" {
		c.listOnly = true
	}
	return c
}

func Main() {
	cfg := parseFlags()
	initLog()
	printBanner()

	// -help shows the full usage (including every flag); no arguments shows the
	// basic usage (common commands + first-time setup only).
	if cfg.help {
		printUsage(true)
		return
	}
	if flag.NFlag() == 0 && flag.NArg() == 0 {
		printUsage(false)
		return
	}

	if cfg.saveLogin {
		saveLoginPrompt()
		return
	}

	client := newClient()

	if cfg.testLogin {
		if restoreSession(client) {
			fmt.Println("TestLogin: SUCCESS - stored credentials logged in and returned a fresh session cookie.")
		} else {
			fmt.Println("TestLogin: FAILED - see the message above (no stored login, wrong password, or blocked).")
		}
		return
	}

	if err := run(cfg, client); err != nil {
		fatalf("%v", err)
	}
}

// run sequences the booking flow; each step is implemented (and tested) in
// flow.go / schedule.go.
func run(cfg config, client *http.Client) error {
	if cfg.week {
		return runWeek(cfg, client)
	}
	if cfg.mine {
		return runMine(cfg, client)
	}
	if cfg.date == "" {
		return errors.New(`the -date flag is required, e.g. -date "Aug 24"`)
	}
	target, err := resolveTargetDate(cfg.date)
	if err != nil {
		return err
	}
	logf("Target day: %s  (%g-hour slots, %d cars)", target.Format("Monday, January 2, 2006"), cfg.slotHours, len(cars))

	// 1. Load every car's day view (file or live, with retry/reauth/cookie caching).
	pages, err := loadCarPages(cfg, client, target)
	if err != nil {
		return err
	}

	// 2. Contact details: scraped from the logged-in page, cached as fallback.
	prof, scraped := resolveProfile(pages[0].page, loadProfile)
	if scraped && cfg.fromFile == "" {
		saveProfile(prof)
	}

	// 3. Open seats across all cars, from each schedule's authoritative hours.
	opts := collectOptions(pages, target, cfg.slotHours, prof.FullName)
	if len(opts) == 0 {
		logf("Nothing open on any car for that day.")
		return nil
	}
	fmt.Print(renderOptions(target, opts, cfg.slotHours))
	if cfg.listOnly {
		logf("List-only mode - not booking.")
		return nil
	}

	// 4. Preference order.
	input := cfg.choices
	if input == "" {
		input = promptChoices()
	}
	picks := parseChoices(input, len(opts))
	if len(picks) == 0 {
		logf("No valid slot choices entered - aborting.")
		return nil
	}
	prefs, desc := preferenceOrder(opts, picks)
	logf("Preference order: %s", desc)

	// 5. Attack window (Eastern — the schedule's timezone).
	start, giveUp, err := deriveWindow(target, cfg.startEastern, cfg.giveupEastern)
	if err != nil {
		return err
	}
	logf("Attack window (your local time): %s  ->  %s",
		start.Local().Format("1/2/2006 3:04 PM"), giveUp.Local().Format("1/2/2006 3:04 PM"))
	if cfg.dryRun {
		logf("DryRun - not waiting or booking. Re-run without -dry-run to arm Cruiser.")
		return nil
	}
	if prof.FullName == "" {
		return errNoName
	}

	// 6. Wait for the window, then book (each choice targets its own car's page).
	waitForWindow(start, time.Now, time.Sleep, func(s string) { fmt.Print(s) })
	logf("Window open - starting booking attempts.")
	dayURLByCar := make(map[string]string, len(cars))
	for _, c := range cars {
		dayURLByCar[c.Name] = carDayURL(c, target)
	}
	outcome, _ := runAttempts(prefs, target, giveUp, cfg.retryEvery(),
		realAttemptDeps(client, dayURLByCar, cfg.slotHours, prof))
	reportOutcome(outcome, toast)
	logf("Done.")
	return nil
}

const weekDays = 7

// defaultWeekStart is 28 days out from tomorrow — the booking frontier that
// opens tomorrow morning (a slot becomes bookable 28 days ahead).
func defaultWeekStart(now time.Time) time.Time {
	d := time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, time.UTC)
	return d.AddDate(0, 0, 1+28)
}

// runMine shows the user's own booked lessons and who holds the opposite seat.
// One fetch per car — the day view carries every booking in the loaded window.
func runMine(cfg config, client *http.Client) error {
	n := time.Now()
	today := time.Date(n.Year(), n.Month(), n.Day(), 0, 0, 0, 0, time.UTC)
	// The day view returns bookings for roughly [reqDay-28, reqDay+1], so we
	// point the fetch ~28 days out — its window then spans the whole bookable
	// range [today, today+28] while we still display only from today onward.
	fetchDay := today.AddDate(0, 0, 28)
	pages, err := loadCarPages(cfg, client, fetchDay)
	if err != nil {
		return err
	}
	prof, _ := resolveProfile(pages[0].page, loadProfile)
	fmt.Print(renderMySlots(collectMySlots(pages, prof.FullName, today)))
	return nil
}

// runWeek shows a week of availability across all cars — read-only, one fetch
// per car (the day view returns weeks of open-hours data in a single call).
func runWeek(cfg config, client *http.Client) error {
	weekStart := defaultWeekStart(time.Now())
	if cfg.date != "" {
		d, err := resolveTargetDate(cfg.date)
		if err != nil {
			return err
		}
		weekStart = d
	}
	logf("Week of %s -> %s across %d cars", weekStart.Format("Mon Jan 2"),
		weekStart.AddDate(0, 0, weekDays-1).Format("Mon Jan 2"), len(cars))

	// One fetch per car, pointed at the week start so the returned open-hours
	// range covers the whole week.
	pages, err := loadCarPages(cfg, client, weekStart)
	if err != nil {
		return err
	}
	fmt.Print(renderWeek(pages, weekStart, weekDays, cfg.slotHours))
	return nil
}
