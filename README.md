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

## Build

Requires Go 1.21+ (and on Linux, Fyne's GUI headers):

```sh
go build -o eqlc .
go test ./...        # parser regression suite (real EQL log lines)
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
