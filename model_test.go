package main

import (
	"testing"
	"time"
)

// Every format below is a real line observed in EQL beta logs (July 2026).
// If a parser change breaks one of these, the meter silently loses data —
// exactly how the classic "experience!!" double-bang bug slipped through.

func TestParseDamageLines(t *testing.T) {
	cases := []struct {
		line     string
		attacker string
		target   string
		amount   int
		ability  string
	}{
		{"[Wed Jul 15 20:15:35 2026] You slash a fire beetle for 14 points of damage.",
			"Soandso", "a fire beetle", 14, "slash"},
		{"[Wed Jul 15 20:15:33 2026] A fire beetle bites YOU for 2 points of damage.",
			"a fire beetle", "Soandso", 2, "bites"},
		{"[Wed Jul 15 20:15:33 2026] You hit a fire beetle for 5 points of magic damage by Lifetap.",
			"Soandso", "a fire beetle", 5, "Lifetap"},
		{"[Wed Jul 15 20:15:30 2026] a fire beetle hit you for 4 points of fire damage by Burst of Flame.",
			"a fire beetle", "Soandso", 4, "Burst of Flame"},
		{"[Wed Jul 15 20:15:34 2026] A fire beetle has taken 2 damage from your Flame Lick.",
			"Soandso", "a fire beetle", 2, "Flame Lick (dot)"},
	}
	for _, c := range cases {
		ev, death := ParseLine(c.line, "Soandso")
		if death != nil || ev == nil {
			t.Fatalf("no event for %q", c.line)
		}
		if ev.Kind != KDamage {
			t.Errorf("%q: kind = %v, want damage", c.line, ev.Kind)
		}
		if ev.Attacker != c.attacker || ev.Target != c.target || ev.Amount != c.amount || ev.Ability != c.ability {
			t.Errorf("%q:\n got (%s, %s, %d, %s)\nwant (%s, %s, %d, %s)",
				c.line, ev.Attacker, ev.Target, ev.Amount, ev.Ability,
				c.attacker, c.target, c.amount, c.ability)
		}
	}
}

func TestParseHealAndCC(t *testing.T) {
	ev, _ := ParseLine("[Wed Jul 15 20:15:33 2026] You healed Soandso for 5 hit points by Lifetap.", "Soandso")
	if ev == nil || ev.Kind != KHeal || ev.Amount != 5 || ev.Ability != "Lifetap" {
		t.Fatalf("heal parse failed: %+v", ev)
	}
	ev, _ = ParseLine("[Wed Jul 15 20:15:33 2026] A fire beetle staggers.", "Soandso")
	if ev == nil || ev.Kind != KCC || ev.Ability != "stun" || ev.Target != "a fire beetle" {
		t.Fatalf("stagger parse failed: %+v", ev)
	}
}

func TestParseDeaths(t *testing.T) {
	_, d := ParseLine("[Thu Jul 16 16:52:36 2026] You have slain a fire beetle!", "Soandso")
	if d == nil || d.Victim != "a fire beetle" || d.Killer != "Soandso" {
		t.Fatalf("you-slain parse failed: %+v", d)
	}
	_, d = ParseLine("[Thu Jul 16 16:52:36 2026] Soandso has been slain by a gnoll pup!", "Soandso")
	if d == nil || d.Victim != "Soandso" || d.Killer != "a gnoll pup" {
		t.Fatalf("slain-by parse failed: %+v", d)
	}
}

func TestParseLootDispositions(t *testing.T) {
	cases := []struct {
		line  string
		item  string
		count int
		disp  string
	}{
		{"[Thu Jul 16 16:52:36 2026] You looted a Sarialiyn's Lute from orc scoutsman's corpse and sold it for free.",
			"Sarialiyn's Lute", 1, "sold"},
		{"[Thu Jul 16 18:40:30 2026] You looted a Rat Eye from a large plague rat's corpse and sold it for 1 gold, 7 silver and 9 copper.",
			"Rat Eye", 1, "sold"},
		{"[Thu Jul 16 19:45:44 2026] You looted a Mote of Lesser Potential from a large plague rat's corpse and stored it in your currency",
			"Mote of Lesser Potential", 1, "stored"},
		{"[Thu Jul 16 18:44:28 2026] You looted a Skeletal Rod +2 from skeleton L`rodd's corpse to create a Skeletal Rod +4",
			"Skeletal Rod +2", 1, "merged"},
		{"[Thu Jul 16 18:50:40 2026] --You have looted a Bone Chips from a cracked skeleton's corpse.--",
			"Bone Chips", 1, "kept"},
		{"[Thu Jul 16 14:19:08 2026] You looted 2 Spiderling Eye from a jungle spiderling's corpse and sold it for 1 silver and 4 copper.",
			"Spiderling Eye", 2, "sold"},
	}
	for _, c := range cases {
		le := ParseLoot(c.line)
		if le == nil {
			t.Fatalf("no loot event for %q", c.line)
		}
		if le.Item != c.item || le.Count != c.count || le.Disposition != c.disp {
			t.Errorf("%q:\n got (%s, %d, %s)\nwant (%s, %d, %s)",
				c.line, le.Item, le.Count, le.Disposition, c.item, c.count, c.disp)
		}
	}
}

