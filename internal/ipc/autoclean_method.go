package ipc

import (
	"errors"
	"time"
)

// AutoCleanConfig is the snapshot of the [auto_clean] config section the IPC
// service needs. The daemon's main loop builds one of these from the on-disk
// config and hands it to NewAutoCleanService; Toggle mutates it in place and
// invokes the save callback to persist back to TOML.
//
// Re-declared here (rather than imported from internal/config) so the IPC
// package stays compile-clean before T97 adds the matching TOML schema. After
// T97 lands, internal/config will own the canonical struct and main.go will
// translate its fields into this snapshot.
type AutoCleanConfig struct {
	Enabled            bool
	RiskAcknowledgedAt string
	ModulesAllowed     []string
	MinIdleDays        int
	MinSizeMB          int
	SizeCapPerTickGB   int
}

// AutoCleanStatsStore is the narrow read surface Status needs. The real
// *store.Store will satisfy this once the auto_clean_events table ships;
// tests pass nil because the toggle path never touches it.
type AutoCleanStatsStore interface {
	// AutoCleanStatsSince returns (count, freedBytes) of successful
	// 'deleted' rows whose started_at_unix >= sinceUnix.
	AutoCleanStatsSince(sinceUnix int64) (int, int64, error)
}

// AutoCleanStatusRequest is the wire request for AutoClean.Status.
type AutoCleanStatusRequest struct {
	HistoryLimit int `json:"history_limit,omitempty"`
}

// AutoCleanStatusResponse is the wire reply for AutoClean.Status.
type AutoCleanStatusResponse struct {
	Enabled          bool     `json:"enabled"`
	AcknowledgedAt   string   `json:"acknowledged_at"`
	ModulesAllowed   []string `json:"modules_allowed"`
	MinIdleDays      int      `json:"min_idle_days"`
	MinSizeMB        int      `json:"min_size_mb"`
	SizeCapPerTickGB int      `json:"size_cap_per_tick_gb"`
	Deletions7d      int      `json:"deletions_7d"`
	FreedBytes7d     int64    `json:"freed_bytes_7d"`
}

// AutoCleanToggleRequest is the wire request for AutoClean.Toggle. Enable
// requires a non-empty AcknowledgedAt; disable does not.
type AutoCleanToggleRequest struct {
	Enabled        bool   `json:"enabled"`
	AcknowledgedAt string `json:"acknowledged_at,omitempty"`
}

// AutoCleanToggleResponse echoes the post-toggle state.
type AutoCleanToggleResponse struct {
	Enabled        bool   `json:"enabled"`
	AcknowledgedAt string `json:"acknowledged_at"`
}

// AutoCleanService implements AutoClean.Status and AutoClean.Toggle. It
// owns a pointer to the live config so that Toggle's mutation is visible to
// the daemon's tick loop without a config reload.
type AutoCleanService struct {
	cfg   *AutoCleanConfig
	stats AutoCleanStatsStore
	save  func(*AutoCleanConfig) error
}

// NewAutoCleanService wires a service. stats may be nil when the caller
// doesn't yet have an auto_clean_events table (tests, early-boot daemon);
// Status returns zero deletion counters in that case rather than erroring.
func NewAutoCleanService(cfg *AutoCleanConfig, stats AutoCleanStatsStore, save func(*AutoCleanConfig) error) *AutoCleanService {
	return &AutoCleanService{cfg: cfg, stats: stats, save: save}
}

// Status renders the current auto-clean config plus 7-day delete stats.
func (s *AutoCleanService) Status(_ AutoCleanStatusRequest, resp *AutoCleanStatusResponse) error {
	if s.cfg == nil {
		return errors.New("autoclean: config not configured")
	}
	resp.Enabled = s.cfg.Enabled
	resp.AcknowledgedAt = s.cfg.RiskAcknowledgedAt
	resp.ModulesAllowed = s.cfg.ModulesAllowed
	resp.MinIdleDays = s.cfg.MinIdleDays
	resp.MinSizeMB = s.cfg.MinSizeMB
	resp.SizeCapPerTickGB = s.cfg.SizeCapPerTickGB
	if s.stats != nil {
		since := time.Now().Add(-7 * 24 * time.Hour).Unix()
		n, freed, err := s.stats.AutoCleanStatsSince(since)
		if err != nil {
			return err
		}
		resp.Deletions7d = n
		resp.FreedBytes7d = freed
	}
	return nil
}

// Toggle flips the master switch. Enabling requires the caller to supply
// AcknowledgedAt — the CLI guards this with --i-understand-the-risks so a
// bare `auto-clean on` cannot opt the user into unattended deletions.
func (s *AutoCleanService) Toggle(req AutoCleanToggleRequest, resp *AutoCleanToggleResponse) error {
	if s.cfg == nil {
		return errors.New("autoclean: config not configured")
	}
	if req.Enabled && req.AcknowledgedAt == "" {
		return errors.New("autoclean: Enable=true requires AcknowledgedAt; CLI must pass --i-understand-the-risks")
	}
	s.cfg.Enabled = req.Enabled
	if req.Enabled {
		s.cfg.RiskAcknowledgedAt = req.AcknowledgedAt
	}
	if s.save != nil {
		if err := s.save(s.cfg); err != nil {
			return err
		}
	}
	resp.Enabled = s.cfg.Enabled
	resp.AcknowledgedAt = s.cfg.RiskAcknowledgedAt
	return nil
}
