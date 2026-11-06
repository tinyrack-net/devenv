package mcpserver

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

const (
	defaultSearchResults = 200
	maxSearchResults     = 2000
	searchTimeout        = 60 * time.Second
)

type searchIn struct {
	Name           string `json:"name" jsonschema:"environment name"`
	Pattern        string `json:"pattern" jsonschema:"regular expression (ripgrep syntax); set fixed_strings for literal text"`
	Path           string `json:"path,omitempty" jsonschema:"directory or file to search; relative paths are relative to /home/dev (default /home/dev)"`
	Glob           string `json:"glob,omitempty" jsonschema:"only files matching this glob, e.g. *.go or src/**/*.ts"`
	IgnoreCase     bool   `json:"ignore_case,omitempty"`
	FixedStrings   bool   `json:"fixed_strings,omitempty" jsonschema:"treat pattern as literal text"`
	Context        int    `json:"context,omitempty" jsonschema:"lines of context around each match (max 10)"`
	FilesWithMatch bool   `json:"files_with_matches,omitempty" jsonschema:"only list matching file paths"`
	MaxResults     int    `json:"max_results,omitempty" jsonschema:"maximum output lines (default 200, max 2000)"`
	IncludeIgnored bool   `json:"include_ignored,omitempty" jsonschema:"also search files ignored by .gitignore and hidden files"`
}

type searchOut struct {
	Results   string `json:"results" jsonschema:"path:line:text lines (or paths)"`
	Count     int    `json:"count" jsonschema:"number of returned lines"`
	Truncated bool   `json:"truncated" jsonschema:"more results exist; narrow the search"`
	Engine    string `json:"engine" jsonschema:"rg or grep"`
}

func clampResults(n int) int {
	if n <= 0 {
		return defaultSearchResults
	}
	return min(n, maxSearchResults)
}

// searchScript builds a shell script running rg, falling back to grep.
func searchScript(in searchIn, p string, limit int) string {
	q := shellQuote
	var rg, gr []string
	rg = append(rg, "rg", "--line-number", "--no-heading", "--color=never", "--max-columns=500", "--max-columns-preview")
	gr = append(gr, "grep", "-rIn", "--color=never", "--exclude-dir=.git", "--exclude-dir=node_modules")
	if in.IgnoreCase {
		rg, gr = append(rg, "-i"), append(gr, "-i")
	}
	if in.FixedStrings {
		rg, gr = append(rg, "-F"), append(gr, "-F")
	} else {
		gr = append(gr, "-E")
	}
	if c := min(max(in.Context, 0), 10); c > 0 {
		rg, gr = append(rg, fmt.Sprintf("-C%d", c)), append(gr, fmt.Sprintf("-C%d", c))
	}
	if in.FilesWithMatch {
		rg, gr = append(rg, "-l"), append(gr, "-l")
	}
	if in.IncludeIgnored {
		rg = append(rg, "-uu")
	}
	if in.Glob != "" {
		rg = append(rg, "--glob", q(in.Glob))
		gr = append(gr, "--include="+q(in.Glob))
	}
	rg = append(rg, "-e", q(in.Pattern), "--", q(p))
	gr = append(gr, "-e", q(in.Pattern), "--", q(p))
	// head -n limit+1 detects truncation. Exit codes: 0 match, 1 none, 2 error.
	return fmt.Sprintf(`if command -v rg >/dev/null 2>&1; then echo rg >&2; %s; else echo grep >&2; %s; fi | head -n %d`,
		strings.Join(rg, " "), strings.Join(gr, " "), limit+1)
}

func (s *Server) search(ctx context.Context, _ *mcp.CallToolRequest, in searchIn) (*mcp.CallToolResult, searchOut, error) {
	if in.Pattern == "" {
		return nil, searchOut{}, errorf("pattern is required")
	}
	if in.Path == "" {
		in.Path = homeDir
	}
	p, err := resolvePath(in.Path)
	if err != nil {
		return nil, searchOut{}, err
	}
	limit := clampResults(in.MaxResults)
	res, err := s.runScript(ctx, in.Name, "set -o pipefail; "+searchScript(in, p, limit), searchTimeout)
	if err != nil {
		return nil, searchOut{}, err
	}
	out := searchOut{Engine: "grep"}
	stderr := res.Stderr
	if eng, rest, ok := strings.Cut(stderr, "\n"); ok && (eng == "rg" || eng == "grep") {
		out.Engine, stderr = eng, rest
	}
	// rg/grep exit 1 means no match; 141 is SIGPIPE from head (truncated).
	if res.ExitCode != 0 && res.ExitCode != 1 && res.ExitCode != 141 && strings.TrimSpace(res.Stdout) == "" {
		return nil, searchOut{}, errorf("search failed: %s", strings.TrimSpace(stderr))
	}
	if res.TimedOut {
		return nil, searchOut{}, errorf("search timed out after %s; narrow path or glob", searchTimeout)
	}
	lines := strings.Split(strings.TrimRight(res.Stdout, "\n"), "\n")
	if len(lines) == 1 && lines[0] == "" {
		lines = nil
	}
	if len(lines) > limit {
		lines = lines[:limit]
		out.Truncated = true
	}
	out.Count = len(lines)
	out.Results = strings.Join(lines, "\n")
	return nil, out, nil
}

