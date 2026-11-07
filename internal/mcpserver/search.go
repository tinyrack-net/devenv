package mcpserver

import (
	"context"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/tinyrack-net/devenv/internal/agent"
)

const (
	defaultSearchResults = 200
	maxSearchResults     = 2000
	searchTimeout        = 60 * time.Second
)

type searchIn struct {
	Name           string `json:"name" jsonschema:"environment name"`
	Pattern        string `json:"pattern" jsonschema:"regular expression (Go RE2 syntax); set fixed_strings for literal text"`
	Path           string `json:"path,omitempty" jsonschema:"directory or file to search; relative paths are relative to /home/dev (default /home/dev)"`
	Glob           string `json:"glob,omitempty" jsonschema:"only files matching this glob, e.g. *.go or src/**/*.ts"`
	IgnoreCase     bool   `json:"ignore_case,omitempty"`
	FixedStrings   bool   `json:"fixed_strings,omitempty" jsonschema:"treat pattern as literal text"`
	Context        int    `json:"context,omitempty" jsonschema:"lines of context around each match (max 10)"`
	FilesWithMatch bool   `json:"files_with_matches,omitempty" jsonschema:"only list matching file paths"`
	MaxResults     int    `json:"max_results,omitempty" jsonschema:"maximum output lines (default 200, max 2000)"`
	IncludeIgnored bool   `json:"include_ignored,omitempty" jsonschema:"also search files ignored by .gitignore, hidden files, .git and node_modules"`
}

type searchOut struct {
	Results      string `json:"results" jsonschema:"path:line:text lines for matches, path-line-text for context, -- between groups (or paths)"`
	Count        int    `json:"count" jsonschema:"number of returned lines"`
	Truncated    bool   `json:"truncated" jsonschema:"more results exist; narrow the search"`
	FilesScanned int    `json:"files_scanned"`
}

func clampResults(n int) int {
	if n <= 0 {
		return defaultSearchResults
	}
	return min(n, maxSearchResults)
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
	res, err := callEnv[agent.SearchResult](s, ctx, in.Name, "search", agent.SearchRequest{
		Pattern: in.Pattern, Path: p, Glob: in.Glob, IgnoreCase: in.IgnoreCase, FixedStrings: in.FixedStrings,
		Context: in.Context, FilesOnly: in.FilesWithMatch, MaxResults: clampResults(in.MaxResults), IncludeIgnored: in.IncludeIgnored,
	}, searchTimeout)
	if err != nil {
		return nil, searchOut{}, err
	}
	return nil, searchOut{Results: res.Results, Count: res.Count, Truncated: res.Truncated, FilesScanned: res.FilesScanned}, nil
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
	res, err := callEnv[agent.FindResult](s, ctx, in.Name, "find", agent.FindRequest{
		Pattern: in.Pattern, Path: p, MaxResults: clampResults(in.MaxResults), IncludeIgnored: in.IncludeIgnored,
	}, searchTimeout)
	if err != nil {
		return nil, findOut{}, err
	}
	return nil, findOut{Files: res.Files, Truncated: res.Truncated}, nil
}

func (s *Server) registerSearch(srv *mcp.Server) {
	ro := &mcp.ToolAnnotations{ReadOnlyHint: true}
	mcp.AddTool(srv, &mcp.Tool{
		Name: "search",
		Description: "Search file contents with a regular expression. Respects .gitignore and skips hidden files, .git, node_modules and binary files by default. " +
			"Returns path:line:text lines; results are capped, so narrow path or glob when truncated.",
		Annotations: ro,
	}, logged("search", s.search))
	mcp.AddTool(srv, &mcp.Tool{
		Name:        "find_files",
		Description: "Find files by glob (paths relative to the search directory). Respects .gitignore by default.",
		Annotations: ro,
	}, logged("find_files", s.findFiles))
}
