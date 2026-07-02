#!/usr/bin/env python3
"""
eqdps - a cross-platform EverQuest (Legends) combat-log DPS meter.

Pure standard library: runs identically on Linux, macOS, and Windows with any
Python 3.8+. Tails the live eqlog_*.txt, segments combat into encounters, and
shows a live DPS table. Also has a --replay mode to crunch an existing log.

Usage:
    python3 eqdps.py                 # auto-find newest eqlog, live meter
    python3 eqdps.py --list          # show detected log files and exit
    python3 eqdps.py --log PATH      # use a specific log file
    python3 eqdps.py --replay PATH   # parse a whole file, print encounter summaries
    python3 eqdps.py --player NAME   # override player name (else from filename)
    python3 eqdps.py --gap 12        # seconds of no combat that ends an encounter
    python3 eqdps.py --mine          # show only your own damage
"""
from __future__ import annotations

import argparse
import os
import re
import sys
import time
from datetime import datetime
from glob import glob
from pathlib import Path

# ---------------------------------------------------------------------------
# Line parsing
# ---------------------------------------------------------------------------

LINE_RE = re.compile(r"^\[(?P<ts>[^\]]+)\] (?P<body>.*)$")
TS_FMT = "%a %b %d %H:%M:%S %Y"

# Melee + non-melee "for N points of damage" lines. The verb regex matches both
# 2nd person ("You slash") and 3rd person ("An orc slashes YOU").
_VERBS = (
    r"hits?|slashe?s?|crushe?s?|pierces?|bashe?s?|kicks?|bites?|mauls?|"
    r"punche?s?|backstabs?|strikes?|gores?|claws?|slams?|rends?|smashe?s?|"
    r"stings?|slices?"
)
DAMAGE_RE = re.compile(
    r"^(?P<attacker>.+?) (?P<verb>" + _VERBS + r") (?P<target>.+?) "
    r"for (?P<amount>\d+) points? of (?P<nm>non-melee )?damage\."
    r"(?: \((?P<flags>[^)]*)\))?\s*$"
)
# Damage-over-time / spell tick: "An orc has taken 12 damage from your Foo."
DOT_RE = re.compile(
    r"^(?P<target>.+?) has taken (?P<amount>\d+) damage from (?P<source>.+?)\.\s*$"
)
# Deaths (used to mark enemies and bound encounters)
SLAIN_BY_RE = re.compile(r"^(?P<target>.+?) has been slain by (?P<killer>.+?)!\s*$")
YOU_SLAIN_RE = re.compile(r"^You have slain (?P<target>.+?)!\s*$")
DIES_RE = re.compile(r"^(?P<target>.+?) dies\.\s*$")


def norm(name: str, player: str) -> str:
    """Normalise 'You'/'Your'/'YOU'/'yourself' to the player's real name."""
    low = name.strip().lower()
    if low in ("you", "your", "yourself", "you`s", "your's"):
        return player
    # strip a possessive that some lines use ("Your pet" stays, but bare poss.)
    return name.strip()


class Event:
    __slots__ = ("ts", "attacker", "target", "amount", "ability", "crit")

    def __init__(self, ts, attacker, target, amount, ability, crit):
        self.ts = ts
        self.attacker = attacker
        self.target = target
        self.amount = amount
        self.ability = ability
        self.crit = crit


def parse_line(line: str, player: str):
    """Return an Event, the literal string 'death', or None."""
    m = LINE_RE.match(line)
    if not m:
        return None
    try:
        ts = datetime.strptime(m.group("ts"), TS_FMT).timestamp()
    except ValueError:
        return None
    body = m.group("body")

    dm = DAMAGE_RE.match(body)
    if dm:
        flags = (dm.group("flags") or "")
        crit = "critical" in flags.lower() or "crippling" in flags.lower()
        verb = dm.group("verb")
        ability = "spell" if dm.group("nm") else verb
        return Event(
            ts,
            norm(dm.group("attacker"), player),
            norm(dm.group("target"), player),
            int(dm.group("amount")),
            ability,
            crit,
        )

    dt = DOT_RE.match(body)
    if dt:
        src = dt.group("source")
        if src.lower().startswith("your "):
            attacker, ability = player, src[5:]
        elif "'s " in src:
            owner, _, spell = src.partition("'s ")
            attacker, ability = owner, spell
        else:
            attacker, ability = "Unknown", src  # environmental / unattributed
        return Event(
            ts, attacker, norm(dt.group("target"), player),
            int(dt.group("amount")), ability + " (dot)", False,
        )

    if YOU_SLAIN_RE.match(body) or SLAIN_BY_RE.match(body) or DIES_RE.match(body):
        return "death"
    return None


# ---------------------------------------------------------------------------
# Encounter accumulation
# ---------------------------------------------------------------------------

