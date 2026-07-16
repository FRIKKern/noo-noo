package store

import (
	"fmt"
	"time"
)

// DiskSample is one row of disk_space_history: a point-in-time capacity
// reading for a single volume. Sizing a whole VOLUME is a statfs primitive
// (core.VolumeCapacity), distinct from the sizer package which owns per-ASSET
// allocated-byte truth.
type DiskSample struct {
	VolumeUUID string
	MountPoint string
	TotalBytes int64
	FreeBytes  int64
	At         time.Time
}

// RecordDiskSpace appends a capacity sample for one volume. Modeled on
// RecordCacheSize; callers (scan.ScanRoots) throttle to at most one row per
// volume per hour so pressure-triggered scans do not flood the series.
func (s *Store) RecordDiskSpace(volumeUUID, mountPoint string, totalBytes, freeBytes int64, at time.Time) error {
	_, err := s.db.Exec(
		`INSERT INTO disk_space_history(volume_uuid, mount_point, total_bytes, free_bytes, recorded_at)
		 VALUES(?, ?, ?, ?, ?)`,
		volumeUUID, mountPoint, totalBytes, freeBytes, at.UTC(),
	)
	if err != nil {
		return fmt.Errorf("insert disk_space_history: %w", err)
	}
	return nil
}

// DiskSpaceSeries returns all samples for volumeUUID with recorded_at >= since,
// in chronological order. `noo-noo trends` reads this to project days-until-full
// from the real fill rate; the scan throttle also reads it (a non-empty result
// within the last hour means "already sampled, skip").
func (s *Store) DiskSpaceSeries(volumeUUID string, since time.Time) ([]DiskSample, error) {
	rows, err := s.db.Query(
		`SELECT volume_uuid, mount_point, total_bytes, free_bytes, recorded_at
		   FROM disk_space_history
		  WHERE volume_uuid = ? AND recorded_at >= ?
		  ORDER BY recorded_at ASC`,
		volumeUUID, since.UTC(),
	)
	if err != nil {
		return nil, fmt.Errorf("query disk_space_history: %w", err)
	}
	defer func() { _ = rows.Close() }()
	var out []DiskSample
	for rows.Next() {
		var d DiskSample
		if err := rows.Scan(&d.VolumeUUID, &d.MountPoint, &d.TotalBytes, &d.FreeBytes, &d.At); err != nil {
			return nil, err
		}
		out = append(out, d)
	}
	return out, rows.Err()
}
