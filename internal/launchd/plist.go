// Package launchd generates and installs launchd LaunchAgent plists for
// noo-nood. Pure-Go templating; uninstall via `launchctl bootout`.
package launchd

import (
	"bytes"
	"fmt"
	"os"
	"path/filepath"
	"text/template"
)

const plistTmpl = `<?xml version="1.0" encoding="UTF-8"?>
<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd">
<plist version="1.0">
<dict>
  <key>Label</key>
  <string>{{.Label}}</string>
  <key>ProgramArguments</key>
  <array>
    <string>{{.ProgramPath}}</string>{{range .Args}}
    <string>{{.}}</string>{{end}}
  </array>
  <key>RunAtLoad</key>
  <{{.RunAtLoad}}/>
  <key>KeepAlive</key>
  <{{.KeepAlive}}/>
  <key>ProcessType</key>
  <string>Background</string>
  <key>LowPriorityBackgroundIO</key>
  <true/>
  <key>Nice</key>
  <integer>15</integer>
  <key>StandardOutPath</key>
  <string>{{.OutLog}}</string>
  <key>StandardErrorPath</key>
  <string>{{.ErrLog}}</string>
</dict>
</plist>
`

// Log file names inside the log directory. The cask's zap stanza and the
// audit log already live under ~/Library/Logs/noo-noo; the daemon's stdout
// and stderr belong next to them, not in /tmp.
const (
	OutLogName = "noo-nood.log"
	ErrLogName = "noo-nood.err.log"
)

// DefaultLogDir is ~/Library/Logs/noo-noo, falling back to /tmp/noo-noo
// only when the home directory is unknowable.
func DefaultLogDir() string {
	home, err := os.UserHomeDir()
	if err != nil || home == "" {
		return filepath.Join(os.TempDir(), "noo-noo")
	}
	return filepath.Join(home, "Library", "Logs", "noo-noo")
}

type plistData struct {
	Label       string
	ProgramPath string
	Args        []string
	RunAtLoad   string
	KeepAlive   string
	OutLog      string
	ErrLog      string
}

func boolTag(b bool) string {
	if b {
		return "true"
	}
	return "false"
}

// GeneratePlist returns the rendered LaunchAgent plist bytes for the given
// inputs, with stdout/stderr routed to OutLogName/ErrLogName inside logDir.
// Output is byte-stable across runs (deterministic template).
func GeneratePlist(label, programPath string, args []string, runAtLoad, keepAlive bool, logDir string) ([]byte, error) {
	t, err := template.New("plist").Parse(plistTmpl)
	if err != nil {
		return nil, fmt.Errorf("parse template: %w", err)
	}
	var buf bytes.Buffer
	err = t.Execute(&buf, plistData{
		Label:       label,
		ProgramPath: programPath,
		Args:        args,
		RunAtLoad:   boolTag(runAtLoad),
		KeepAlive:   boolTag(keepAlive),
		OutLog:      filepath.Join(logDir, OutLogName),
		ErrLog:      filepath.Join(logDir, ErrLogName),
	})
	if err != nil {
		return nil, fmt.Errorf("execute template: %w", err)
	}
	return buf.Bytes(), nil
}
