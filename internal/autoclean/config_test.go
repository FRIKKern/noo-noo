package autoclean

import (
	"testing"

	"github.com/FRIKKern/noo-noo/internal/heuristics"
)

// goodCfg returns a fully-opted-in config with the production defaults.
// Tests mutate one field at a time to exercise individual gates.
func goodCfg() Config {
	return Config{
		Enabled:            true,
		ModulesAllowed:     []string{"dev"},
		MinIdleDays:        90,
		MinSizeMB:          1024,
		SizeCapPerTickGB:   10,
		RiskAcknowledgedAt: "2026-05-01T00:00:00Z",
	}
}

// goodSugg returns a Suggestion that comfortably clears every gate.
// Size: 1.5 GiB; idle: 184 days; module: dev.
func goodSugg() heuristics.Suggestion {
	return heuristics.Suggestion{
		ID:     42,
		Module: "dev",
		Target: "/tmp/old/node_modules",
		Evidence: map[string]any{
			"size_bytes": int64(1500) * 1024 * 1024,
			"idle_days":  184,
		},
	}
}

func TestEvaluateAllPass(t *testing.T) {
	a, ok := EvaluateSuggestion(goodSugg(), goodCfg())
	if !ok {
		t.Fatalf("all-pass should qualify; reason=%s", a.SkipReason)
	}
	if a.SuggestionID != 42 {
		t.Errorf("SuggestionID = %d, want 42", a.SuggestionID)
	}
	if a.TargetPath != "/tmp/old/node_modules" {
		t.Errorf("TargetPath = %q, want /tmp/old/node_modules", a.TargetPath)
	}
}

func TestGate1Disabled(t *testing.T) {
	cfg := goodCfg()
	cfg.Enabled = false
	a, ok := EvaluateSuggestion(goodSugg(), cfg)
	if ok || a.SkipReason != "not_enabled" {
		t.Fatalf("want not_enabled, got ok=%v reason=%s", ok, a.SkipReason)
	}
}

func TestGate1aNoAck(t *testing.T) {
	cfg := goodCfg()
	cfg.RiskAcknowledgedAt = ""
	a, ok := EvaluateSuggestion(goodSugg(), cfg)
	if ok || a.SkipReason != "risk_not_acknowledged" {
		t.Fatalf("want risk_not_acknowledged, got ok=%v reason=%s", ok, a.SkipReason)
	}
}

func TestGate2WrongModule(t *testing.T) {
	s := goodSugg()
	s.Module = "caches"
	a, ok := EvaluateSuggestion(s, goodCfg())
	if ok || a.SkipReason != "module_not_allowed" {
		t.Fatalf("want module_not_allowed, got ok=%v reason=%s", ok, a.SkipReason)
	}
}

func TestGate3NotIdle(t *testing.T) {
	s := goodSugg()
	s.Evidence["idle_days"] = 30
	a, ok := EvaluateSuggestion(s, goodCfg())
	if ok || a.SkipReason != "idle_too_short" {
		t.Fatalf("want idle_too_short, got ok=%v reason=%s", ok, a.SkipReason)
	}
}

func TestGate4TooSmall(t *testing.T) {
	s := goodSugg()
	s.Evidence["size_bytes"] = int64(100) * 1024 * 1024 // 100 MiB
	a, ok := EvaluateSuggestion(s, goodCfg())
	if ok || a.SkipReason != "size_too_small" {
		t.Fatalf("want size_too_small, got ok=%v reason=%s", ok, a.SkipReason)
	}
}

// TestGateOrderShortCircuit: gate 1 (Enabled) fires before gate 1a
// (RiskAcknowledgedAt). This matters because the cheap not_enabled
// branch should win over the more expressive risk-not-ack branch.
func TestGateOrderShortCircuit(t *testing.T) {
	cfg := goodCfg()
	cfg.Enabled = false
	cfg.RiskAcknowledgedAt = "" // would also fail 1a
	a, ok := EvaluateSuggestion(goodSugg(), cfg)
	if ok || a.SkipReason != "not_enabled" {
		t.Fatalf("gate 1 must fire first; got ok=%v reason=%s", ok, a.SkipReason)
	}
}

func TestEvidenceFloat64Decoded(t *testing.T) {
	// JSON round-trip turns int64 into float64; the gate must still
	// see "1.5 GiB" and not "0 bytes".
	s := heuristics.Suggestion{
		ID:     1,
		Module: "dev",
		Target: "/x",
		Evidence: map[string]any{
			"size_bytes": float64(1500 * 1024 * 1024),
			"idle_days":  float64(200),
		},
	}
	if _, ok := EvaluateSuggestion(s, goodCfg()); !ok {
		t.Fatal("float64-encoded evidence must decode back to int64")
	}
}

func TestEvidenceMissingFails(t *testing.T) {
	// No evidence at all means size=0, idle=0 — fails gate 3 first
	// (idleness comes before size in the cascade).
	s := heuristics.Suggestion{ID: 1, Module: "dev", Target: "/x"}
	a, ok := EvaluateSuggestion(s, goodCfg())
	if ok || a.SkipReason != "idle_too_short" {
		t.Fatalf("missing evidence should fail idle gate; got ok=%v reason=%s", ok, a.SkipReason)
	}
}
