package main

import (
	"encoding/json"
	"io"
	"net/http"
	"net/url"
	"regexp"
	"strings"
	"sync"
	"time"
)

// wikiURL is the human-readable article URL for an eqlwiki page title.
func wikiURL(page string) string {
	return "https://eqlwiki.com/" + url.PathEscape(strings.ReplaceAll(page, " ", "_"))
}

var wikiCache = struct {
	sync.Mutex
	m map[string]string
}{m: map[string]string{}}

var wikiHTTP = &http.Client{Timeout: 12 * time.Second}

type wikiResp struct {
	Query struct {
		Pages map[string]struct {
			Revisions []struct {
				Slots struct {
					Main struct {
						Content string `json:"*"`
					} `json:"main"`
				} `json:"slots"`
			} `json:"revisions"`
		} `json:"pages"`
	} `json:"query"`
}

// FetchWikiText downloads a page's wikitext and renders it to readable plain
// text. Results are cached for the life of the process.
func FetchWikiText(page string) (string, error) {
	wikiCache.Lock()
	if t, ok := wikiCache.m[page]; ok {
		wikiCache.Unlock()
		return t, nil
	}
	wikiCache.Unlock()

	q := url.Values{}
	q.Set("action", "query")
	q.Set("prop", "revisions")
	q.Set("rvprop", "content")
	q.Set("rvslots", "main")
	q.Set("format", "json")
	q.Set("titles", page)
	resp, err := wikiHTTP.Get("https://eqlwiki.com/api.php?" + q.Encode())
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	b, err := io.ReadAll(io.LimitReader(resp.Body, 2<<20))
	if err != nil {
		return "", err
	}
	var wr wikiResp
	if err := json.Unmarshal(b, &wr); err != nil {
		return "", err
	}
	raw := ""
	for _, p := range wr.Query.Pages {
		if len(p.Revisions) > 0 {
			raw = p.Revisions[0].Slots.Main.Content
		}
	}
	txt := wikitextToPlain(raw)
	if txt == "" {
		txt = "(no walkthrough text found — try the link)"
	}
	wikiCache.Lock()
	wikiCache.m[page] = txt
	wikiCache.Unlock()
	return txt, nil
}

var (
	tmplRe    = regexp.MustCompile(`\{\{[^{}]*\}\}`)
	tagRe     = regexp.MustCompile(`(?s)</?(?:div|span|ul|li|br|b|i|blockquote|p|s|table|tr|td|th)[^>]*/?>`)
	linkRe    = regexp.MustCompile(`\[\[(?:[^\]|]*\|)?([^\]|]+)\]\]`)
	extLinkRe = regexp.MustCompile(`\[https?://[^ \]]+ ?([^\]]*)\]`)
	fileRe    = regexp.MustCompile(`\[\[(?:File|Image):[^\]]*\]\]`)
	nlRe      = regexp.MustCompile(`\n{3,}`)
)

// wikitextToPlain strips wiki markup down to readable walkthrough text.
func wikitextToPlain(t string) string {
	t = fileRe.ReplaceAllString(t, "")
	for i := 0; i < 5; i++ { // nested templates
		t = tmplRe.ReplaceAllString(t, "")
	}
	t = linkRe.ReplaceAllString(t, "$1")
	t = extLinkRe.ReplaceAllString(t, "$1")
	t = tagRe.ReplaceAllString(t, "\n")
	t = strings.ReplaceAll(t, "'''", "")
	t = strings.ReplaceAll(t, "''", "")
	var out []string
	for _, ln := range strings.Split(t, "\n") {
		s := strings.TrimSpace(ln)
		if strings.HasPrefix(s, "{|") || strings.HasPrefix(s, "|}") || strings.HasPrefix(s, "|-") {
			continue
		}
		s = strings.TrimSpace(strings.TrimPrefix(s, "!"))
		s = strings.TrimSpace(strings.TrimPrefix(s, "|"))
		s = strings.TrimSpace(strings.TrimPrefix(s, ":"))
		s = strings.TrimSpace(strings.TrimPrefix(s, "*"))
		s = strings.TrimSpace(strings.TrimPrefix(s, "#"))
		out = append(out, s)
	}
	t = strings.Join(out, "\n")
	t = nlRe.ReplaceAllString(t, "\n\n")
	return strings.TrimSpace(t)
}
