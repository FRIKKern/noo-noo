package cli

import (
	"bufio"
	"fmt"
	"io"
	"strings"
)

// Confirm prompts on out and reads a y/n line from in. Returns true on
// "y" or "Y" (case-insensitive). If skip is true, returns true without
// prompting.
func Confirm(in io.Reader, out io.Writer, prompt string, skip bool) bool {
	if skip {
		_, _ = fmt.Fprintln(out, "(--yes specified, skipping confirmation)")
		return true
	}
	_, _ = fmt.Fprintf(out, "%s [y/N] ", prompt)
	scanner := bufio.NewScanner(in)
	if !scanner.Scan() {
		return false
	}
	answer := strings.ToLower(strings.TrimSpace(scanner.Text()))
	return answer == "y" || answer == "yes"
}