class Combatant:
    def __init__(self, name):
        self.name = name
        self.total = 0
        self.hits = 0
        self.crits = 0
        self.best = 0
        self.targets = set()
        self.abilities = {}  # ability -> total

    def add(self, ev: Event):
        self.total += ev.amount
        self.hits += 1
        if ev.crit:
            self.crits += 1
        self.best = max(self.best, ev.amount)
        self.targets.add(ev.target)
        self.abilities[ev.ability] = self.abilities.get(ev.ability, 0) + ev.amount


class Encounter:
    def __init__(self, player):
        self.player = player
        self.start = None
        self.last = None
        self.by_attacker = {}  # name -> Combatant
        self.enemies = set()   # things the player (or pets) damaged

    def empty(self):
        return self.start is None

    def add(self, ev: Event):
        if self.start is None:
            self.start = ev.ts
        self.last = ev.ts
        c = self.by_attacker.get(ev.attacker)
        if c is None:
            c = self.by_attacker[ev.attacker] = Combatant(ev.attacker)
        c.add(ev)
        if ev.attacker == self.player:
            self.enemies.add(ev.target)

    def duration(self):
        if self.start is None:
            return 0.0
        return max(0.0, (self.last or self.start) - self.start)

    def friendlies(self):
        """Attackers that hit an enemy (you + pets + group); excludes the mobs."""
        out = []
        for name, c in self.by_attacker.items():
            if name == self.player or (c.targets & self.enemies and name not in self.enemies):
                out.append(c)
        return out


# ---------------------------------------------------------------------------
# Display
# ---------------------------------------------------------------------------

CLEAR = "\033[H\033[J"


def enable_ansi():
    if os.name == "nt":
        try:
            import ctypes
            k = ctypes.windll.kernel32
            k.SetConsoleMode(k.GetStdHandle(-11), 7)
        except Exception:
            pass


def fmt(n):
    return f"{n:,}"


def render(enc: Encounter, mine_only: bool, status: str):
    dur = enc.duration() or 1.0
    crew = enc.friendlies()
    if mine_only:
        crew = [c for c in crew if c.name == enc.player]
    crew.sort(key=lambda c: c.total, reverse=True)
    raid = sum(c.total for c in crew) or 1

    lines = [CLEAR, "  eqdps — \033[1m{}\033[0m   {}".format(enc.player, status), ""]
    if enc.enemies:
        tgt = ", ".join(sorted(enc.enemies)[:3])
        if len(enc.enemies) > 3:
            tgt += f" +{len(enc.enemies) - 3} more"
        lines.append(f"  vs {tgt}")
    lines.append("  fight {:>5.1f}s   raid DPS {:>8.1f}   total {}".format(
        enc.duration(), raid / dur, fmt(raid)))
    lines.append("")
    lines.append("  {:<20} {:>9} {:>11} {:>6} {:>6} {:>5}".format(
        "name", "dps", "total", "%", "crit%", "best"))
    lines.append("  " + "-" * 62)
    for c in crew:
        you = "\033[1;36m" if c.name == enc.player else ""
        end = "\033[0m" if you else ""
        critpct = (100 * c.crits / c.hits) if c.hits else 0
        lines.append("  {}{:<20}{} {:>9.1f} {:>11} {:>5.0f}% {:>5.0f}% {:>5}".format(
            you, c.name[:20], end,
            c.total / dur, fmt(c.total), 100 * c.total / raid, critpct, c.best))
    if not crew:
        lines.append("  (waiting for combat...)")
    lines.append("")
    lines.append("  Ctrl-C to quit")
    sys.stdout.write("\n".join(lines) + "\n")
    sys.stdout.flush()


def print_summary(enc: Encounter, idx: int):
    if enc.empty():
        return
    dur = enc.duration() or 1.0
    crew = sorted(enc.friendlies(), key=lambda c: c.total, reverse=True)
    if not crew:
        return
    raid = sum(c.total for c in crew) or 1
    tgt = ", ".join(sorted(enc.enemies)[:4]) or "?"
    t0 = datetime.fromtimestamp(enc.start).strftime("%H:%M:%S")
    print(f"\n=== Fight #{idx} @ {t0}  ({enc.duration():.0f}s)  vs {tgt} ===")
    print(f"    raid: {fmt(raid)} dmg  |  {raid/dur:.0f} dps")
    for c in crew:
        critpct = (100 * c.crits / c.hits) if c.hits else 0
        top = max(c.abilities.items(), key=lambda kv: kv[1], default=("", 0))[0]
        print(f"    {c.name:<22} {c.total/dur:>8.0f} dps  {fmt(c.total):>10}  "
              f"({100*c.total/raid:>3.0f}%)  crit {critpct:>3.0f}%  top:{top}")


