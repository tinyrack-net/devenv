package mcpserver

import (
	"bytes"
	"context"
	"encoding/base64"
	"fmt"
	"io"
	"net/http"
	"os"
	"path"
	"sort"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/pkg/sftp"
)

// homeDir is the environment user's home; a variable for tests.
var homeDir = "/home/dev"

const (
	// maxReadBytes caps the file size read_file loads.
	maxReadBytes = 10 << 20
	// maxReadOutput caps the returned text.
	maxReadOutput  = 256 << 10
	defaultReadMax = 2000
	maxWriteBytes  = 5 << 20
	maxBinaryBytes = 5 << 20
	maxListEntries = 1000
)

// resolvePath makes relative paths relative to /home/dev and cleans them.
func resolvePath(p string) (string, error) {
	p = strings.TrimSpace(p)
	if p == "" {
		return "", errorf("path is required")
	}
	if strings.HasPrefix(p, "~/") || p == "~" {
		p = homeDir + strings.TrimPrefix(p, "~")
	}
	if !path.IsAbs(p) {
		p = path.Join(homeDir, p)
	}
	return path.Clean(p), nil
}

// withSFTP runs fn with an SFTP client to the environment.
func (s *Server) withSFTP(ctx context.Context, name string, fn func(*sftp.Client) error) error {
	sc, release, err := s.connect(ctx, name, true)
	if err != nil {
		return err
	}
	defer release()
	c, err := sftp.NewClient(sc)
	if err != nil {
		return errorf("open sftp session: %v", err)
	}
	defer c.Close()
	done := make(chan error, 1)
	go func() { done <- fn(c) }()
	select {
	case err := <-done:
		return err
	case <-ctx.Done():
		return errorf("cancelled")
	}
}

func fsErr(p string, err error) error {
	if err == nil {
		return nil
	}
	if os.IsNotExist(err) || strings.Contains(err.Error(), "file does not exist") {
		return errorf("%s: no such file or directory", p)
	}
	if os.IsPermission(err) || strings.Contains(err.Error(), "permission denied") {
		return errorf("%s: permission denied (files outside /home/dev may need exec with sudo)", p)
	}
	if _, ok := err.(*toolError); ok {
		return err
	}
	return errorf("%s: %v", p, err)
}

func isBinary(b []byte) bool {
	sample := b[:min(len(b), 8000)]
	return bytes.IndexByte(sample, 0) >= 0 || !utf8.Valid(trimPartialRune(sample))
}

// trimPartialRune drops a trailing incomplete UTF-8 sequence from a sample.
func trimPartialRune(b []byte) []byte {
	for i := 0; i < 3 && len(b) > 0; i++ {
		if r, _ := utf8.DecodeLastRune(b); r != utf8.RuneError {
			return b
		}
		b = b[:len(b)-1]
	}
	return b
}

// ---- read_file

type readIn struct {
	Name     string `json:"name" jsonschema:"environment name"`
	Path     string `json:"path" jsonschema:"file path; relative paths are relative to /home/dev"`
	Offset   int    `json:"offset,omitempty" jsonschema:"first line to return, 1-based (default 1)"`
	Limit    int    `json:"limit,omitempty" jsonschema:"maximum number of lines (default 2000)"`
	Encoding string `json:"encoding,omitempty" jsonschema:"text (default) or base64 to read any file (up to 5MB) as base64"`
}

type readOut struct {
	Path       string `json:"path"`
	Base64     string `json:"base64,omitempty" jsonschema:"file content when encoding is base64"`
	MIMEType   string `json:"mime_type,omitempty" jsonschema:"detected type for images and base64 reads"`
	Width      int    `json:"width,omitempty" jsonschema:"image width in pixels (original)"`
	Height     int    `json:"height,omitempty" jsonschema:"image height in pixels (original)"`
	Content    string `json:"content" jsonschema:"requested lines, prefixed with line numbers and a tab"`
	StartLine  int    `json:"start_line"`
	EndLine    int    `json:"end_line"`
	TotalLines int    `json:"total_lines"`
	Truncated  bool   `json:"truncated" jsonschema:"more lines exist after end_line; read again with offset"`
	SizeBytes  int64  `json:"size_bytes"`
}

