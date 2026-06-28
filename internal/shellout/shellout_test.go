package shellout

import (
	"errors"
	"strings"
	"testing"
)

func TestOutputSuccess(t *testing.T) {
	out, err := Output("printf", "hello")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if string(out) != "hello" {
		t.Errorf("got %q, want %q", out, "hello")
	}
}

func TestOutputFoldsStderrIntoError(t *testing.T) {
	// `sh -c` lets us write to stderr and exit non-zero deterministically.
	_, err := Output("sh", "-c", "echo boom 1>&2; exit 3")
	if err == nil {
		t.Fatal("expected error")
	}
	if !strings.Contains(err.Error(), "boom") {
		t.Errorf("error message missing stderr: %q", err.Error())
	}
	var se *Error
	if !errors.As(err, &se) {
		t.Fatalf("expected *Error, got %T", err)
	}
	if se.Stderr != "boom" {
		t.Errorf("Stderr = %q, want %q", se.Stderr, "boom")
	}
}

func TestJSONDecodes(t *testing.T) {
	var v struct {
		Name string `json:"name"`
	}
	if err := JSON(&v, "printf", `{"name":"rhodium"}`); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if v.Name != "rhodium" {
		t.Errorf("got %q, want %q", v.Name, "rhodium")
	}
}

func TestCombinedMergesStreams(t *testing.T) {
	out, err := Combined("sh", "-c", "echo out; echo err 1>&2")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !strings.Contains(out, "out") || !strings.Contains(out, "err") {
		t.Errorf("combined output missing a stream: %q", out)
	}
}

func TestSpecDirSetsWorkingDirectory(t *testing.T) {
	dir := t.TempDir()
	out, err := Spec{Dir: dir}.Output("pwd")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	// macOS /tmp is a symlink to /private/tmp, so compare suffixes.
	if !strings.Contains(strings.TrimSpace(string(out)), strings.TrimPrefix(dir, "/private")) {
		t.Errorf("pwd = %q, want it to reflect %q", strings.TrimSpace(string(out)), dir)
	}
}
