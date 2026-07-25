#!/usr/bin/env python3
"""Harvest eqlwiki quest pages -> questdb_wiki.go for EQLC.

v2: mines Category:Quests pages and classifies item links as turn-in
components (linked from walkthrough text) vs rewards (linked only from the
Reward section). Only components are emitted — rewards aren't "look out for"
items. Out-of-era quests are skipped entirely.
"""
import json, re, urllib.request, urllib.parse, sys, time
from collections import defaultdict

API = "https://eqlwiki.com/api.php?"

def api(params):
    url = API + urllib.parse.urlencode({**params, "format": "json"})
    for _ in range(3):
        try:
            return json.load(urllib.request.urlopen(url, timeout=30))
        except Exception:
            time.sleep(2)
    return {}

def category(cat):
    titles, cont = [], {}
    while True:
        d = api({"action": "query", "list": "categorymembers",
                 "cmtitle": f"Category:{cat}", "cmlimit": "500",
                 "cmnamespace": "0", **cont})
        titles += [m["title"] for m in d.get("query", {}).get("categorymembers", [])]
        cont = d.get("continue")
        if not cont:
            break
    return titles

# universe of item titles (to distinguish item links from NPC/zone links)
item_titles = set(category("Quest Items"))
print(f"quest-item universe: {len(item_titles)}", file=sys.stderr)

quests = category("Quests")
print(f"quest pages: {len(quests)}", file=sys.stderr)

OUT_ERA = re.compile(r"\{\{(Kunark|Velious|Luclin|EpicQuests|Chardok(?: Revamp)?|FearHateRevamp|HoleVP)[^}]*\}\}", re.I)
LINK = re.compile(r"\[\[([^\]|#]+)(?:\|[^\]]*)?\]\]")
REWARD_SEC = re.compile(r"==+\s*Rewards?\s*==+(.*?)(?=\n==[^=]|\Z)", re.S | re.I)

components = defaultdict(set)   # item -> quests using it as a component
reward_only = defaultdict(set)  # item -> quests giving it as a reward
skipped = 0
for i in range(0, len(quests), 50):
    d = api({"action": "query", "prop": "revisions", "rvprop": "content",
             "rvslots": "main", "titles": "|".join(quests[i:i+50])})
    for p in d.get("query", {}).get("pages", {}).values():
        revs = p.get("revisions")
        if not revs:
            continue
        t = revs[0]["slots"]["main"]["*"]
        quest = p["title"]
        if OUT_ERA.search(t):
            skipped += 1
            continue
        reward_text = " ".join(m.group(1) for m in REWARD_SEC.finditer(t))
        reward_links = {l.strip() for l in LINK.findall(reward_text)}
        body_no_reward = REWARD_SEC.sub(" ", t)
        for l in LINK.findall(body_no_reward):
            l = l.strip()
            if l in item_titles:
                components[l].add(quest)
        for l in reward_links:
            if l in item_titles:
                reward_only[l].add(quest)
    print(f"quests {min(i+50,len(quests))}/{len(quests)}", file=sys.stderr)

# an item that is a component anywhere is a component, full stop
for item in components:
    reward_only.pop(item, None)

print(f"components: {len(components)}  reward-only (excluded): {len(reward_only)}  out-of-era quests skipped: {skipped}", file=sys.stderr)

def norm(s):
    s = s.lower().strip()
    for a in ("a ", "an ", "the "):
        if s.startswith(a):
            s = s[len(a):]
            break
    if " +" in s:
        s = s[: s.rindex(" +")]
    return s

seen, lines = set(), []
for item in sorted(components):
    k = norm(item)
    if k in seen:
        continue
    seen.add(k)
    pages = sorted(components[item])[:3]
    qs = "; ".join(pages).replace('"', "'")
    pg = ", ".join('"%s"' % p.replace('"', "'") for p in pages)
    lines.append(f'\t"{k}": {{Quest: "{qs}", Pages: []string{{{pg}}}}},')

go = """package main

// Code generated from eqlwiki quest pages (%d turn-in components; reward-only
// items excluded). Curated entries in questdb.go take precedence.

var questDBWiki = map[string]QuestInfo{
%s
}
""" % (len(lines), "\n".join(lines))
open("/Users/user/eql/eqdps/questdb_wiki.go", "w").write(go)
print(f"wrote questdb_wiki.go with {len(lines)} entries", file=sys.stderr)