func (s *Server) readFile(ctx context.Context, _ *mcp.CallToolRequest, in readIn) (*mcp.CallToolResult, readOut, error) {
	p, err := resolvePath(in.Path)
	if err != nil {
		return nil, readOut{}, err
	}
	var data []byte
	var size int64
	err = s.withSFTP(ctx, in.Name, func(c *sftp.Client) error {
		st, err := c.Stat(p)
		if err != nil {
			return fsErr(p, err)
		}
		if st.IsDir() {
			return errorf("%s is a directory; use list_dir", p)
		}
		size = st.Size()
		if size > maxReadBytes {
			return errorf("%s is %d bytes (limit %d); use exec with head, tail, sed -n or grep", p, size, maxReadBytes)
		}
		f, err := c.Open(p)
		if err != nil {
			return fsErr(p, err)
		}
		defer f.Close()
		data, err = io.ReadAll(io.LimitReader(f, maxReadBytes+1))
		return fsErr(p, err)
	})
	if err != nil {
		return nil, readOut{}, err
	}
	switch {
	case in.Encoding == "base64":
		if len(data) > maxBinaryBytes {
			return nil, readOut{}, errorf("%s is %d bytes; base64 reads are limited to %d bytes", p, len(data), maxBinaryBytes)
		}
		return nil, readOut{Path: p, SizeBytes: size, MIMEType: http.DetectContentType(data), Base64: base64.StdEncoding.EncodeToString(data)}, nil
	case in.Encoding != "" && in.Encoding != "text":
		return nil, readOut{}, errorf("encoding must be text or base64")
	}
	// Images are returned as image content the model can see.
	if mime := imageMIME(data); mime != "" {
		img, outMIME, w, h, err := prepareImage(data, mime)
		if err != nil {
			return nil, readOut{}, errorf("%s: decode image: %v", p, err)
		}
		out := readOut{Path: p, SizeBytes: size, MIMEType: mime, Width: w, Height: h}
		note := fmt.Sprintf("%s: %s image, %dx%d, %d bytes", p, mime, w, h, size)
		if outMIME != mime || len(img) != len(data) {
			note += " (scaled down for viewing)"
		}
		return &mcp.CallToolResult{Content: []mcp.Content{
			&mcp.ImageContent{Data: img, MIMEType: outMIME},
			&mcp.TextContent{Text: note},
		}}, out, nil
	}
	if isBinary(data) {
		return nil, readOut{}, errorf("%s looks like a binary file (%d bytes, %s); read it with encoding base64 or inspect it with exec", p, size, http.DetectContentType(data))
	}

	text := string(data)
	lines := strings.SplitAfter(text, "\n")
	if len(lines) > 0 && lines[len(lines)-1] == "" {
		lines = lines[:len(lines)-1]
	}
	offset := max(in.Offset, 1)
	limit := in.Limit
	if limit <= 0 {
		limit = defaultReadMax
	}
	out := readOut{Path: p, TotalLines: len(lines), SizeBytes: size, StartLine: offset}
	if offset > len(lines) && len(lines) > 0 {
		return nil, readOut{}, errorf("offset %d is beyond the end of the file (%d lines)", offset, len(lines))
	}
	var b strings.Builder
	end := offset - 1
	for i := offset - 1; i < len(lines) && i < offset-1+limit; i++ {
		line := lines[i]
		if len(line) > 2000 {
			line = line[:2000] + "… [line truncated]\n"
		}
		entry := fmt.Sprintf("%6d\t%s", i+1, line)
		if b.Len()+len(entry) > maxReadOutput {
			break
		}
		b.WriteString(entry)
		end = i + 1
	}
	out.Content = b.String()
	if out.Content != "" && !strings.HasSuffix(out.Content, "\n") {
		out.Content += "\n"
	}
	out.EndLine = end
	out.Truncated = end < len(lines)
	if len(lines) == 0 {
		out.StartLine, out.EndLine = 0, 0
	}
	return nil, out, nil
}

// ---- write_file

type writeIn struct {
	Name     string `json:"name" jsonschema:"environment name"`
	Path     string `json:"path" jsonschema:"file path; relative paths are relative to /home/dev. Parent directories are created"`
	Content  string `json:"content" jsonschema:"complete new file content"`
	Encoding string `json:"encoding,omitempty" jsonschema:"text (default) or base64 when content is base64 encoded binary data"`
}