# ---------------------------------------------------------------------------
# Log discovery + tailing
# ---------------------------------------------------------------------------

def candidate_logs():
    home = Path.home()
    pats = [
        str(home / ".local/share/wineprefixes/*/drive_c/**/Logs/eqlog_*.txt"),
        str(home / ".wine/drive_c/**/Logs/eqlog_*.txt"),
        str(home / "Library/Application Support/CrossOver/Bottles/*/drive_c/**/Logs/eqlog_*.txt"),
        "eqlog_*.txt",
    ]
    if os.name == "nt":
        pats += [
            r"C:\Users\Public\Daybreak Game Company\Installed Games\**\Logs\eqlog_*.txt",
            r"C:\Program Files (x86)\**\Logs\eqlog_*.txt",
            r"C:\Program Files\**\Logs\eqlog_*.txt",
        ]
    found = set()
    for p in pats:
        found.update(glob(p, recursive=True))
    return sorted(found, key=lambda f: os.path.getmtime(f), reverse=True)


def player_from_path(path):
    name = os.path.basename(path)
    m = re.match(r"eqlog_([^_]+)_", name)
    return m.group(1) if m else "You"


def follow(path):
    """Yield new lines as they're appended; handle truncation/rotation."""
    f = open(path, "r", encoding="utf-8", errors="replace")
    f.seek(0, os.SEEK_END)
    while True:
        line = f.readline()
        if line:
            if line.endswith("\n"):
                yield line.rstrip("\n")
            else:  # partial line; rewind and wait for the rest
                f.seek(f.tell() - len(line))
                yield None
        else:
            yield None
            if os.path.getsize(path) < f.tell():  # truncated/rotated
                f.seek(0)


# ---------------------------------------------------------------------------
# Modes
# ---------------------------------------------------------------------------

def run_replay(path, player, gap):
    enc = Encounter(player)
    idx = 0
    with open(path, "r", encoding="utf-8", errors="replace") as f:
        for line in f:
            ev = parse_line(line.rstrip("\n"), player)
            if ev is None or ev == "death":
                continue
            if not enc.empty() and ev.ts - enc.last > gap:
                idx += 1
                print_summary(enc, idx)
                enc = Encounter(player)
            enc.add(ev)
    if not enc.empty():
        idx += 1
        print_summary(enc, idx)
    print(f"\n{idx} encounter(s) parsed from {os.path.basename(path)}")


def run_live(path, player, gap, mine_only):
    enable_ansi()
    enc = Encounter(player)
    last_render = 0.0
    src = follow(path)
    status = f"live: {os.path.basename(path)}"
    while True:
        line = next(src)
        now = time.time()
        if line is not None:
            ev = parse_line(line, player)
            if ev is not None and ev != "death":
                if not enc.empty() and ev.ts - enc.last > gap:
                    enc = Encounter(player)  # new fight
                enc.add(ev)
        else:
            time.sleep(0.2)
        # close a stale fight using wall-clock idle, and refresh ~3x/sec
        if now - last_render >= 0.33:
            render(enc, mine_only, status)
            last_render = now


def main():
    ap = argparse.ArgumentParser(description="Cross-platform EQ combat-log DPS meter")
    ap.add_argument("--log", help="path to eqlog_*.txt (default: auto-detect newest)")
    ap.add_argument("--replay", help="parse an existing log and print summaries")
    ap.add_argument("--player", help="your character name (default: from filename)")
    ap.add_argument("--gap", type=float, default=10.0,
                    help="seconds of inactivity that ends an encounter (default 10)")
    ap.add_argument("--mine", action="store_true", help="show only your own damage")
    ap.add_argument("--list", action="store_true", help="list detected logs and exit")
    args = ap.parse_args()

    if args.list:
        logs = candidate_logs()
        if not logs:
            print("No eqlog_*.txt files found.")
            return
        for p in logs:
            mt = datetime.fromtimestamp(os.path.getmtime(p)).strftime("%Y-%m-%d %H:%M")
            sz = os.path.getsize(p) / 1e6
            print(f"{mt}  {sz:8.1f} MB  {p}")
        return

    path = args.replay or args.log
    if not path:
        logs = candidate_logs()
        if not logs:
            sys.exit("No eqlog_*.txt found. Pass --log PATH. (Is /log on in game?)")
        path = logs[0]
    if not os.path.exists(path):
        sys.exit(f"Log not found: {path}")

    player = args.player or player_from_path(path)

    if args.replay:
        run_replay(path, player, args.gap)
    else:
        try:
            run_live(path, player, args.gap, args.mine)
        except KeyboardInterrupt:
            sys.stdout.write("\n")


if __name__ == "__main__":
    main()
