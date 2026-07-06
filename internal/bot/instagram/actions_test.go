package instagram

import "testing"

// TestTrackStagnation is a regression test: FetchFollowersList previously
// compared len(results) == len(seen) to detect scroll stagnation, which is a
// tautology (both are updated in lockstep, so it's always true) — the loop
// unconditionally broke after 4 attempts regardless of whether scrolling
// actually loaded new followers. trackStagnation instead compares the current
// count against the count from the previous scroll, matching the working
// pattern used in internal/bot/tiktok/actions.go.
func TestTrackStagnation(t *testing.T) {
	prevCount, noChangeRounds := 0, 0

	// New items loaded each round — never stagnates.
	for _, count := range []int{5, 12, 20} {
		prevCount, noChangeRounds = trackStagnation(count, prevCount, noChangeRounds)
		if noChangeRounds != 0 {
			t.Fatalf("count=%d: noChangeRounds = %d, want 0", count, noChangeRounds)
		}
	}

	// No new items for 3 consecutive rounds — should accumulate stagnation.
	for i := 1; i <= 3; i++ {
		prevCount, noChangeRounds = trackStagnation(20, prevCount, noChangeRounds)
		if noChangeRounds != i {
			t.Fatalf("stagnant round %d: noChangeRounds = %d, want %d", i, noChangeRounds, i)
		}
	}
	if prevCount != 20 {
		t.Fatalf("prevCount = %d, want 20", prevCount)
	}

	// A subsequent increase resets stagnation.
	prevCount, noChangeRounds = trackStagnation(25, prevCount, noChangeRounds)
	if noChangeRounds != 0 || prevCount != 25 {
		t.Fatalf("after growth: prevCount=%d, noChangeRounds=%d, want 25, 0", prevCount, noChangeRounds)
	}
}