type writeOut struct {
	Path    string `json:"path"`
	Bytes   int    `json:"bytes"`
	Created bool   `json:"created" jsonschema:"true when the file did not exist"`
}

// writeAtomic writes data to a temp file next to p and renames it over p,
// keeping the existing file mode.
func writeAtomic(c *sftp.Client, p string, data []byte) (created bool, err error) {
	mode := os.FileMode(0o644)
	if st, err := c.Stat(p); err == nil {
		if st.IsDir() {
			return false, errorf("%s is a directory", p)
		}
		mode = st.Mode().Perm()
	} else if os.IsNotExist(err) {
		created = true
	} else {
		return false, fsErr(p, err)
	}
	if err := c.MkdirAll(path.Dir(p)); err != nil {
		return false, fsErr(path.Dir(p), err)
	}
	tmp := fmt.Sprintf("%s/.%s.devenv-%d.tmp", path.Dir(p), path.Base(p), time.Now().UnixNano())
	f, err := c.OpenFile(tmp, os.O_WRONLY|os.O_CREATE|os.O_TRUNC)
	if err != nil {
		return false, fsErr(p, err)
	}
	if _, err := f.Write(data); err != nil {
		f.Close()
		_ = c.Remove(tmp)
		return false, fsErr(p, err)
	}
	if err := f.Close(); err != nil {
		_ = c.Remove(tmp)
		return false, fsErr(p, err)
	}
	_ = c.Chmod(tmp, mode)
	if err := c.PosixRename(tmp, p); err != nil {
		_ = c.Remove(tmp)
		return false, fsErr(p, err)
	}
	return created, nil
}

func (s *Server) writeFile(ctx context.Context, _ *mcp.CallToolRequest, in writeIn) (*mcp.CallToolResult, writeOut, error) {
	p, err := resolvePath(in.Path)
	if err != nil {
		return nil, writeOut{}, err
	}
	data := []byte(in.Content)
	switch in.Encoding {
	case "", "text":
	case "base64":
		data, err = base64.StdEncoding.DecodeString(strings.TrimSpace(in.Content))
		if err != nil {
			return nil, writeOut{}, errorf("content is not valid base64: %v", err)
		}
	default:
		return nil, writeOut{}, errorf("encoding must be text or base64")
	}
	if len(data) > maxWriteBytes {
		return nil, writeOut{}, errorf("content is larger than %d bytes", maxWriteBytes)
	}
	out := writeOut{Path: p, Bytes: len(data)}
	err = s.withSFTP(ctx, in.Name, func(c *sftp.Client) error {
		created, err := writeAtomic(c, p, data)
		out.Created = created
		return err
	})
	if err != nil {
		return nil, writeOut{}, err
	}
	return nil, out, nil
}

// ---- edit_file

type edit struct {
	OldText    string `json:"old_text" jsonschema:"exact text to replace, including whitespace and indentation; must match exactly once unless replace_all"`
	NewText    string `json:"new_text" jsonschema:"replacement text"`
	ReplaceAll bool   `json:"replace_all,omitempty" jsonschema:"replace every occurrence instead of requiring a unique match"`
}

type editIn struct {
	Name  string `json:"name" jsonschema:"environment name"`
	Path  string `json:"path" jsonschema:"file path; relative paths are relative to /home/dev"`
	Edits []edit `json:"edits" jsonschema:"replacements, matched against the original file; they must not overlap"`
}

type editOut struct {
	Path         string `json:"path"`
	Replacements int    `json:"replacements"`
	Diff         string `json:"diff" jsonschema:"unified diff of the change"`
}

type span struct{ start, end int }

