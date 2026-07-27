# Cruiser — AB Driver's Ed slot grabber (Go)

Lists open AB Driver's Ed lesson slots and books one the moment the 28-day
booking window opens. One static binary per platform — no runtime to install.

## Build

```sh
go build -o cruiser.exe ./cmd/cruiser                                   # Windows
GOOS=darwin GOARCH=arm64 go build -o cruiser-mac-arm64 ./cmd/cruiser    # macOS Apple Silicon
GOOS=darwin GOARCH=amd64 go build -o cruiser-mac-intel ./cmd/cruiser    # macOS Intel
```

## First-time setup

```sh
cruiser -save-login    # stores your SuperSaaS login in the OS credential store
cruiser -test-login    # verifies it can log in
```

Secrets (login, session cookies, contact details) live only in the OS-native
credential store — Windows Credential Manager / macOS Keychain — never in the
binary, source, or plain files.

## Use

```sh
cruiser                                      # no args: banner + basic usage
cruiser -help                                # full usage, including every flag
cruiser -mine                                # your booked lessons + who's in the opposite seat
cruiser -week                                # a week of availability across all cars (read-only)
cruiser -week -date "Aug 23"                 # a specific week (7 days from the given start)
cruiser -date "Aug 24" -list                 # show open seats for one day (read-only)
cruiser -date "Aug 24" -choices 1,2 -dry-run # confirm plan + attack window
cruiser -date "Aug 24" -choices 1,2          # arm: waits for the 28-day window, then books
```

`-week` defaults to the week starting 28 days out from tomorrow (the booking
frontier) and uses **one fetch per car** — the day view returns weeks of
open-hours data in a single call. Booking data only loads ~30 days out, so days
past that show scheduled open hours (they fill in as each window nears).

Booking opens 28 days ahead at 6:00 am US Eastern; Cruiser starts hammering
at 5:55 am ET and falls through your choice list if seats get taken.

## Tests

```sh
go test ./...
```

## License

MIT — see [LICENSE](LICENSE).

## macOS notes

- First keychain access from a new binary pops an "allow" dialog — click
  **Always Allow** once so scheduled runs work unattended.
- Schedule with `launchd` (or just start it in a terminal the night before;
  the binary itself waits for the window).
