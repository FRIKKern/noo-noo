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

// destRootPlaceholder stands in for the offload destination in a rendered
// NativeCommand when no destination is configured — so the suggestion reads
// as a template the user completes, never a stale absolute path from another
// machine.
const destRootPlaceholder = "<your-external-volume>"

// destRootToken is the substitution point inside a NativeCommand template.
// It is replaced at read time (scan) with the configured [offload] dest_root,
// or with destRootPlaceholder when unconfigured. Keeping playbooks templated
// (not baked at construction) is what makes them machine-agnostic.
const destRootToken = "{dest_root}"

// GateKind selects how Apply proves an asset is safe to relocate.
type GateKind string

const (
	// GateProcess (the zero value) gates on StopGate via pgrep-by-name: the
	// owning app must not be running anywhere. Right when one named process
	// owns the whole asset (colima, Claude).
	GateProcess GateKind = ""
	// GatePath gates on an lsof liveness probe of the target path itself: no
	// process may hold any file open under it. Right for a cold SUB-asset of
	// a live parent — e.g. ~/.claude/jobs is safely relocatable even while
	// the ~/.claude config dir is locked by a running Claude, as long as
	// nothing has the jobs subtree open. Fail-safe: doubt = live = blocked.
	GatePath GateKind = "path"
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
	// NativeCommand is the supported command / env change for native-config
	// assets, as a machine-agnostic TEMPLATE: the destRootToken ("{dest_root}")
	// is substituted at scan time with the configured [offload] dest_root (or a
	// generic placeholder when unconfigured), so the suggestion is correct for
	// ANY user's machine rather than baking one host's absolute path.
	NativeCommand string
	// Gate selects the Apply safety gate: GateProcess (default) uses StopGate
	// via pgrep; GatePath probes the target path for open files (for a cold
	// sub-asset whose live parent has no single stop-gate process).
	Gate GateKind
	// StopGate is a pgrep pattern for a process that must NOT be running
	// while the asset is relocated. Empty means no gate. Consulted only when
	// Gate is GateProcess.
	StopGate string
	// ParentAssetID, when set, names the enclosing playbook asset this entry
	// is a sub-asset of (e.g. claude-jobs → claude-config). Cosmetic: it
	// groups related rows in reports; it does not affect plan/apply, which key
	// strictly on exact path.
	ParentAssetID string
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
			NativeCommand: "pnpm config set store-dir " + destRootToken + "/.pnpm-store --global",
			Note:          "store-dir is currently unpinned: home-cwd invocations grow a second internal store while external-cwd ones use the external store",
		},
		{
			AssetID:       "colima",
			Path:          "~/.colima",
			Class:         ClassNativeConfig,
			NativeCommand: "colima stop && mv ~/.colima " + destRootToken + "/.colima && launchctl setenv COLIMA_HOME " + destRootToken + "/.colima (persist COLIMA_HOME in your shell profile)",
			StopGate:      "colima",
			Note:          "12GiB Docker VM disk; COLIMA_HOME is the supported relocation; the VM must be stopped first",
		},
		{
			AssetID:       "claude-config",
			Path:          "~/.claude",
			Class:         ClassNativeConfig,
			NativeCommand: "mv ~/.claude " + destRootToken + "/.claude && launchctl setenv CLAUDE_CONFIG_DIR " + destRootToken + "/.claude (persist CLAUDE_CONFIG_DIR in your shell profile)",
			Note:          "CLAUDE_CONFIG_DIR is honored by the installed binary",
		},
		{
			// Sub-asset of claude-config: the parent ~/.claude is native-config
			// and stays locked while Claude runs, but its cold job scratch
			// relocates cleanly once nothing holds it open. Path-gated (lsof on
			// the jobs subtree), NOT process-gated — the live 2026-07-15 win
			// (~/.claude 8.3G → 4.3G by relocating jobs alone).
			AssetID:       "claude-jobs",
			Path:          "~/.claude/jobs",
			Class:         ClassRelocate,
			Gate:          GatePath,
			ParentAssetID: "claude-config",
			Note:          "cold job scratch under the live ~/.claude config dir; relocatable while Claude runs as long as the jobs subtree is lsof-clean (path-gated, not process-gated)",
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

// pathGated reports whether Apply must gate this asset on an lsof probe of
// its own path (GatePath) rather than on a named process (GateProcess).
func (pb Playbook) pathGated() bool { return pb.Gate == GatePath }

// renderNativeCommand substitutes the destRootToken in a NativeCommand
// template with the configured offload destination root. An unconfigured
// dest_root (empty string) renders the generic placeholder so the suggestion
// reads as a fill-in-the-blank template; the scan verdict separately carries
// the "configure [offload] dest_root" guidance. An empty command renders
// empty.
func renderNativeCommand(cmd, destRoot string) string {
	if cmd == "" {
		return ""
	}
	root := destRoot
	if root == "" {
		root = destRootPlaceholder
	}
	return strings.ReplaceAll(cmd, destRootToken, root)
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