// applyEdits applies edits matched against the original content.
func applyEdits(orig string, edits []edit) (string, int, error) {
	if len(edits) == 0 {
		return "", 0, errorf("edits is empty")
	}
	type repl struct {
		span
		text string
		idx  int
	}
	var repls []repl
	for i, e := range edits {
		if e.OldText == "" {
			return "", 0, errorf("edit %d: old_text is empty; use write_file to create or replace a whole file", i)
		}
		count := strings.Count(orig, e.OldText)
		switch {
		case count == 0:
			hint := ""
			if strings.Contains(strings.Join(strings.Fields(orig), " "), strings.Join(strings.Fields(e.OldText), " ")) {
				hint = " (it matches when whitespace is ignored: check indentation, tabs and line endings)"
			}
			return "", 0, errorf("edit %d: old_text not found%s; read_file the region again and copy it exactly", i, hint)
		case count > 1 && !e.ReplaceAll:
			return "", 0, errorf("edit %d: old_text matches %d places; add surrounding lines to make it unique or set replace_all", i, count)
		}
		from := 0
		for {
			j := strings.Index(orig[from:], e.OldText)
			if j < 0 {
				break
			}
			st := from + j
			repls = append(repls, repl{span{st, st + len(e.OldText)}, e.NewText, i})
			from = st + len(e.OldText)
			if !e.ReplaceAll {
				break
			}
		}
	}
	sort.Slice(repls, func(a, b int) bool { return repls[a].start < repls[b].start })
	for k := 1; k < len(repls); k++ {
		if repls[k].start < repls[k-1].end {
			return "", 0, errorf("edits %d and %d overlap; merge them into one edit", repls[k-1].idx, repls[k].idx)
		}
	}
	var b strings.Builder
	last := 0
	for _, r := range repls {
		b.WriteString(orig[last:r.start])
		b.WriteString(r.text)
		last = r.end
	}
	b.WriteString(orig[last:])
	return b.String(), len(repls), nil
}

func (s *Server) editFile(ctx context.Context, _ *mcp.CallToolRequest, in editIn) (*mcp.CallToolResult, editOut, error) {
	p, err := resolvePath(in.Path)
	if err != nil {
		return nil, editOut{}, err
	}
	out := editOut{Path: p}
	err = s.withSFTP(ctx, in.Name, func(c *sftp.Client) error {
		f, err := c.Open(p)
		if err != nil {
			return fsErr(p, err)
		}
		data, err := io.ReadAll(io.LimitReader(f, maxReadBytes+1))
		f.Close()
		if err != nil {
			return fsErr(p, err)
		}
		if len(data) > maxReadBytes {
			return errorf("%s is larger than %d bytes", p, maxReadBytes)
		}
		if isBinary(data) {
			return errorf("%s looks like a binary file", p)
		}
		orig := string(data)
		// Accept LF in old_text for CRLF files.
		crlf := strings.Contains(orig, "\r\n")
		edits := in.Edits
		if crlf {
			edits = make([]edit, len(in.Edits))
			for i, e := range in.Edits {
				e.OldText = toCRLF(e.OldText)
				e.NewText = toCRLF(e.NewText)
				edits[i] = e
			}
		}
		updated, n, err := applyEdits(orig, edits)
		if err != nil {
			return err
		}
		if updated == orig {
			return errorf("edits produce no change")
		}
		if _, err := writeAtomic(c, p, []byte(updated)); err != nil {
			return err
		}
		out.Replacements = n
		out.Diff = unifiedDiff(p, orig, updated)
		return nil
	})
	if err != nil {
		return nil, editOut{}, err
	}
	return nil, out, nil
}

func toCRLF(s string) string {
	return strings.ReplaceAll(strings.ReplaceAll(s, "\r\n", "\n"), "\n", "\r\n")
}

// unifiedDiff renders a compact diff: changed region with 3 lines of
// context. Edits are usually local, so one hunk spanning the first to the
// last changed line is clear enough.
func unifiedDiff(name, a, b string) string {
	al := strings.SplitAfter(a, "\n")
	bl := strings.SplitAfter(b, "\n")
	pre := 0
	for pre < len(al) && pre < len(bl) && al[pre] == bl[pre] {
		pre++
	}
	suf := 0
	for suf < len(al)-pre && suf < len(bl)-pre && al[len(al)-1-suf] == bl[len(bl)-1-suf] {
		suf++
	}
	const ctx = 3
	s := max(pre-ctx, 0)
	ae := min(len(al)-suf+ctx, len(al))
	be := min(len(bl)-suf+ctx, len(bl))
	var out strings.Builder
	fmt.Fprintf(&out, "--- %s\n+++ %s\n@@ -%d,%d +%d,%d @@\n", name, name, s+1, ae-s, s+1, be-s)
	write := func(prefix, line string) {
		if line == "" {
			return
		}
		out.WriteString(prefix + strings.TrimSuffix(line, "\n") + "\n")
	}
	for i := s; i < pre; i++ {
		write(" ", al[i])
	}
	for i := pre; i < len(al)-suf; i++ {
		write("-", al[i])
	}
	for i := pre; i < len(bl)-suf; i++ {
		write("+", bl[i])
	}
	for i := len(al) - suf; i < ae; i++ {
		write(" ", al[i])
	}
	d := out.String()
	if len(d) > maxReadOutput {
		d = d[:maxReadOutput] + "\n… [diff truncated]\n"
	}
	return d
}

