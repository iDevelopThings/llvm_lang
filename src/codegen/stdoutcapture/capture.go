// Package stdoutcapture redirects the real C-runtime `stdout` (the same one
// a JIT-compiled `printf` call writes through) to a file for the duration of
// a callback, then hands back everything written.
//
// This exists as its own, non-test package specifically because of a Go
// toolchain restriction discovered while building it: `go test` flatly
// rejects `import "C"` inside any `_test.go` file ("use of cgo in test ...
// not supported" - cmd/go/internal/modindex/read.go rejects it outright,
// regardless of module/build settings). Cgo works completely normally in an
// ordinary source file, so the actual cgo/freopen logic lives here instead,
// and codegen's own test file just imports this package like any other -
// no cgo in the test file itself.
//
// See AGENTS.md's codegen section and BLOCKERS.md for the problem this
// solves: capturing what a JIT-compiled `print` call actually writes to real
// stdout, which Go's own os.Stdout variable can't see (a JIT-compiled printf
// call writes through the real C runtime's stdio, entirely separate from
// Go's os.Stdout - redirecting *that* only works by operating on the C
// runtime's own `stdout` FILE* directly, which is exactly what freopen does
// here). This works because this project's own binaries are built with cgo
// (mandatory for go-llvm) through the exact same mingw64 toolchain/CRT the
// JIT-resolved "printf" symbol resolves against - freopen'ing *this*
// package's own `stdout` FILE* really does redirect what the JIT-executed
// call writes to.
package stdoutcapture

/*
#include <stdio.h>
#include <io.h>
#include <stdlib.h>

// llx_saved_stdout_fd holds a duplicate of the real stdout file descriptor
// while a capture is in progress, so End can restore it. Not reentrant/
// thread-safe (a single global, no locking) - fine for this test-only
// helper, since callers never run two captures concurrently.
static int llx_saved_stdout_fd = -1;

static int llx_begin_capture(const char *path) {
	fflush(stdout);
	llx_saved_stdout_fd = _dup(_fileno(stdout));
	if (llx_saved_stdout_fd == -1) {
		return -1;
	}
	// "wb" (binary), not "w" - avoids the CRT's text-mode '\n' -> "\r\n"
	// translation, so a byte-for-byte comparison against the language's own
	// literal "\n" print output isn't corrupted by it.
	if (freopen(path, "wb", stdout) == NULL) {
		return -1;
	}
	return 0;
}

static void llx_end_capture(void) {
	fflush(stdout);
	if (llx_saved_stdout_fd != -1) {
		_dup2(llx_saved_stdout_fd, _fileno(stdout));
		_close(llx_saved_stdout_fd);
		llx_saved_stdout_fd = -1;
	}
}
*/
import "C"

import (
	"fmt"
	"os"
	"path/filepath"
	"unsafe"
)

// Capture redirects real stdout to a temp file for the duration of fn, then
// restores it and returns everything written.
func Capture(fn func()) ([]byte, error) {
	path := filepath.Join(os.TempDir(), fmt.Sprintf("llx_stdout_capture_%d.txt", os.Getpid()))
	cpath := C.CString(path)
	defer C.free(unsafe.Pointer(cpath))
	defer os.Remove(path)

	if C.llx_begin_capture(cpath) != 0 {
		return nil, fmt.Errorf("stdoutcapture: failed to redirect stdout to %q", path)
	}
	fn()
	C.llx_end_capture()

	return os.ReadFile(path)
}
