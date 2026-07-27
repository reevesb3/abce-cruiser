package app

import (
	"flag"
	"fmt"
	"os"
)

// activeBanner selects the startup banner. Swap to bannerScope or bannerRetro
// to change the look.
var activeBanner = bannerCruiser

const bannerCruiser = `
  _____________________  ________________________________
  __  ____/__  __ \_  / / /___  _/_  ___/__  ____/__  __ \
  _  /    __  /_/ /  / / / __  / _____ \__  __/  __  /_/ /
  / /___  _  _, _// /_/ / __/ /  ____/ /_  /___  _  _, _/
  \____/  /_/ |_| \____/  /___/  /____/ /_____/  /_/ |_|
                 AB Driver's Ed slot grabber
  --------------------------------------------------------`

const bannerScope = `
  >>------------------  C R U I S E R  ------------------<<
                             _______
            ___            /  ___  \            ___
        (x)=====\_________/__/   \__\_________/=====(x)
                    (O)                   (O)
      AB Driver's Ed slot grabber  .  locked and loaded`

const bannerRetro = `
 +----------------------------------------------------------+
    CRUISER  --  AB Driver's Ed
                                             _______
    grabs the lesson slot the instant     _/  o  o  \_
    the 28-day booking window opens      |___________(O)
                                         |__(O)_________|
 +----------------------------------------------------------+`

func printBanner() { fmt.Println(activeBanner) }

// printUsage shows how to use Cruiser. With full=false (no arguments) it prints
// just the common commands and first-time setup; with full=true (-help) it also
// lists every flag.
func printUsage(full bool) {
	fmt.Println()
	fmt.Println("Cruiser — books AB Driver's Ed driving-lesson slots the moment the 28-day window opens.")
	fmt.Println()
	fmt.Println("Common usage:")
	fmt.Println(`  cruiser -mine                         Show your booked lessons + who's in the opposite seat`)
	fmt.Println(`  cruiser -week                         Show a week of availability across all cars`)
	fmt.Println(`  cruiser -date "Aug 24" -list          List open seats for one day`)
	fmt.Println(`  cruiser -date "Aug 24" -choices 1,2   Arm Cruiser for that day (books at 6am ET)`)
	fmt.Println(`  cruiser -date "Aug 24" -choices 1,2 -dry-run   Preview the plan without booking`)
	fmt.Println()
	fmt.Println("First-time setup:")
	fmt.Println(`  cruiser -save-login                   Store your SuperSaaS login (OS credential store)`)
	fmt.Println(`  cruiser -test-login                   Verify the stored login works`)
	fmt.Println()
	if full {
		fmt.Println("All flags:")
		flag.CommandLine.SetOutput(os.Stdout)
		flag.PrintDefaults()
	} else {
		fmt.Println("Run 'cruiser -help' for all flags and options.")
	}
}
