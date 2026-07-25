package main

import "strings"

// QuestInfo describes a known quest item: where it goes and anything Josh
// should know before the loot filter eats it. Verified in-game / on eqlwiki
// during the July 2026 beta unless marked unverified.
type QuestInfo struct {
	Quest      string
	TurnIn     string   // NPC + place
	Note       string
	Pages      []string // eqlwiki quest page titles (for links / walkthrough fetch)
	Unverified bool
}

// questDB is keyed by lowercased item name (leading article stripped).
var questDB = map[string]QuestInfo{
	// --- Crushbone ---
	"sarialiyn's lute":       {Quest: "Tranquilsong sword", TurnIn: "Sarialiyn Tranquilsong, Kelethin bard guild", Note: "also needs 2 orc jawbones; repeatable Song Weavers faction"},
	"fractured orc jawbone":  {Quest: "Tranquilsong sword", TurnIn: "Sarialiyn Tranquilsong, Kelethin bard guild", Note: "2 per turn-in with the lute"},
	"crushbone belt":         {Quest: "Crushbone Belts", TurnIn: "Canloe Nusback, S. Kaladim warrior guild (-16, 536)", Note: "XP + dwarf faction; belts do NOT stack"},
	"crushbone shoulderpads": {Quest: "Crushbone Belts (bonus)", TurnIn: "Canloe Nusback, S. Kaladim", Note: "2 legionnaire shoulderpads per reward"},
	"blue orc head":          {Quest: "The Falchion (PAL)", TurnIn: "General Jyleel, N. Felwithe paladin guild", Note: "with Black Heart + Large Locked Crate -> Falchion of the Koada'Vie"},
	"black heart":            {Quest: "The Falchion (PAL)", TurnIn: "General Jyleel, N. Felwithe", Note: "from Ambassador D'Vinn"},
	"large locked crate":     {Quest: "The Falchion (PAL)", TurnIn: "General Jyleel, N. Felwithe", Note: "from Brawn, Ocean of Tears siren rocks"},
	"tergon's spellbook":     {Quest: "Tergon's Spellbook (MAG)", TurnIn: "Tergon Brenclog, Ak'Anon library", Note: "gnome faction + scroll"},
	"rondo's tongue":         {Quest: "Parrying Pick (ROG)", TurnIn: "Diggins, N. Kaladim mining tunnels", Note: "need all 4 Dunfire tongues; kills give +Kaladim faction"},
	"barma's tongue":         {Quest: "Parrying Pick (ROG)", TurnIn: "Diggins, N. Kaladim", Note: "Butcherblock bandit camp (280, 1648)"},
	"crytil's tongue":        {Quest: "Parrying Pick (ROG)", TurnIn: "Diggins, N. Kaladim", Note: "Butcherblock bandit camp"},
	"keldyn's tongue":        {Quest: "Parrying Pick (ROG)", TurnIn: "Diggins, N. Kaladim", Note: "Butcherblock bandit camp"},

	// --- Slave keys (Crushbone) ---
	"shackle key 15": {Quest: "Slave Keys", TurnIn: "Retlon Brenclog (chained), Crushbone", Note: "XP + caster-guild faction; slave must con dubious+"},
	"shackle key 16": {Quest: "Slave Keys", TurnIn: "male dwarven slave, Crushbone"},
	"shackle key 17": {Quest: "Slave Keys", TurnIn: "female dwarven slave, Crushbone"},
	"shackle key 18": {Quest: "Slave Keys", TurnIn: "male wood elf slave, Crushbone"},
	"shackle key 19": {Quest: "Slave Keys", TurnIn: "female wood elf slave, Crushbone"},
	"shackle key 20": {Quest: "Slave Keys", TurnIn: "male high elf slave, Crushbone"},
	"shackle key 21": {Quest: "Slave Keys", TurnIn: "female high elf slave, Crushbone"},
	"shackle key 22": {Quest: "none known", TurnIn: "-", Note: "keys 22-24 have no known use; safe to sell"},
	"shackle key 23": {Quest: "none known", TurnIn: "-", Note: "safe to sell"},
	"shackle key 24": {Quest: "none known", TurnIn: "-", Note: "safe to sell"},

	// --- Gnoll Slayer chain ---
	"baby joseph sayer":              {Quest: "Gnoll Slayer (base)", TurnIn: "Marton Sayer, Qeynos Hills cottage (1381, 278)", Note: "DO NOT SELL THE BABY"},
	"gnoll's eye":                    {Quest: "Gnoll Slayer (final)", TurnIn: "Marton Sayer, Qeynos Hills", Note: "with journal + base sword -> 15/40 gnoll-bane"},
	"journal of greater enchantment": {Quest: "Gnoll Slayer (final)", TurnIn: "Marton Sayer, Qeynos Hills", Note: "from a Lteth Val Scribe, Splitpaw"},

	// --- Kobold / Odus faction economy ---
	"kobold molar": {Quest: "Kobold Molars (Good)", TurnIn: "Tiam Khonsir, Erudin (-1127, -419)", Note: "+7 Deepwater/+5 HC Erudin/-1 Heretics. NEVER turn in at Paineel (-7 Deepwater)"},
	"kobold hide":  {Quest: "Kobold Killing", TurnIn: "Tabure Ahendle, S. Qeynos arena (65, -507)", Note: "4 per turn-in; rawhide + Qeynos faction", Unverified: true},
	"kobold shaman's pouch": {Quest: "Kobold Shaman Artifacts", TurnIn: "Erudin (Deepwater)", Note: "Deepwater Knights faction"},
	"pearl of odus": {Quest: "Odus Pearls", TurnIn: "Weligon Steelherder, Deepwater temple, Erudin", Note: "combine 4 in his Empty Bag first — loose pearls are rejected"},
	"nicked coin":   {Quest: "Cleanse the Ocean", TurnIn: "Laoni Reista, Deepwater guild hall, Erudin", Note: "works at apprehensive; do dialogue chain first"},
	"heretic shark skin": {Quest: "Barnacle Breastplate", TurnIn: "Weligon Steelherder, Erudin", Note: "combine 1 + 3 Baby Shark Remains in Shark Bag"},
	"baby shark remains": {Quest: "Barnacle Breastplate", TurnIn: "Weligon Steelherder, Erudin", Note: "need 3 per breastplate"},

	// --- Fiery Avenger chain ---
	"torn, burnt book":         {Quest: "Fiery Avenger", TurnIn: "Rineval Talyas, N. Qeynos (161, -339) or Rysva To`Biath, Neriak", Note: "+ frost book + 1000pp -> Book of Scale"},
	"torn, frost-covered book": {Quest: "Fiery Avenger", TurnIn: "Rineval Talyas, N. Qeynos or Rysva To`Biath, Neriak", Note: "+ burnt book + 1000pp -> Book of Scale"},
	"torn, frost covered book": {Quest: "Fiery Avenger", TurnIn: "Rineval Talyas, N. Qeynos or Rysva To`Biath, Neriak", Note: "+ burnt book + 1000pp -> Book of Scale"},
	"book of scale":            {Quest: "Fiery Avenger", TurnIn: "Oracle of K`Arnon, Ocean of Tears east isle (-529, -8843)", Note: "-> Miragul's Phylactery; mind the Guardian"},
	"miragul's phylactery":     {Quest: "Fiery Avenger", TurnIn: "Lich of Miragul, Everfrost ice caves (via west river branch)", Note: "no charm needed; only the turner-in gets head+robe"},
	"miragul's head":           {Quest: "Fiery Avenger", TurnIn: "Inte Akera, Plane of Sky isle 4 windmill", Note: "with robe + both Blessings; needs warmly Deepwater"},
	"miragul's robe":           {Quest: "Fiery Avenger", TurnIn: "Inte Akera, Plane of Sky isle 4 windmill", Note: "with head + both Blessings"},

	// --- Neriak / Nektulos ---
	"tattered journal page 1": {Quest: "Initiate Darkpriest Mace (CLR)", TurnIn: "High Priestess Alexandria, Neriak 3rd Gate (407, -820)", Note: "get Dark Blessed Box first (say 'magical relic'); repeatable"},
	"tattered journal page 2": {Quest: "Initiate Darkpriest Mace (CLR)", TurnIn: "High Priestess Alexandria, Neriak 3rd Gate", Note: "collect all 3 pages"},
	"tattered journal page 3": {Quest: "Initiate Darkpriest Mace (CLR)", TurnIn: "High Priestess Alexandria, Neriak 3rd Gate", Note: "collect all 3 pages"},

	// --- Splitpaw / misc ---
	"preserved split paw eye": {Quest: "Splitpaw collection", TurnIn: "(Splitpaw quests)", Note: "also a Glimpse clicky, 5 charges"},
	"brain of the ishva mal":  {Quest: "Splitpaw", TurnIn: "(Splitpaw quests)", Note: "NO DROP boss drop — hold it"},
	"large empty crate":       {Quest: "Cold Iron (SHD)", TurnIn: "Royal Guard Sheltuin, Paineel", Note: "WARNING: quest LOWERS Deepwater Knights — sequence around Fiery Avenger"},
	"thick caustic fluid":     {Quest: "Tunare Warden symbol", TurnIn: "(Tunare cleric symbol quests)", Note: "only for Tunare clerics"},
	"tree snake fang":         {Quest: "ogre Beastlord task (suspected)", TurnIn: "Oggok beastlord guild?", Note: "stacks in EQL — bulk turn-in suspected", Unverified: true},
	"gnoll tongue":            {Quest: "Qeynos gnoll quests", TurnIn: "Qeynos", Note: "faction turn-in"},
	"patch of gnoll fur":      {Quest: "Qeynos gnoll quests", TurnIn: "Qeynos", Note: "faction turn-in"},
	"bone chips":              {Quest: "faction turn-ins / necro reagent", TurnIn: "various", Note: "bulk +5 faction turn-ins in EQL"},
	"lizard tail":             {Quest: "Lizard Tails (x2 quests)", TurnIn: "Horgus (Craknek) or Grevak (Greenblood), Oggok", Note: "4 tails per turn-in; armor + ogre faction; also a brewing mat"},
	"lambent stone":           {Quest: "Lambent Armor (BRD)", TurnIn: "Cryssia Stardreamer / Walthin Fireweaver, Temple of Sol Ro", Note: "hill/sand giant drop; sells to players 20-35pp — never vendor"},
}

// questLookup normalizes an item name and returns its quest info.
func questLookup(item string) (QuestInfo, bool) {
	n := strings.ToLower(strings.TrimSpace(item))
	for _, a := range []string{"a ", "an ", "the "} {
		if strings.HasPrefix(n, a) {
			n = n[len(a):]
			break
		}
	}
	// strip tier suffix: "crushbone belt +4" -> "crushbone belt"
	if i := strings.LastIndex(n, " +"); i > 0 {
		n = n[:i]
	}
	if qi, ok := questDB[n]; ok {
		// curated entries win; borrow wiki page links if they don't have their own
		if len(qi.Pages) == 0 {
			if wq, ok2 := questDBWiki[n]; ok2 {
				qi.Pages = wq.Pages
			}
		}
		return qi, true
	}
	qi, ok := questDBWiki[n]
	return qi, ok
}