type findIn struct {
	Name           string `json:"name" jsonschema:"environment name"`
	Pattern        string `json:"pattern,omitempty" jsonschema:"glob for file paths, e.g. *.go, **/test_*.py or src/**/*.tsx (default: all files)"`
	Path           string `json:"path,omitempty" jsonschema:"directory to search; relative paths are relative to /home/dev (default /home/dev)"`
	MaxResults     int    `json:"max_results,omitempty" jsonschema:"maximum results (default 200, max 2000)"`
	IncludeIgnored bool   `json:"include_ignored,omitempty" jsonschema:"include files ignored by .gitignore and hidden files"`
}

type findOut struct {
	Files     []string `json:"files"`
	Truncated bool     `json:"truncated"`
}

func (s *Server) findFiles(ctx context.Context, _ *mcp.CallToolRequest, in findIn) (*mcp.CallToolResult, findOut, error) {
	if in.Path == "" {
		in.Path = homeDir
	}
	p, err := resolvePath(in.Path)
	if err != nil {
		return nil, findOut{}, err
	}
	limit := clampResults(in.MaxResults)
	q := shellQuote
	rg := "rg --files --color=never"
	if in.IncludeIgnored {
		rg += " -uu"
	}
	if in.Pattern != "" {
		rg += " --glob " + q(in.Pattern)
	}
	findCmd := "find . -type f -not -path '*/.git/*' -not -path '*/node_modules/*'"
	if in.Pattern != "" {
		pat := in.Pattern
		if !strings.Contains(pat, "/") {
			findCmd += " -name " + q(pat)
		} else {
			findCmd += " -path " + q("./"+strings.ReplaceAll(pat, "**/", "*"))
		}
	}
	script := fmt.Sprintf(`set -o pipefail; cd %s || exit 2; if command -v rg >/dev/null 2>&1; then %s; else %s | sed 's|^\./||'; fi | sort | head -n %d`,
		q(p), rg, findCmd, limit+1)
	res, err := s.runScript(ctx, in.Name, script, searchTimeout)
	if err != nil {
		return nil, findOut{}, err
	}
	if res.ExitCode == 2 && strings.TrimSpace(res.Stdout) == "" {
		return nil, findOut{}, errorf("find failed: %s", strings.TrimSpace(res.Stderr))
	}
	out := findOut{Files: []string{}}
	for _, l := range strings.Split(strings.TrimRight(res.Stdout, "\n"), "\n") {
		if l != "" {
			out.Files = append(out.Files, l)
		}
	}
	if len(out.Files) > limit {
		out.Files = out.Files[:limit]
		out.Truncated = true
	}
	return nil, out, nil
}

// runScript runs a non-login bash script as dev with a timeout.
func (s *Server) runScript(ctx context.Context, name, script string, timeout time.Duration) (execOut, error) {
	sc, release, err := s.connect(ctx, name, true)
	if err != nil {
		return execOut{}, err
	}
	defer release()
	return runCommand(ctx, sc, "exec setsid bash -c "+shellQuote(script), "", timeout)
}

func (s *Server) registerSearch(srv *mcp.Server) {
	ro := &mcp.ToolAnnotations{ReadOnlyHint: true}
	mcp.AddTool(srv, &mcp.Tool{
		Name: "search",
		Description: "Search file contents with ripgrep (grep when rg is missing). Respects .gitignore and skips hidden files by default. " +
			"Returns path:line:text lines; results are capped, so narrow path or glob when truncated.",
		Annotations: ro,
	}, logged("search", s.search))
	mcp.AddTool(srv, &mcp.Tool{
		Name:        "find_files",
		Description: "Find files by glob (paths relative to the search directory). Respects .gitignore by default.",
		Annotations: ro,
	}, logged("find_files", s.findFiles))
}
