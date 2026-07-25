# EQLC — EverQuest Legends Companion

A native desktop companion app for EverQuest Legends that reads the game's
`/log` file live. Single Go binary (Fyne GUI), no runtime to install.

Grew out of `eqdps` (a DPS meter); the meter is now one module of several.

## Modules

- **Home** — glanceable dashboard cards (combat, kills/deaths, loot & money,
  quest items, XP, session); tap a card to jump to its full view
- **Live / History** — the combat meter: damage done / enemy damage / damage
  taken / healing (HPS) / CC & stuns / deaths, per-fight history with search,
  per-ability breakdowns on click
- **Loot** — every looted item with its auto-loot disposition (kept / sold /
  stored / merged), quest items starred via a built-in quest database, and a
  red alert when a quest item gets auto-sold or auto-merged
- **Quests** — quest items grouped by quest, with turn-in notes, links to the
  eqlwiki article, and the walkthrough text fetched right into the app
- **Inventory** — imports `/outputfile inventory` dumps; carried quest items
  feed the Quests view
- **XP** — per-day and per-session experience (real percentages when the
  client's percent display is on), kills, active hours, %/hr rates, level dings
- **Session** — money (vendor vs corpse coin), skill-ups, zones visited,
  recent faction hits
- **Mini mode** — collapse to a one-line always-updating pill while playing

## Quest database

`questdb.go` holds curated entries (turn-in NPC, location, gotchas — verified
in game). `questdb_wiki.go` is generated from eqlwiki.com quest pages by
`harvest_quest_items.py` (~1,400 turn-in components, out-of-era content
excluded). Regenerate after game patches:

```sh
python3 harvest_quest_items.py && go build -o eqlc .
```

## Run

```sh
eqlc                 # auto-detect newest eqlog_*.txt, open the window
eqlc -log PATH       # use a specific log file
eqlc -player NAME    # override character name (default: from filename)
eqlc -gap 12         # seconds of inactivity that ends an encounter (default 10)
eqlc -list           # list detected logs and exit
eqlc -replay PATH    # crunch a whole log into per-fight summaries (no GUI)
```

In game, make sure logging is on: `/log on` (eqclient.ini `Log=1`), and turn
on the experience-percent display for real XP analytics.

Log auto-detection covers Windows install dirs, macOS (CrossOver bottles and
`~/Documents/eqlogs` for VM setups), and Linux (Wine prefixes); otherwise
pass `-log`.

Settings persist to the OS config dir (`%AppData%\eqlc\config.json` on
Windows, `~/Library/Application Support/eqlc/config.json` on macOS,
`~/.config/eqlc/config.json` on Linux). Optional: set `"sync_cmd"` there to
any shell command and the Inventory tab gains a **Sync** button that runs it —
handy for VM setups where a script copies game files somewhere readable.

## Build

EQLC is a single Go program using [Fyne](https://fyne.io) for the GUI. Fyne
uses cgo, so each OS needs a C compiler alongside Go 1.21+.

### Windows (native)

1. Install Go: <https://go.dev/dl/> (the .msi installer is fine)
2. Install a C compiler — simplest is MSYS2 (<https://www.msys2.org>), then in
   the MSYS2 UCRT64 shell: `pacman -S mingw-w64-ucrt-x86_64-gcc`
   and add `C:\msys64\ucrt64\bin` to your PATH
3. In a normal terminal, from the repo folder:

```bat
go build -ldflags -H=windowsgui -o eqlc.exe .
```

(`-H=windowsgui` stops a console window opening behind the app; omit it if
you want console output.) Run `eqlc.exe` — it will find your EQL logs under
`C:\Users\Public\Daybreak Game Company\Installed Games\` automatically.

### macOS

Xcode command-line tools provide the compiler (`xcode-select --install`), then:

```sh
go build -o eqlc .
```

### Linux

```sh
sudo apt-get install -y gcc libgl1-mesa-dev xorg-dev   # Debian/Ubuntu
go build -o eqlc .
```

### Cross-compiling

[`fyne-cross`](https://github.com/fyne-io/fyne-cross) runs each target's
toolchain in Docker, so you can build Windows binaries from macOS/Linux:

```sh
go install github.com/fyne-io/fyne-cross@latest
fyne-cross windows -arch amd64      # -> fyne-cross/bin/windows-amd64/
fyne-cross darwin  -arch amd64,arm64
```

### Tests

```sh
go test ./...        # parser regression suite (real EQL log-line formats)
```

## Files

- `model.go`    — log parsing (damage/heals/CC/deaths/loot/XP/session) + accumulation
- `model_test.go` — parser regression tests against real EQL log-line formats
- `questdb.go`  — curated quest-item database
- `questdb_wiki.go` — generated wiki quest-item database (do not hand-edit)
- `inventory.go` — /outputfile inventory import + quest progress grouping
- `wiki.go`     — eqlwiki fetch + wikitext-to-plain-text for in-app walkthroughs
- `tail.go`     — log discovery + live tailing (handles rotation/truncation)
- `ui.go`       — Fyne dark theme, bar-list widget, dashboard cards
- `main.go`     — flags, wiring, tabs, mini mode, replay
- `harvest_quest_items.py` — questdb_wiki.go generator (eqlwiki API)
- `eqdps.py`    — original Python prototype (kept for reference)