// ---- list_dir

type listIn struct {
	Name string `json:"name" jsonschema:"environment name"`
	Path string `json:"path,omitempty" jsonschema:"directory; relative paths are relative to /home/dev (default /home/dev)"`
}

type dirEntry struct {
	Name     string    `json:"name"`
	Type     string    `json:"type" jsonschema:"file, dir or symlink"`
	Size     int64     `json:"size"`
	Modified time.Time `json:"modified"`
}

type listOut struct {
	Path      string     `json:"path"`
	Entries   []dirEntry `json:"entries"`
	Truncated bool       `json:"truncated"`
}

func (s *Server) listDir(ctx context.Context, _ *mcp.CallToolRequest, in listIn) (*mcp.CallToolResult, listOut, error) {
	if in.Path == "" {
		in.Path = homeDir
	}
	p, err := resolvePath(in.Path)
	if err != nil {
		return nil, listOut{}, err
	}
	out := listOut{Path: p, Entries: []dirEntry{}}
	err = s.withSFTP(ctx, in.Name, func(c *sftp.Client) error {
		infos, err := c.ReadDir(p)
		if err != nil {
			return fsErr(p, err)
		}
		sort.Slice(infos, func(i, j int) bool {
			if infos[i].IsDir() != infos[j].IsDir() {
				return infos[i].IsDir()
			}
			return infos[i].Name() < infos[j].Name()
		})
		for _, fi := range infos {
			if len(out.Entries) >= maxListEntries {
				out.Truncated = true
				break
			}
			t := "file"
			switch {
			case fi.Mode()&os.ModeSymlink != 0:
				t = "symlink"
			case fi.IsDir():
				t = "dir"
			}
			out.Entries = append(out.Entries, dirEntry{Name: fi.Name(), Type: t, Size: fi.Size(), Modified: fi.ModTime()})
		}
		return nil
	})
	if err != nil {
		return nil, listOut{}, err
	}
	return nil, out, nil
}

func (s *Server) registerFiles(srv *mcp.Server) {
	ro := &mcp.ToolAnnotations{ReadOnlyHint: true}
	mcp.AddTool(srv, &mcp.Tool{
		Name: "read_file",
		Description: "Read a file from an environment. Text files return lines prefixed with their line number and a tab (the prefix is not part of the file); " +
			"up to 2000 lines by default, use offset and limit for large files. Images (png, jpeg, gif, webp) are returned as images you can see. " +
			"Use encoding base64 for other binary files.",
		Annotations: ro,
	}, logged("read_file", s.readFile))
	mcp.AddTool(srv, &mcp.Tool{
		Name: "write_file",
		Description: "Create or completely replace a file in an environment. Parent directories are created. Prefer edit_file for changes to existing files. " +
			"Use encoding base64 to write binary data.",
	}, logged("write_file", s.writeFile))
	mcp.AddTool(srv, &mcp.Tool{
		Name: "edit_file",
		Description: "Edit a file by exact text replacement. Each old_text must match exactly once in the original file (copy it from read_file without the line number prefix, " +
			"including indentation). Several non-overlapping edits can be applied at once. Returns a diff.",
	}, logged("edit_file", s.editFile))
	mcp.AddTool(srv, &mcp.Tool{
		Name:        "list_dir",
		Description: "List a directory in an environment (directories first).",
		Annotations: ro,
	}, logged("list_dir", s.listDir))
}
