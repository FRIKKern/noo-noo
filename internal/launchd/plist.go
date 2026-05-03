// Package launchd generates and installs launchd LaunchAgent plists for
// noo-nood. Pure-Go templating; uninstall via `launchctl bootout`.
package launchd

import (
	"bytes"
	"fmt"
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
  <key>StandardOutPath</key>
  <string>/tmp/noo-nood.out.log</string>
  <key>StandardErrorPath</key>
  <string>/tmp/noo-nood.err.log</string>
</dict>
</plist>
`

type plistData struct {
	Label       string
	ProgramPath string
	Args        []string
	RunAtLoad   string
	KeepAlive   string
}

func boolTag(b bool) string {
	if b {
		return "true"
	}
	return "false"
}

// GeneratePlist returns the rendered LaunchAgent plist bytes for the given
// inputs. Output is byte-stable across runs (deterministic template).
func GeneratePlist(label, programPath string, args []string, runAtLoad, keepAlive bool) ([]byte, error) {
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
	})
	if err != nil {
		return nil, fmt.Errorf("execute template: %w", err)
	}
	return buf.Bytes(), nil
}
