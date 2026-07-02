package main

import (
	"bufio"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"sort"
	"strings"
	"time"
)

// discoverLogs returns eqlog_*.txt candidates, newest first.
func discoverLogs() []string {
	home, _ := os.UserHomeDir()
	pats := []string{
		filepath.Join(home, ".local/share/wineprefixes/*/drive_c/**/Logs/eqlog_*.txt"),
		filepath.Join(home, ".wine/drive_c/**/Logs/eqlog_*.txt"),
		filepath.Join(home, "Library/Application Support/CrossOver/Bottles/*/drive_c/**/Logs/eqlog_*.txt"),
		filepath.Join(home, "Documents/eqlogs/eqlog_*.txt"), // Parallels VM: game Logs dir symlinked to the shared Mac folder
		"eqlog_*.txt",
	}
	if runtime.GOOS == "windows" {
		pats = append(pats,
			`C:\Users\Public\Daybreak Game Company\Installed Games\**\Logs\eqlog_*.txt`,
			`C:\Program Files (x86)\**\Logs\eqlog_*.txt`,
			`C:\Program Files\**\Logs\eqlog_*.txt`,
		)
	}
	seen := map[string]bool{}
	var out []string
	for _, p := range pats {
		for _, f := range globStar(p) {
			if !seen[f] {
				seen[f] = true
				out = append(out, f)
			}
		}
	}
	sort.Slice(out, func(i, j int) bool { return mtime(out[i]).After(mtime(out[j])) })
	return out
}

func mtime(p string) time.Time {
	if fi, err := os.Stat(p); err == nil {
		return fi.ModTime()
	}
	return time.Time{}
}

// globStar expands a pattern that may contain a single "**" (recursive dir).
// filepath.Glob has no "**", so we split on it and walk.
func globStar(pat string) []string {
	if !strings.Contains(pat, "**") {
		m, _ := filepath.Glob(pat)
		return m
	}
	parts := strings.SplitN(pat, "**", 2)
	prefix := strings.TrimRight(parts[0], string(os.PathSeparator)+"/")
	suffix := strings.TrimLeft(parts[1], string(os.PathSeparator)+"/")
	// prefix itself may contain a glob (e.g. the wineprefixes/*) -> expand it.
	var bases []string
	if strings.ContainsAny(prefix, "*?[") {
		bases, _ = filepath.Glob(prefix)
	} else {
		bases = []string{prefix}
	}
	var out []string
	for _, base := range bases {
		_ = filepath.WalkDir(base, func(path string, d os.DirEntry, err error) error {
			if err != nil || !d.IsDir() {
				return nil
			}
			if m, _ := filepath.Glob(filepath.Join(path, suffix)); len(m) > 0 {
				out = append(out, m...)
			}
			return nil
		})
	}
	return out
}

var playerRe = regexp.MustCompile(`eqlog_([^_]+)_`)

func playerFromPath(p string) string {
	if m := playerRe.FindStringSubmatch(filepath.Base(p)); m != nil {
		return m[1]
	}
	return "You"
}

// tail follows a file, sending appended lines to out until stop is closed.
// Handles truncation/rotation. If fromStart is true it reads from the top.
func tail(path string, fromStart bool, out chan<- string, stop <-chan struct{}) error {
	f, err := os.Open(path)
	if err != nil {
		return err
	}
	defer f.Close()
	if !fromStart {
		f.Seek(0, io.SeekEnd)
	}
	r := bufio.NewReader(f)
	for {
		select {
		case <-stop:
			return nil
		default:
		}
		line, err := r.ReadString('\n')
		if len(line) > 0 && strings.HasSuffix(line, "\n") {
			select {
			case out <- strings.TrimRight(line, "\r\n"):
			case <-stop:
				return nil
			}
			continue
		}
		// no full line yet: rewind the partial read and wait
		if len(line) > 0 {
			f.Seek(-int64(len(line)), io.SeekCurrent)
			r.Reset(f)
		}
		if err == io.EOF {
			select {
			case <-stop:
				return nil
			case <-time.After(250 * time.Millisecond):
			}
			if pos, _ := f.Seek(0, io.SeekCurrent); fileSize(path) < pos {
				f.Seek(0, io.SeekStart) // rotated/truncated
				r.Reset(f)
			}
			continue
		}
		if err != nil {
			return err
		}
	}
}

func fileSize(p string) int64 {
	if fi, err := os.Stat(p); err == nil {
		return fi.Size()
	}
	return 0
}

// seedFile bulk-parses an existing log to build encounter history, then returns.
func seedFile(path string, m *Meter) {
	f, err := os.Open(path)
	if err != nil {
		return
	}
	defer f.Close()
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 1<<20), 1<<20)
	for sc.Scan() {
		m.AddLine(sc.Text())
	}
}
