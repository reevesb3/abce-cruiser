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
	"flag"
	"fmt"
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
