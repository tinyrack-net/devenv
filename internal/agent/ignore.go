package agent

import (
	"bufio"
	"os"
	"path"
	"strings"

	"github.com/bmatcuk/doublestar/v4"
)

// ignoreRule is one .gitignore line, relative to the directory of its file.
type ignoreRule struct {
	base    string // directory of the .gitignore, relative to the walk root ("" for root)
	pattern string
	negate  bool
	dirOnly bool
	// anchored patterns contain a slash (other than a trailing one) and
	// match from base; others match at any depth below base.
	anchored bool
}

// ignoreSet holds rules in file order; later rules win (gitignore semantics).
type ignoreSet []ignoreRule

// alwaysSkip are never searched unless include_ignored.
var alwaysSkip = map[string]bool{".git": true, "node_modules": true, ".hg": true, ".svn": true}

func parseIgnoreFile(file, base string) ignoreSet {
	f, err := os.Open(file)
	if err != nil {
		return nil
	}
	defer f.Close()
	var rules ignoreSet
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		line := strings.TrimRight(sc.Text(), " \t\r")
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		r := ignoreRule{base: base}
		if strings.HasPrefix(line, "!") {
			r.negate = true
			line = line[1:]
		} else if strings.HasPrefix(line, `\`) {
			line = line[1:]
		}
		if strings.HasSuffix(line, "/") {
			r.dirOnly = true
			line = strings.TrimSuffix(line, "/")
		}
		if strings.Contains(line, "/") {
			r.anchored = true
			line = strings.TrimPrefix(line, "/")
		}
		if line == "" {
			continue
		}
		r.pattern = line
		rules = append(rules, r)
	}
	return rules
}

// ignored reports whether rel (slash separated, relative to the walk root)
// is ignored by the rules.
func (s ignoreSet) ignored(rel string, isDir bool) bool {
	ignored := false
	for _, r := range s {
		if r.dirOnly && !isDir {
			continue
		}
		p := rel
		if r.base != "" {
			if !strings.HasPrefix(rel, r.base+"/") {
				continue
			}
			p = strings.TrimPrefix(rel, r.base+"/")
		}
		var match bool
		if r.anchored {
			match, _ = doublestar.Match(r.pattern, p)
			if !match {
				// "dir/sub" also ignores everything below it.
				match, _ = doublestar.Match(r.pattern+"/**", p)
			}
		} else {
			match, _ = doublestar.Match(r.pattern, path.Base(p))
		}
		if match {
			ignored = !r.negate
		}
	}
	return ignored
}
