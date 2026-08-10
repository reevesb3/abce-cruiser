# Cruiser — AB Driver's Ed slot grabber (Go)

Lists open AB Driver's Ed lesson slots and books one the moment the 28-day
booking window opens. One static binary per platform — no runtime to install.

## Build

```sh
go build -o cruiser.exe ./cmd/cruiser                                   # Windows
GOOS=darwin GOARCH=arm64 go build -o cruiser-darwin-arm64 ./cmd/cruiser  # macOS Apple Silicon
GOOS=darwin GOARCH=amd64 go build -o cruiser-darwin-amd64 ./cmd/cruiser  # macOS Intel
```

Before your first run, see [Windows notes](#windows-notes) or
[macOS notes](#macos-notes) — both systems block unsigned downloads until you
clear them, which takes one or two one-time commands.

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

## Windows notes

The Windows builds are unsigned command-line programs, so Windows warns about
them twice — once when you download, once when you run. Here is the whole
thing start to finish, assuming you have never used PowerShell.

### 1. Download the right file

**Start → Settings → System → About**, look at "System type":

- "x64-based processor" → `cruiser-windows-amd64.exe`
- "ARM-based processor" → `cruiser-windows-arm64.exe`

Your browser will probably object that the file "isn't commonly downloaded"
and offer to discard it. Choose **Keep** (in Edge: click the "…" next to the
warning → **Keep** → **Show more** → **Keep anyway**). Leave it in
**Downloads**.

### 2. Open PowerShell

Press the **Windows key**, type `powershell`, press **Enter**. A window with a
text prompt appears. You type a line, press Enter, it runs.

Use PowerShell or Command Prompt — **not Git Bash**. Cruiser hides your
password as you type it, which needs a real Windows console; in Git Bash the
`-save-login` prompt fails with "The handle is invalid."

### 3. Go to the Downloads folder

```powershell
cd ~\Downloads
```

### 4. Rename it and remove the download block

```powershell
Rename-Item cruiser-windows-amd64.exe cruiser.exe   # shorter name
Unblock-File .\cruiser.exe                          # clear the "from the internet" mark
```

Notes:

- `Unblock-File` strips the Mark of the Web. The equivalent in the mouse-only
  path: right-click the file → **Properties** → tick **Unblock** at the
  bottom → **OK**.
- Windows hides file extensions by default, so the file may show as just
  `cruiser-windows-amd64` in Explorer while its real name ends in `.exe`. Type
  the full name including `.exe` in PowerShell. (To see extensions: Explorer →
  **View → Show → File name extensions**.)
- There is no "make it executable" step on Windows — `.exe` is enough.

Redo `Unblock-File` any time you download a newer build.

### 5. Run it

PowerShell won't run a program from the current folder unless you prefix it
with `.\` — that means "the cruiser.exe in this folder":

```powershell
.\cruiser.exe -save-login
.\cruiser.exe -test-login
```

The first run pops **"Windows protected your PC"** from SmartScreen. Click
**More info**, then **Run anyway**. This appears once per build.

Every `cruiser …` example elsewhere in this README is `.\cruiser.exe …` when
you run it this way.

Cruiser writes a `logs\signup-log.txt` next to wherever you launch it from, so
keep running it from the same folder if you want one continuous log.

If Microsoft Defender quarantines the file outright (Go binaries occasionally
trip heuristics), it will vanish from Downloads — restore it from **Windows
Security → Virus & threat protection → Protection history**, or add an
exclusion for the folder you keep it in.

### 6. Credential storage — no prompt

Your login goes into **Windows Credential Manager** under `SuperSaaSSniper`.
Unlike macOS, Windows does not pop an approval dialog for this, so there's
nothing to click and nothing that can stall an unattended run. To inspect or
delete it later: **Control Panel → Credential Manager → Windows Credentials**.

### 7. Keep the PC awake for the booking window

Booking opens at 6:00 am ET and cruiser starts hammering at 5:55 am. A
sleeping PC books nothing. Plug in the laptop and disable sleep on AC power:

```powershell
powercfg /change standby-timeout-ac 0
powercfg /change hibernate-timeout-ac 0
```

(Or **Settings → System → Power & battery → Screen, sleep & hibernate
timeouts** → set the plugged-in sleep option to **Never**.) Leave the
PowerShell window open overnight — closing it kills the run. On a laptop, also
check that closing the lid doesn't sleep it, or just leave the lid open.

Task Scheduler works if you want a proper scheduled job, but it isn't
necessary: the binary waits for the window on its own.

### 8. Notifications

Cruiser raises a Windows toast plus a series of beeps when it books. If toasts
are suppressed, check **Settings → System → Notifications** (and that Focus
Assist / Do Not Disturb is off) — or just watch the PowerShell window, which
logs the same information.

## macOS notes

The Mac builds are unsigned command-line programs, so macOS blocks them until
you explicitly clear them. Here is the whole thing start to finish, assuming
you have never used Terminal.

### 1. Download the right file

Apple menu  → **About This Mac**:

- "Chip: Apple M1/M2/M3/M4…" → `cruiser-darwin-arm64`
- "Processor: Intel…" → `cruiser-darwin-amd64`

Leave it in your **Downloads** folder.

### 2. Open Terminal

Press **⌘ Space**, type `terminal`, press **Return**. A window with a text
prompt appears. You type a line, press **Return**, it runs. That's it.

### 3. Go to the Downloads folder

Type this and press Return:

```sh
cd ~/Downloads
```

### 4. Rename, unlock, and un-quarantine it

Three one-time commands. Replace `cruiser-darwin-arm64` with whichever file
you actually downloaded.

```sh
mv cruiser-darwin-arm64 cruiser     # shorter name, so later commands are shorter
chmod +x cruiser                    # mark it runnable — downloads arrive without this
xattr -d com.apple.quarantine cruiser   # remove the "downloaded from the internet" tag
```

Notes on each:

- `chmod +x` — without it you get `zsh: permission denied: ./cruiser`.
- `xattr -d com.apple.quarantine` — without it Gatekeeper refuses with
  *"cannot be opened because Apple cannot check it for malicious software."*
  If the command answers `No such xattr`, the tag was never applied — that's
  fine, carry on.
- If you'd rather not run `xattr`: run cruiser once, let macOS block it, then
  open **System Settings → Privacy & Security**, scroll to the Security
  section, and click **Open Anyway** next to the cruiser message.

Redo `chmod +x` and `xattr -d` any time you download a newer build.

### 5. Run it

Because cruiser lives in a folder that isn't on your `PATH`, you must prefix
it with `./` — that means "the cruiser in this folder":

```sh
./cruiser -save-login
./cruiser -test-login
```

Every `cruiser …` example elsewhere in this README is `./cruiser …` when you
run it this way, from `~/Downloads`. Cruiser writes a `logs/signup-log.txt`
next to wherever you launch it from, so keep running it from the same folder
if you want one continuous log.

To drop the `./`, move the binary onto your `PATH` once:

```sh
sudo mv cruiser /usr/local/bin/      # asks for your Mac password
```

### 6. The keychain dialog

The first time cruiser reads or writes a saved login, macOS pops:

> **security** wants to use your confidential information stored in
> "SuperSaaSSniper" in your keychain.

`security` is Apple's own built-in keychain tool, which cruiser calls — the
dialog naming it instead of cruiser is expected. Type your Mac login password
and click **Always Allow**, not Allow, so an unattended 5:55 am run isn't
sitting on a dialog waiting for you.

### 7. Keep the Mac awake for the booking window

Booking opens at 6:00 am ET and cruiser starts hammering at 5:55 am. A
sleeping Mac books nothing. Start it the night before under `caffeinate`,
which holds off sleep until cruiser exits:

```sh
caffeinate -is ./cruiser -date "Aug 24" -choices 1,2
```

Leave the Terminal window open and the laptop plugged in — closing the window
or the lid kills the run. (`launchd` also works if you want a proper
scheduled job, but it isn't necessary: the binary waits for the window on its
own.)

### 8. Notifications

The first alert may ask permission to show notifications from Terminal.
Allow it, or just watch the Terminal window — cruiser logs there either way.

## License

MIT — see [LICENSE](LICENSE).
