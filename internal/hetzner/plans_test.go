package hetzner

import "testing"

func TestPlanRAMKnowsTheCurrentPlansAndSaysWhenItDoesNot(t *testing.T) {
	tests := map[string]int{
		"cx23":  4,
		"cx33":  8,
		"cax11": 4,
		"cax21": 8,
	}
	for name, want := range tests {
		gb, ok := PlanRAM(name)
		if !ok || gb != want {
			t.Errorf("PlanRAM(%q) = %d, %v; want %d, true", name, gb, ok, want)
		}
	}

	// An unknown plan is a gap in a table, not a refusal: publish skips the check
	// rather than refusing a rental over a name Hetzner made up after this was
	// written.
	if _, ok := PlanRAM("cx99"); ok {
		t.Fatal("an unknown plan was reported as known")
	}
}
