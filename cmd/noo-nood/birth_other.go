//go:build !darwin

package main

import "time"

// pathBirthTime is unavailable off macOS (no portable creation time); the
// storm tracker falls back to first-sighting time in that case.
func pathBirthTime(string) (time.Time, bool) { return time.Time{}, false }
