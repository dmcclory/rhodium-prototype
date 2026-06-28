// Package shellout centralizes the "run an external command and fold its
// stderr into the returned error" pattern that was previously duplicated
// across the gh, rhodium, and cli packages. Every helper captures the
// command's diagnostic output (stderr, or combined output for Combined) and
// surfaces it through *Error so callers get useful failure messages without
// each re-implementing the buffer dance.
package shellout

import (
	"bytes"
	"encoding/json"
	"os/exec"
	"strings"
)

// Error wraps a failed command, carrying the captured diagnostic output so it
// shows up in the error message. The underlying exec error (e.g.
// *exec.ExitError) is available via errors.Unwrap; Stderr is exposed so
// callers that need to branch on a specific message (e.g. "HTTP 404") can
// inspect it via errors.As.
type Error struct {
	Stderr string // trimmed stderr (Output/Run) or combined output (Combined)
	Err    error  // underlying error from exec
}

func (e *Error) Error() string {
	if e.Stderr == "" {
		return e.Err.Error()
	}
	return e.Err.Error() + ": " + e.Stderr
}

func (e *Error) Unwrap() error { return e.Err }

// Spec carries the optional knobs (currently just a working directory) for
// the handful of call sites that need them. The zero value behaves exactly
// like the package-level helpers.
type Spec struct {
	Dir string
}

// Output runs name+args and returns stdout. On failure the error is an
// *Error whose message includes the command's stderr. stdout collected before
// the failure is still returned so callers can inspect partial output.
func Output(name string, args ...string) ([]byte, error) {
	return Spec{}.Output(name, args...)
}

// Combined runs name+args and returns combined stdout+stderr. On failure the
// error is an *Error whose message includes that combined output.
func Combined(name string, args ...string) (string, error) {
	return Spec{}.Combined(name, args...)
}

// Run runs name+args, discarding stdout. On failure the error is an *Error
// whose message includes the command's stderr.
func Run(name string, args ...string) error {
	return Spec{}.Run(name, args...)
}

// JSON runs name+args and unmarshals stdout into dst.
func JSON(dst any, name string, args ...string) error {
	out, err := Output(name, args...)
	if err != nil {
		return err
	}
	return json.Unmarshal(out, dst)
}

func (s Spec) Output(name string, args ...string) ([]byte, error) {
	cmd := exec.Command(name, args...)
	cmd.Dir = s.Dir
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		return stdout.Bytes(), &Error{Stderr: strings.TrimSpace(stderr.String()), Err: err}
	}
	return stdout.Bytes(), nil
}

func (s Spec) Combined(name string, args ...string) (string, error) {
	cmd := exec.Command(name, args...)
	cmd.Dir = s.Dir
	out, err := cmd.CombinedOutput()
	if err != nil {
		return string(out), &Error{Stderr: strings.TrimSpace(string(out)), Err: err}
	}
	return string(out), nil
}

func (s Spec) Run(name string, args ...string) error {
	cmd := exec.Command(name, args...)
	cmd.Dir = s.Dir
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		return &Error{Stderr: strings.TrimSpace(stderr.String()), Err: err}
	}
	return nil
}
