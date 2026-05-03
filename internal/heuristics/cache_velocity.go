package heuristics

import (
	"context"
	"fmt"
	"time"

	"github.com/FRIKKern/noo-noo/internal/config"
	"github.com/FRIKKern/noo-noo/internal/store"
)

// CacheVelocity flags cache directories that have grown faster than
// cfg.Heuristics.CacheVelocity.GrowthMultiplier over WindowDays. Risk: medium
// (deletion may invalidate dev caches; user should review).
func CacheVelocity(ctx context.Context, st *store.Store, cfg config.Config) []Suggestion {
	if !cfg.Heuristics.CacheVelocity.Enabled {
		return nil
	}
	windowDays := cfg.Heuristics.CacheVelocity.WindowDays
	multi := cfg.Heuristics.CacheVelocity.GrowthMultiplier

	targets, err := distinctCacheTargets(st)
	if err != nil {
		return nil
	}
	now := time.Now()
	since := now.Add(-time.Duration(windowDays) * 24 * time.Hour)

	out := make([]Suggestion, 0, len(targets))
	for _, target := range targets {
		select {
		case <-ctx.Done():
			return out
		default:
		}
		series, err := st.CacheSizeSeries(target, since)
		if err != nil || len(series) < 2 {
			continue
		}
		oldest := series[0]
		latest := series[len(series)-1]
		if oldest.Bytes <= 0 {
			continue
		}
		ratio := float64(latest.Bytes) / float64(oldest.Bytes)
		if ratio < multi {
			continue
		}
		out = append(out, Suggestion{
			Module: "cache_velocity",
			Target: target,
			Reason: fmt.Sprintf("%.1fx growth in %dd (%d -> %d bytes)", ratio,
				windowDays, oldest.Bytes, latest.Bytes),
			Evidence: map[string]any{
				"ratio":         ratio,
				"window_days":   windowDays,
				"bytes_then":    oldest.Bytes,
				"bytes_now":     latest.Bytes,
				"recorded_then": oldest.At,
				"recorded_now":  latest.At,
			},
			RiskLevel: RiskMedium,
			CreatedAt: now,
		})
	}
	return out
}

// distinctCacheTargets returns the distinct target_path values currently in
// cache_size_history. Uses the store's underlying *sql.DB directly so we don't
// need to widen the store's public surface for a heuristic-private query.
func distinctCacheTargets(st *store.Store) ([]string, error) {
	rows, err := st.DB().Query(`SELECT DISTINCT target_path FROM cache_size_history ORDER BY target_path`)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	var out []string
	for rows.Next() {
		var t string
		if err := rows.Scan(&t); err != nil {
			return nil, err
		}
		out = append(out, t)
	}
	return out, rows.Err()
}
