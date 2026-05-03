package ipc

import (
	"errors"
	"testing"
)

// fakeStats is the tiny test double for AutoCleanStatsStore.
type fakeStats struct {
	count int
	freed int64
	err   error
}

func (f fakeStats) AutoCleanStatsSince(_ int64) (int, int64, error) {
	return f.count, f.freed, f.err
}

func TestAutoCleanToggle_RejectsEnableWithoutAck(t *testing.T) {
	cfg := &AutoCleanConfig{}
	saved := false
	svc := NewAutoCleanService(cfg, nil, func(*AutoCleanConfig) error {
		saved = true
		return nil
	})

	var resp AutoCleanToggleResponse
	err := svc.Toggle(AutoCleanToggleRequest{Enabled: true, AcknowledgedAt: ""}, &resp)
	if err == nil {
		t.Fatal("Toggle(enabled=true, ack=\"\") must error")
	}
	if saved {
		t.Fatal("config must NOT be saved on rejected toggle")
	}
	if cfg.Enabled {
		t.Fatal("rejected toggle must not flip Enabled")
	}
}

func TestAutoCleanToggle_AcceptsEnableWithAck(t *testing.T) {
	cfg := &AutoCleanConfig{}
	var saved *AutoCleanConfig
	svc := NewAutoCleanService(cfg, nil, func(c *AutoCleanConfig) error {
		saved = c
		return nil
	})

	var resp AutoCleanToggleResponse
	err := svc.Toggle(AutoCleanToggleRequest{Enabled: true, AcknowledgedAt: "2026-05-03T10:00:00Z"}, &resp)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !resp.Enabled {
		t.Fatal("response Enabled should be true")
	}
	if resp.AcknowledgedAt == "" {
		t.Fatal("response AcknowledgedAt should be persisted")
	}
	if saved == nil {
		t.Fatal("config not persisted via save callback")
	}
	if !cfg.Enabled || cfg.RiskAcknowledgedAt != "2026-05-03T10:00:00Z" {
		t.Fatalf("cfg not mutated: %+v", cfg)
	}
}

func TestAutoCleanToggle_DisableNoAckNeeded(t *testing.T) {
	cfg := &AutoCleanConfig{
		Enabled:            true,
		RiskAcknowledgedAt: "2026-05-01T00:00:00Z",
	}
	svc := NewAutoCleanService(cfg, nil, func(*AutoCleanConfig) error { return nil })

	var resp AutoCleanToggleResponse
	if err := svc.Toggle(AutoCleanToggleRequest{Enabled: false}, &resp); err != nil {
		t.Fatalf("disable must not error: %v", err)
	}
	if resp.Enabled {
		t.Fatal("disable should clear Enabled in response")
	}
	if cfg.Enabled {
		t.Fatal("disable should clear cfg.Enabled")
	}
}

func TestAutoCleanToggle_PropagatesSaveError(t *testing.T) {
	cfg := &AutoCleanConfig{}
	want := errors.New("disk full")
	svc := NewAutoCleanService(cfg, nil, func(*AutoCleanConfig) error { return want })

	var resp AutoCleanToggleResponse
	err := svc.Toggle(AutoCleanToggleRequest{Enabled: true, AcknowledgedAt: "2026-05-03T10:00:00Z"}, &resp)
	if !errors.Is(err, want) {
		t.Fatalf("Toggle should surface save error, got %v", err)
	}
}

func TestAutoCleanStatus_RendersConfigSnapshot(t *testing.T) {
	cfg := &AutoCleanConfig{
		Enabled:            true,
		RiskAcknowledgedAt: "2026-05-01T00:00:00Z",
		ModulesAllowed:     []string{"dev"},
		MinIdleDays:        90,
		MinSizeMB:          1024,
		SizeCapPerTickGB:   10,
	}
	svc := NewAutoCleanService(cfg, fakeStats{count: 3, freed: 9000}, nil)

	var resp AutoCleanStatusResponse
	if err := svc.Status(AutoCleanStatusRequest{}, &resp); err != nil {
		t.Fatalf("Status: %v", err)
	}
	if !resp.Enabled || resp.AcknowledgedAt != "2026-05-01T00:00:00Z" {
		t.Errorf("config snapshot wrong: %+v", resp)
	}
	if len(resp.ModulesAllowed) != 1 || resp.ModulesAllowed[0] != "dev" {
		t.Errorf("ModulesAllowed = %v", resp.ModulesAllowed)
	}
	if resp.Deletions7d != 3 || resp.FreedBytes7d != 9000 {
		t.Errorf("stats not threaded through: %+v", resp)
	}
}

func TestAutoCleanStatus_NilStatsZero(t *testing.T) {
	cfg := &AutoCleanConfig{}
	svc := NewAutoCleanService(cfg, nil, nil)
	var resp AutoCleanStatusResponse
	if err := svc.Status(AutoCleanStatusRequest{}, &resp); err != nil {
		t.Fatalf("Status with nil stats must not error: %v", err)
	}
	if resp.Deletions7d != 0 || resp.FreedBytes7d != 0 {
		t.Errorf("expected zero counters with nil stats: %+v", resp)
	}
}
