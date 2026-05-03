// Package core contains foundational primitives shared by every cleanup
// module: byte-size formatting, directory walks, and path-safety checks.
package core

import (
	"fmt"
	"strconv"
	"strings"
)

// Bytes is a byte count, formatted human-readable via String().
type Bytes int64

const (
	kb Bytes = 1024
	mb       = 1024 * kb
	gb       = 1024 * mb
	tb       = 1024 * gb
)

func (b Bytes) String() string {
	switch {
	case b >= tb:
		return fmt.Sprintf("%.1f TB", float64(b)/float64(tb))
	case b >= gb:
		return fmt.Sprintf("%.1f GB", float64(b)/float64(gb))
	case b >= mb:
		return fmt.Sprintf("%.1f MB", float64(b)/float64(mb))
	case b >= kb:
		return fmt.Sprintf("%.1f KB", float64(b)/float64(kb))
	default:
		return fmt.Sprintf("%d B", int64(b))
	}
}

// ParseBytes accepts forms like "100", "1KB", "1.5MB", "2GB", "0.5TB".
// Case-insensitive. No suffix means bytes.
func ParseBytes(s string) (Bytes, error) {
	s = strings.TrimSpace(strings.ToUpper(s))
	if s == "" {
		return 0, fmt.Errorf("empty size")
	}
	var mult Bytes = 1
	switch {
	case strings.HasSuffix(s, "TB"):
		mult, s = tb, strings.TrimSuffix(s, "TB")
	case strings.HasSuffix(s, "GB"):
		mult, s = gb, strings.TrimSuffix(s, "GB")
	case strings.HasSuffix(s, "MB"):
		mult, s = mb, strings.TrimSuffix(s, "MB")
	case strings.HasSuffix(s, "KB"):
		mult, s = kb, strings.TrimSuffix(s, "KB")
	}
	v, err := strconv.ParseFloat(strings.TrimSpace(s), 64)
	if err != nil {
		return 0, fmt.Errorf("parse %q: %w", s, err)
	}
	return Bytes(v * float64(mult)), nil
}
