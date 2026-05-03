package autoclean

import "testing"

const GB = int64(1024 * 1024 * 1024)

// TestBudgetGreedyFit walks through a sequence of takes that exercises
// both the accept and reject branches and checks the Used accumulator
// only moves on accepts.
func TestBudgetGreedyFit(t *testing.T) {
	b := NewBudget(10) // 10 GiB
	if !b.Take(3 * GB) {
		t.Error("3 GB should fit")
	}
	if !b.Take(4 * GB) {
		t.Error("4 GB should fit (now 7 used)")
	}
	if b.Take(4 * GB) {
		t.Error("4 GB should reject (would exceed 10)")
	}
	if !b.Take(2 * GB) {
		t.Error("2 GB should fit (9 used)")
	}
	if b.Take(2 * GB) {
		t.Error("2 GB should reject (would exceed 10)")
	}
	if b.Used() != 9*GB {
		t.Errorf("used = %d, want 9 GB", b.Used())
	}
}

// TestBudgetRejectNegative: a negative charge is a programming bug, not
// a refund. Refuse it and leave the budget alone.
func TestBudgetRejectNegative(t *testing.T) {
	b := NewBudget(10)
	if b.Take(-1) {
		t.Error("negative bytes must reject")
	}
	if b.Used() != 0 {
		t.Error("rejected take must not charge")
	}
}

// TestBudgetExactlyFits: the cap edge is inclusive — exactly capBytes
// fits, capBytes+1 does not.
func TestBudgetExactlyFits(t *testing.T) {
	b := NewBudget(10)
	if !b.Take(10 * GB) {
		t.Error("exact-fit should accept")
	}
	if b.Take(1) {
		t.Error("1 byte over should reject")
	}
	if b.Remaining() != 0 {
		t.Errorf("remaining = %d, want 0", b.Remaining())
	}
}

// TestBudgetZeroCap: a 0 GiB budget rejects every positive charge but
// accepts a no-op zero charge. This is the safe-degraded mode if the
// config ever yields SizeCapPerTickGB == 0.
func TestBudgetZeroCap(t *testing.T) {
	b := NewBudget(0)
	if b.Cap() != 0 {
		t.Errorf("cap = %d, want 0", b.Cap())
	}
	if b.Take(1) {
		t.Error("any positive bytes must reject against 0 cap")
	}
	if !b.Take(0) {
		t.Error("zero charge must succeed (no-op)")
	}
}

// TestBudgetNegativeCapClamped: negative capGB shouldn't underflow into
// a giant positive budget — clamp to 0 and behave like zero cap.
func TestBudgetNegativeCapClamped(t *testing.T) {
	b := NewBudget(-5)
	if b.Cap() != 0 {
		t.Errorf("negative cap must clamp to 0, got %d", b.Cap())
	}
	if b.Take(1) {
		t.Error("negative-clamped budget must reject any positive charge")
	}
}

// TestBudgetDefaultIs10GB: the documented default in the plan is 10 GiB
// per day. Anchored as a test so a refactor that changes the unit
// conversion (e.g. GB vs GiB) trips here.
func TestBudgetDefaultIs10GB(t *testing.T) {
	b := NewBudget(10)
	if b.Cap() != 10*GB {
		t.Errorf("default 10 GiB cap = %d, want %d", b.Cap(), 10*GB)
	}
}
