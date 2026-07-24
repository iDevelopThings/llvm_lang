package main

import (
	"bufio"
	"bytes"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestRun_WatchFlagRules(t *testing.T) {
	hello := "../../examples/hello/hello.llx"
	cases := []struct {
		name string
		args []string
		want string
	}{
		{
			name: "-watch with -o",
			args: []string{"-watch", "-o", filepath.Join(t.TempDir(), "out.exe"), hello},
			want: "-watch cannot be used with -emit-llvm or -o",
		},
		{
			name: "-watch with -emit-llvm",
			args: []string{"-watch", "-emit-llvm", hello},
			want: "-watch cannot be used with -emit-llvm or -o",
		},
		{
			name: "-init without -watch",
			args: []string{"-init", "Setup", hello},
			want: "-init and -tick require -watch",
		},
		{
			name: "-tick without -watch",
			args: []string{"-tick", "Step", hello},
			want: "-init and -tick require -watch",
		},
		{
			name: "empty -tick",
			args: []string{"-watch", "-tick", "", hello},
			want: "-tick cannot be empty",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var stderr bytes.Buffer
			code := run(tc.args, &stderr)
			if code != exitUsage {
				t.Errorf("exit code = %d, want %d, stderr:\n%s", code, exitUsage, stderr.String())
			}
			if !strings.Contains(stderr.String(), tc.want) {
				t.Errorf("stderr = %q, want substring %q", stderr.String(), tc.want)
			}
		})
	}
}

func TestRun_Watch_MissingTick(t *testing.T) {
	dir := t.TempDir()
	srcPath := filepath.Join(dir, "main.llx")
	src := "func Init() {\n}\n"
	if err := os.WriteFile(srcPath, []byte(src), 0o644); err != nil {
		t.Fatalf("writing source: %v", err)
	}
	var stderr bytes.Buffer
	code := run([]string{"-watch", srcPath}, &stderr)
	if code != exitCompile {
		t.Errorf("exit code = %d, want %d, stderr:\n%s", code, exitCompile, stderr.String())
	}
	if !strings.Contains(stderr.String(), "Frame") {
		t.Errorf("stderr = %q, want missing Frame diagnostic", stderr.String())
	}
}

// TestBinary_Watch_TickExit runs -watch until Frame returns non-zero.
func TestBinary_Watch_TickExit(t *testing.T) {
	dir := t.TempDir()
	srcPath := filepath.Join(dir, "main.llx")
	src := "" +
		"var n int = 0\n" +
		"func Init() {\n" +
		"\tprint(\"init\")\n" +
		"}\n" +
		"func Frame() int {\n" +
		"\tn = n + 1\n" +
		"\tprint(\"frame\")\n" +
		"\tif n >= 3 {\n" +
		"\t\treturn 42\n" +
		"\t}\n" +
		"\treturn 0\n" +
		"}\n"
	if err := os.WriteFile(srcPath, []byte(src), 0o644); err != nil {
		t.Fatalf("writing source: %v", err)
	}

	cmd := exec.Command(llvmcPath, "-watch", srcPath)
	out, err := cmd.CombinedOutput()
	if err == nil {
		t.Fatalf("llvmc exited 0, want 42; output:\n%s", out)
	}
	ee, ok := err.(*exec.ExitError)
	if !ok {
		t.Fatalf("running llvmc: %v\n%s", err, out)
	}
	if ee.ExitCode() != 42 {
		t.Fatalf("llvmc exit code = %d, want 42\noutput:\n%s", ee.ExitCode(), out)
	}
	got := string(out)
	if c := strings.Count(got, "init"); c != 1 {
		t.Errorf("stdout init count = %d, want 1; got:\n%s", c, got)
	}
	if c := strings.Count(got, "frame"); c != 3 {
		t.Errorf("stdout frame count = %d, want 3; got:\n%s", c, got)
	}
}

// TestBinary_Watch_Reload swaps in a new Frame that exits with a different code.
func TestBinary_Watch_Reload(t *testing.T) {
	dir := t.TempDir()
	srcPath := filepath.Join(dir, "main.llx")
	// Frame-A prints once then spins quietly so the stdout pipe cannot fill
	// and block the watch process before the test rewrites the file.
	v1 := "" +
		"var seen int = 0\n" +
		"func Init() {\n" +
		"\tprint(\"init-A\")\n" +
		"}\n" +
		"func Frame() int {\n" +
		"\tif seen == 0 {\n" +
		"\t\tprint(\"frame-A\")\n" +
		"\t\tseen = 1\n" +
		"\t}\n" +
		"\treturn 0\n" +
		"}\n"
	v2 := "" +
		"func Init() {\n" +
		"\tprint(\"init-B\")\n" +
		"}\n" +
		"func Frame() int {\n" +
		"\tprint(\"frame-B\")\n" +
		"\treturn 9\n" +
		"}\n"
	if err := os.WriteFile(srcPath, []byte(v1), 0o644); err != nil {
		t.Fatalf("writing source: %v", err)
	}

	cmd := exec.Command(llvmcPath, "-watch", srcPath)
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		t.Fatalf("StdoutPipe: %v", err)
	}
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	if err := cmd.Start(); err != nil {
		t.Fatalf("Start: %v", err)
	}

	lines := make(chan string, 64)
	go func() {
		sc := bufio.NewScanner(stdout)
		for sc.Scan() {
			lines <- sc.Text()
		}
		close(lines)
	}()

	waitLine := func(substr string, timeout time.Duration) {
		t.Helper()
		deadline := time.After(timeout)
		for {
			select {
			case <-deadline:
				_ = cmd.Process.Kill()
				t.Fatalf("timed out waiting for %q; stderr:\n%s", substr, stderr.String())
			case line, ok := <-lines:
				if !ok {
					_ = cmd.Process.Kill()
					t.Fatalf("stdout closed before %q; stderr:\n%s", substr, stderr.String())
				}
				if strings.Contains(line, substr) {
					return
				}
			}
		}
	}

	waitLine("init-A", 10*time.Second)
	waitLine("frame-A", 10*time.Second)

	// Ensure mtime advances on coarse filesystems.
	time.Sleep(20 * time.Millisecond)
	if err := os.WriteFile(srcPath, []byte(v2), 0o644); err != nil {
		_ = cmd.Process.Kill()
		t.Fatalf("rewriting source: %v", err)
	}

	waitLine("init-B", 10*time.Second)
	waitLine("frame-B", 10*time.Second)

	done := make(chan error, 1)
	go func() { done <- cmd.Wait() }()
	select {
	case err := <-done:
		if err == nil {
			t.Fatal("llvmc exited 0, want 9")
		}
		ee, ok := err.(*exec.ExitError)
		if !ok {
			t.Fatalf("Wait: %v; stderr:\n%s", err, stderr.String())
		}
		if ee.ExitCode() != 9 {
			t.Fatalf("exit code = %d, want 9; stderr:\n%s", ee.ExitCode(), stderr.String())
		}
	case <-time.After(10 * time.Second):
		_ = cmd.Process.Kill()
		t.Fatal("timed out waiting for watch process exit")
	}
}

