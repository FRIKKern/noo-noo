package startup

import (
	"context"
	"fmt"
	"os/exec"
)

// Runner abstracts subprocess execution so tests can inject a fake.
type Runner interface {
	// Run executes name with args, optionally via sudo. It returns nil on
	// exit code 0, otherwise an error with combined stdout+stderr.
	Run(ctx context.Context, sudo bool, name string, args ...string) error
}

// ExecRunner runs commands via os/exec. Production default.
type ExecRunner struct{}

func (ExecRunner) Run(ctx context.Context, sudo bool, name string, args ...string) error {
	full := append([]string{name}, args...)
	if sudo {
		full = append([]string{"sudo", "-n"}, full...)
	}
	cmd := exec.CommandContext(ctx, full[0], full[1:]...)
	out, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("%s: %w (output: %s)", full[0], err, string(out))
	}
	return nil
}

// Call records a single Run invocation for assertions in tests.
type Call struct {
	Sudo bool
	Args []string // [name, args...]
}

// FakeRunner records calls without executing anything. Optionally returns
// Err for the next call.
type FakeRunner struct {
	Calls []Call
	Err   error
}

func (f *FakeRunner) Run(_ context.Context, sudo bool, name string, args ...string) error {
	f.Calls = append(f.Calls, Call{Sudo: sudo, Args: append([]string{name}, args...)})
	return f.Err
}
