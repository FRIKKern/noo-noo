package autoclean

// Budget tracks a per-tick size cap. Not safe for concurrent use; each
// tick constructs a fresh Budget. The cap is the daily ceiling beyond
// which the autoclean engine will not delete more bytes — even if every
// other gate would let it. Default is 10 GiB (SizeCapPerTickGB=10),
// chosen so a runaway evaluation that flagged everything in $HOME can't
// strip more than 10 GiB before the next opportunity for human override.
//
// The "per tick" name is a slight misnomer kept for source-spec parity:
// in production the daemon ticks once per day, so per-tick == per-day.
type Budget struct {
	capBytes  int64
	usedBytes int64
}

// NewBudget creates a budget with capGB GiB available. Negative or zero
// caps produce a budget that immediately rejects everything (Take always
// returns false), which is the safe failure mode if config decoding ever
// produces a missing/invalid SizeCapPerTickGB.
func NewBudget(capGB int) *Budget {
	if capGB < 0 {
		capGB = 0
	}
	return &Budget{capBytes: int64(capGB) * 1024 * 1024 * 1024}
}

// Take attempts to charge `bytes` against the budget. Returns true if
// the charge fits (and applies it); false otherwise (and leaves the
// budget unchanged — partial charges are not a thing). Negative inputs
// are rejected without touching the budget; zero is accepted as a no-op.
func (b *Budget) Take(bytes int64) bool {
	if bytes < 0 {
		return false
	}
	if b.usedBytes+bytes > b.capBytes {
		return false
	}
	b.usedBytes += bytes
	return true
}

// Remaining returns bytes still available within the cap.
func (b *Budget) Remaining() int64 { return b.capBytes - b.usedBytes }

// Used returns bytes consumed so far.
func (b *Budget) Used() int64 { return b.usedBytes }

// Cap returns the configured ceiling in bytes. Useful for log lines that
// want to render "used X of Y".
func (b *Budget) Cap() int64 { return b.capBytes }