// TestBinary_Watch_LastGoodOnError keeps ticking the previous module when a
// reload compile fails, then picks up a later good edit.
func TestBinary_Watch_LastGoodOnError(t *testing.T) {
	dir := t.TempDir()
	srcPath := filepath.Join(dir, "main.llx")
	good := "" +
		"var seen int = 0\n" +
		"func Init() {\n" +
		"\tprint(\"init-good\")\n" +
		"}\n" +
		"func Frame() int {\n" +
		"\tif seen == 0 {\n" +
		"\t\tprint(\"frame-good\")\n" +
		"\t\tseen = 1\n" +
		"\t}\n" +
		"\treturn 0\n" +
		"}\n"
	bad := "func Frame() int {\n\tthis is not valid\n}\n"
	final := "" +
		"func Init() {\n" +
		"\tprint(\"init-final\")\n" +
		"}\n" +
		"func Frame() int {\n" +
		"\tprint(\"frame-final\")\n" +
		"\treturn 5\n" +
		"}\n"
	if err := os.WriteFile(srcPath, []byte(good), 0o644); err != nil {
		t.Fatalf("writing source: %v", err)
	}

	cmd := exec.Command(llvmcPath, "-watch", srcPath)
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		t.Fatalf("StdoutPipe: %v", err)
	}
	stderrPipe, err := cmd.StderrPipe()
	if err != nil {
		t.Fatalf("StderrPipe: %v", err)
	}
	if err := cmd.Start(); err != nil {
		t.Fatalf("Start: %v", err)
	}

	outLines := make(chan string, 128)
	errLines := make(chan string, 128)
	go scanLines(stdout, outLines)
	go scanLines(stderrPipe, errLines)

	waitOut := func(substr string) {
		t.Helper()
		waitChan(t, outLines, substr, 10*time.Second, cmd)
	}
	waitErr := func(substr string) {
		t.Helper()
		waitChan(t, errLines, substr, 10*time.Second, cmd)
	}

	waitOut("init-good")
	waitOut("frame-good")

	time.Sleep(20 * time.Millisecond)
	if err := os.WriteFile(srcPath, []byte(bad), 0o644); err != nil {
		_ = cmd.Process.Kill()
		t.Fatalf("writing bad source: %v", err)
	}
	waitErr("keeping last good module")

	time.Sleep(20 * time.Millisecond)
	if err := os.WriteFile(srcPath, []byte(final), 0o644); err != nil {
		_ = cmd.Process.Kill()
		t.Fatalf("writing final source: %v", err)
	}
	waitOut("init-final")
	waitOut("frame-final")

	done := make(chan error, 1)
	go func() { done <- cmd.Wait() }()
	select {
	case err := <-done:
		if err == nil {
			t.Fatal("llvmc exited 0, want 5")
		}
		ee, ok := err.(*exec.ExitError)
		if !ok {
			t.Fatalf("Wait: %v", err)
		}
		if ee.ExitCode() != 5 {
			t.Fatalf("exit code = %d, want 5", ee.ExitCode())
		}
	case <-time.After(10 * time.Second):
		_ = cmd.Process.Kill()
		t.Fatal("timed out waiting for watch process exit")
	}
}

func scanLines(r io.Reader, ch chan<- string) {
	sc := bufio.NewScanner(r)
	for sc.Scan() {
		ch <- sc.Text()
	}
	close(ch)
}

func waitChan(t *testing.T, ch <-chan string, substr string, timeout time.Duration, cmd *exec.Cmd) {
	t.Helper()
	deadline := time.After(timeout)
	for {
		select {
		case <-deadline:
			_ = cmd.Process.Kill()
			t.Fatalf("timed out waiting for %q", substr)
		case line, ok := <-ch:
			if !ok {
				_ = cmd.Process.Kill()
				t.Fatalf("stream closed before %q", substr)
			}
			if strings.Contains(line, substr) {
				return
			}
		}
	}
}
