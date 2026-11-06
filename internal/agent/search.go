package agent

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"regexp"
	"sort"
	"strings"

	"github.com/bmatcuk/doublestar/v4"
)

const (
	maxSearchFileSize = 2 << 20
	maxLineLen        = 500
	maxFilesScanned   = 200000
)

// walker walks a tree honoring .gitignore files and skip lists.
type walker struct {
	root           string
	includeIgnored bool
	glob           string
	scanned        int
}

// walk calls fn for each regular file (rel is relative to root). fn returns
// errStop to end early.
var errStop = errors.New("stop")

func (w *walker) walk(fn func(abs, rel string) error) error {
	st, err := os.Stat(w.root)
	if err != nil {
		return err
	}
	if !st.IsDir() {
		return fn(w.root, path.Base(w.root))
	}
	var rules ignoreSet
	err = filepath.WalkDir(w.root, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			if p == w.root {
				return err
			}
			if d != nil && d.IsDir() {
				return fs.SkipDir // unreadable directory
			}
			return nil
		}
		rel, _ := filepath.Rel(w.root, p)
		rel = filepath.ToSlash(rel)
		if d.IsDir() {
			if p == w.root {
				if !w.includeIgnored {
					rules = append(rules, parseIgnoreFile(filepath.Join(p, ".gitignore"), "")...)
				}
				return nil
			}
			name := d.Name()
			if !w.includeIgnored && (alwaysSkip[name] || strings.HasPrefix(name, ".") || rules.ignored(rel, true)) {
				return fs.SkipDir
			}
			if !w.includeIgnored {
				rules = append(rules, parseIgnoreFile(filepath.Join(p, ".gitignore"), rel)...)
			}
			return nil
		}
		if !d.Type().IsRegular() {
			return nil
		}
		if !w.includeIgnored && (strings.HasPrefix(d.Name(), ".") || rules.ignored(rel, false)) {
			return nil
		}
		if w.glob != "" && !globMatch(w.glob, rel) {
			return nil
		}
		w.scanned++
		if w.scanned > maxFilesScanned {
			return errStop
		}
		return fn(p, rel)
	})
	if errors.Is(err, errStop) {
		return nil
	}
	return err
}

// globMatch matches a glob against a relative path. Globs without a slash
// match the file name at any depth (like ripgrep --glob).
func globMatch(glob, rel string) bool {
	if !strings.Contains(glob, "/") {
		ok, _ := doublestar.Match(glob, path.Base(rel))
		return ok
	}
	ok, _ := doublestar.Match(strings.TrimPrefix(glob, "./"), rel)
	return ok
}

// Search greps file contents.
func Search(req SearchRequest) (SearchResult, error) {
	if req.Pattern == "" {
		return SearchResult{}, errors.New("pattern is required")
	}
	pat := req.Pattern
	if req.FixedStrings {
		pat = regexp.QuoteMeta(pat)
	}
	if req.IgnoreCase {
		pat = "(?i)" + pat
	}
	re, err := regexp.Compile(pat)
	if err != nil {
		return SearchResult{}, fmt.Errorf("invalid regular expression: %v (Go RE2 syntax; set fixed_strings for literal text)", err)
	}
	if _, err := doublestar.Match(req.Glob, "x"); req.Glob != "" && err != nil {
		return SearchResult{}, fmt.Errorf("invalid glob %q: %v", req.Glob, err)
	}
	limit := max(req.MaxResults, 1)
	ctxLines := min(max(req.Context, 0), 10)

	w := &walker{root: req.Path, includeIgnored: req.IncludeIgnored, glob: req.Glob}
	var out []string
	truncated := false
	single := false
	if st, err := os.Stat(req.Path); err == nil && !st.IsDir() {
		single = true
	}
	err = w.walk(func(abs, rel string) error {
		if single {
			rel = abs
		}
		lines, err := grepFile(abs, re, ctxLines, req.FilesOnly)
		if err != nil || len(lines) == 0 {
			return nil
		}
		if req.FilesOnly {
			out = append(out, rel)
		} else {
			for _, l := range lines {
				if l == sep {
					out = append(out, "--")
				} else {
					out = append(out, rel+l)
				}
			}
		}
		if len(out) > limit {
			truncated = true
			return errStop
		}
		return nil
	})
	if err != nil {
		return SearchResult{}, err
	}
	if len(out) > limit {
		out = out[:limit]
		truncated = true
	}
	return SearchResult{Results: strings.Join(out, "\n"), Count: len(out), Truncated: truncated, FilesScanned: w.scanned}, nil
}

// sep marks a gap between context groups.
const sep = "--"

// grepFile returns ":line:text" (matches), "-line-text" (context) and sep lines.
func grepFile(p string, re *regexp.Regexp, ctxLines int, filesOnly bool) ([]string, error) {
	f, err := os.Open(p)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	st, err := f.Stat()
	if err != nil || st.Size() > maxSearchFileSize {
		return nil, err
	}
	data, err := io.ReadAll(f)
	if err != nil {
		return nil, err
	}
	if bytes.IndexByte(data[:min(len(data), 8000)], 0) >= 0 {
		return nil, nil // binary
	}
	lines := strings.Split(string(data), "\n")
	if len(lines) > 0 && lines[len(lines)-1] == "" {
		lines = lines[:len(lines)-1]
	}
	var out []string
	lastPrinted := -1
	for i, line := range lines {
		if !re.MatchString(line) {
			continue
		}
		if filesOnly {
			return []string{""}, nil
		}
		from := max(i-ctxLines, lastPrinted+1)
		if ctxLines > 0 && lastPrinted >= 0 && from > lastPrinted+1 {
			out = append(out, sep)
		}
		for j := from; j < i; j++ {
			out = append(out, fmt.Sprintf("-%d-%s", j+1, clip(lines[j])))
		}
		out = append(out, fmt.Sprintf(":%d:%s", i+1, clip(line)))
		lastPrinted = i
		for j := i + 1; j <= min(i+ctxLines, len(lines)-1); j++ {
			if re.MatchString(lines[j]) {
				break
			}
			out = append(out, fmt.Sprintf("-%d-%s", j+1, clip(lines[j])))
			lastPrinted = j
		}
	}
	return out, nil
}

func clip(s string) string {
	s = strings.TrimRight(s, "\r")
	if len(s) > maxLineLen {
		return s[:maxLineLen] + " … [line truncated]"
	}
	return s
}

// Find lists files matching a glob.
func Find(req FindRequest) (FindResult, error) {
	if req.Pattern != "" {
		if _, err := doublestar.Match(req.Pattern, "x"); err != nil {
			return FindResult{}, fmt.Errorf("invalid glob %q: %v", req.Pattern, err)
		}
	}
	limit := max(req.MaxResults, 1)
	w := &walker{root: req.Path, includeIgnored: req.IncludeIgnored, glob: req.Pattern}
	var files []string
	err := w.walk(func(_, rel string) error {
		files = append(files, rel)
		return nil
	})
	if err != nil {
		return FindResult{}, err
	}
	sort.Strings(files)
	res := FindResult{Files: files}
	if len(files) > limit {
		res.Files = files[:limit]
		res.Truncated = true
	}
	if res.Files == nil {
		res.Files = []string{}
	}
	return res, nil
}