func TestXPWithAndWithoutPercent(t *testing.T) {
	m := NewMeter("Soandso", 10*time.Second)
	m.AddLine("[Mon Jul 13 08:45:30 2026] You gain experience! (2.347%)")
	m.AddLine("[Mon Jul 13 08:46:15 2026] You gain experience! (8.995%)")
	m.AddLine("[Tue Jul 21 20:35:07 2026] You gain experience!") // percent display off
	s := m.SessionSnapshot()
	if s.XPCount != 3 {
		t.Fatalf("XPCount = %d, want 3", s.XPCount)
	}
	if s.XPPct < 11.34 || s.XPPct > 11.35 {
		t.Fatalf("XPPct = %f, want ~11.342", s.XPPct)
	}
}

func TestSessionLines(t *testing.T) {
	m := NewMeter("Soandso", 10*time.Second)
	m.AddLine("[Thu Jul 16 19:38:22 2026] You have entered The Feerrott.")
	m.AddLine("[Thu Jul 16 19:40:00 2026] You have become better at Foraging! (56)")
	m.AddLine("[Thu Jul 16 19:41:00 2026] You receive 1 gold, 7 silver and 9 copper from the corpse.")
	m.AddLine("[Thu Jul 16 19:42:00 2026] You have gained a level! Welcome to level 9!")
	s := m.SessionSnapshot()
	if s.LastZone != "The Feerrott" || len(s.Zones) != 1 {
		t.Errorf("zone tracking failed: %+v", s)
	}
	if len(s.SkillUps) != 1 || s.SkillUps[0] != "Foraging (56)" {
		t.Errorf("skill-up tracking failed: %+v", s.SkillUps)
	}
	if s.LootCopper != 179 {
		t.Errorf("coin = %d copper, want 179", s.LootCopper)
	}
}

func TestParseCoin(t *testing.T) {
	if c := parseCoin("1 gold, 7 silver and 9 copper"); c != 179 {
		t.Errorf("coin = %d, want 179", c)
	}
	if c := parseCoin("2 platinum"); c != 2000 {
		t.Errorf("plat = %d, want 2000", c)
	}
	if c := parseCoin("free"); c != 0 {
		t.Errorf("free = %d, want 0", c)
	}
}

func TestSessionSplitOnQuietGap(t *testing.T) {
	m := NewMeter("Soandso", 10*time.Second)
	m.AddLine("[Thu Jul 16 14:00:00 2026] You gain experience! (1.000%)")
	m.AddLine("[Thu Jul 16 14:10:00 2026] You gain experience! (1.000%)")
	// 31 minutes of silence -> new session
	m.AddLine("[Thu Jul 16 14:41:00 2026] You gain experience! (1.000%)")
	_, sess := m.XPReport()
	if len(sess) != 2 {
		t.Fatalf("sessions = %d, want 2 (30min gap should split)", len(sess))
	}
}

func TestQuestLookupNormalization(t *testing.T) {
	for _, name := range []string{"Kobold Molar", "a Kobold Molar", "Lizard Tail", "Crushbone Belt +4"} {
		if _, ok := questLookup(name); !ok {
			t.Errorf("questLookup(%q) missed a curated entry", name)
		}
	}
	// (fun fact: even Rusty Long Sword IS a quest item — the Bladed Weapons
	// faction collection takes it. Use a fabricated name for the negative case.)
	if _, ok := questLookup("Nonexistent Test Widget"); ok {
		t.Errorf("made-up item should not be a quest item")
	}
}
