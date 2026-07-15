// Package offload relocates bulky, well-understood assets from the internal
// disk to a configured external volume. Knowledge about each asset lives in
// a Playbook — data, not code — carrying per-asset safety facts: whether the
// app has a native relocation mechanism (preferred over any file move),
// which process must be stopped first, and whether the asset is
// irreplaceable and therefore must NEVER be deleted by this module.
package offload

import (
	"path/filepath"
	"strings"
)

// Class describes how an asset may be moved off the internal disk.
type Class string

const (
	// ClassNativeConfig: the owning app has a supported config/env knob for
	// relocating the asset. noo-noo only SUGGESTS the exact command; it
	// never moves these files itself (the app would re-create the store at
	// the old path and double the footprint).
	ClassNativeConfig Class = "native-config"
	// ClassRelocate: safe to move+symlink, gated by the volume guard and
	// (when set) a stop-gate on the owning process.
	ClassRelocate Class = "relocate"
	// ClassManual: symlink tolerance is unverified; v1 refuses to
	// auto-relocate and explains what a human must verify first.
	ClassManual Class = "manual"
)

// Playbook is the per-asset knowledge record.
type Playbook struct {
	// AssetID names the asset; it becomes the directory name under the
	// offload destination root for relocate-class assets.
	AssetID string
	// Path is the asset location, "~/"-relative to the user home. Empty for
	// generic (class manual) entries that describe a family of assets.
	Path string
	// Class picks the relocation mechanism.
	Class Class
	// NativeCommand is the exact supported command / env change for
	// native-config assets. Surfaced verbatim in scan Evidence.
	NativeCommand string
	// StopGate is a pgrep pattern for a process that must NOT be running
	// while the asset is relocated. Empty means no gate.
	StopGate string
	// NeverDelete marks irreplaceable data (e.g. a user's only LocalWP
	// blueprint). The module is structurally incapable of emitting a
	// delete action, and Apply refuses "delete" outright; this flag exists
	// so scans surface WHY and so future code keeps the law visible.
	NeverDelete bool
	// Note explains class-manual refusals and any extra safety context.
	Note string
}

// DefaultPlaybooks returns the built-in asset knowledge, verdicts
// live-verified on the reference machine (wave 2026-07-15).
func DefaultPlaybooks() []Playbook {
	return []Playbook{
		{
			AssetID:       "pnpm-store",
			Path:          "~/Library/pnpm",
			Class:         ClassNativeConfig,
			NativeCommand: "pnpm config set store-dir /Volumes/SATECHI/.pnpm-store --global",
			Note:          "store-dir is currently unpinned: home-cwd invocations grow a second internal store while external-cwd ones use the external store",
		},
		{
			AssetID:       "colima",
			Path:          "~/.colima",
			Class:         ClassNativeConfig,
			NativeCommand: "colima stop && mv ~/.colima /Volumes/SATECHI/.colima && launchctl setenv COLIMA_HOME /Volumes/SATECHI/.colima (persist COLIMA_HOME in your shell profile)",
			StopGate:      "colima",
			Note:          "12GiB Docker VM disk; COLIMA_HOME is the supported relocation; the VM must be stopped first",
		},
		{
			AssetID:       "claude-config",
			Path:          "~/.claude",
			Class:         ClassNativeConfig,
			NativeCommand: "mv ~/.claude /Volumes/SATECHI/.claude && launchctl setenv CLAUDE_CONFIG_DIR /Volumes/SATECHI/.claude (persist CLAUDE_CONFIG_DIR in your shell profile)",
			Note:          "CLAUDE_CONFIG_DIR is honored by the installed binary",
		},
		{
			AssetID:     "localwp-blueprints",
			Path:        "~/Library/Application Support/Local/blueprints",
			Class:       ClassRelocate,
			NeverDelete: true,
			Note:        "contains irreplaceable user blueprints; official LocalWP docs: deletion is permanent — relocate only, never delete",
		},
		{
			AssetID:  "claude-vm-bundles",
			Path:     "~/Library/Application Support/Claude/vm_bundles",
			Class:    ClassRelocate,
			StopGate: "Claude",
			Note:     "VM disk bundles; the Claude app must not be running during the move",
		},
		{
			AssetID: "electron-userdata",
			Path:    "",
			Class:   ClassManual,
			Note:    "generic Electron userData dirs: symlink tolerance is UNVERIFIED per app — auto-relocation refused; move one app manually, then launch it and smoke-test before trusting the symlink",
		},
	}
}

// expandPath resolves a "~/"-relative playbook path against home.
func expandPath(home, p string) string {
	if p == "" {
		return ""
	}
	if p == "~" {
		return home
	}
	if strings.HasPrefix(p, "~/") {
		return filepath.Join(home, strings.TrimPrefix(p, "~/"))
	}
	return p
}
