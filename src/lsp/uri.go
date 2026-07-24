package lsp

import (
	"fmt"
	"net/url"
	"path/filepath"
	"runtime"
	"strings"
)

// PathFromURI converts an LSP `file://` document URI (as sent by
// textDocument/didOpen etc.) to a plain OS filesystem path - the identity
// this package's Workspace keys everything by (docs/analysis maps, afero
// paths). Exported so cmd/llvmc-lsp's own handlers can convert an incoming
// request's URI before calling into Workspace.
//
// The drive-letter handling below is gated on runtime.GOOS rather than
// sniffing the URI's own path shape (e.g. "does this look like it has a
// colon where a drive letter would be") - this compiler's own actual
// filesystem convention (what afero.Fs/filepath expect) is what decides
// whether a leading slash needs stripping, not something inferable from the
// URI text alone; a real Unix path can itself contain a literal `:` in a
// directory/file name, which a shape-based guess could misfire on.
func PathFromURI(uri string) (string, error) {
	u, err := url.Parse(uri)
	if err != nil {
		return "", fmt.Errorf("lsp: invalid URI %q: %w", uri, err)
	}
	if u.Scheme != "file" {
		return "", fmt.Errorf("lsp: unsupported URI scheme %q (only file:// is supported)", u.Scheme)
	}

	p := u.Path
	if runtime.GOOS == "windows" {
		// A Windows path (file:///C:/foo/bar.llx) always parses with a
		// leading slash before the drive letter (u.Path ==
		// "/C:/foo/bar.llx") - strip it so filepath.FromSlash produces a
		// real, directly usable "C:\foo\bar.llx" rather than "\C:\...".
		p = strings.TrimPrefix(p, "/")
	}
	return filepath.FromSlash(p), nil
}

// URIFromPath is PathFromURI's inverse - used wherever this package (or
// cmd/llvmc-lsp) hands a file location back to the client: go-to-
// definition's own target file, or a publishDiagnostics notification's URI.
// See PathFromURI's own doc comment for why this is gated on runtime.GOOS.
func URIFromPath(path string) string {
	p := filepath.ToSlash(path)
	if runtime.GOOS == "windows" && !strings.HasPrefix(p, "/") {
		p = "/" + p
	}
	u := url.URL{
		Scheme: "file",
		Path:   p,
	}
	return u.String()
}
