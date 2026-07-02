# eqdps

A cross-platform EverQuest (Legends) combat-log **DPS meter** — native desktop
window with live, colored damage bars. Single Go binary, no runtime to install.

## Run

```sh
eqdps                 # auto-detect newest eqlog_*.txt, open the live window
eqdps -log PATH       # use a specific log file
eqdps -player NAME    # override character name (default: from filename)
eqdps -gap 12         # seconds of inactivity that ends an encounter (default 10)
eqdps -list           # list detected logs and exit
eqdps -replay PATH    # crunch a whole log into per-fight summaries (terminal, no GUI)
```

In game, make sure logging is on: `/log on` (eqclient.ini `Log=1`).

## Window

- Headline: `Cedega — 612 dps`, with target · fight timer · total · fight count.
- One colored horizontal bar per combatant (you in teal), sorted by damage,
  showing `dps · total · %`. Friendlies only (you + pets + group); the mobs are
  filtered out via a shared-target heuristic.
- **on top** checkbox (Linux/X11, via `wmctrl`) and a **reset** button.

## Build

Requires Go 1.21+ and, on Linux, Fyne's GUI headers:

```sh
sudo apt-get install -y libgl1-mesa-dev xorg-dev   # Debian/Ubuntu
go build -o eqdps .
```

### Other OSes (the "cross-OS" part)

The code is OS-agnostic, but Fyne uses cgo, so you build **on** each target (or
use [`fyne-cross`](https://github.com/fyne-io/fyne-cross), which runs the target
toolchains in Docker):

```sh
go install github.com/fyne-io/fyne-cross@latest
fyne-cross windows -arch amd64      # -> fyne-cross/bin/windows-amd64/eqdps.exe
fyne-cross darwin  -arch amd64,arm64
```

Log auto-detection already covers Linux (Wine prefixes), macOS (CrossOver
bottles), and Windows install dirs; otherwise pass `-log`.

## Files

- `model.go` — log line parsing + encounter/DPS accumulation (thread-safe)
- `tail.go`  — log discovery + live file tailing (handles rotation/truncation)
- `ui.go`    — Fyne dark theme + the custom bar-list widget
- `main.go`  — flags, wiring, live window, replay mode
- `eqdps.py` — original Python prototype (kept for reference; validated the formats)
