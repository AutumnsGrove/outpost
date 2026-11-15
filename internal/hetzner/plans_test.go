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

func TestPlansArePricedAndOrderedBySize(t *testing.T) {
	all := Plans()
	if len(all) < 4 {
		t.Fatalf("plans = %v", all)
	}
	for i, p := range all {
		if p.VCPU <= 0 || p.RAMGB <= 0 || p.HourlyEUR <= 0 {
			t.Fatalf("%+v is missing a number the readout needs", p)
		}
		if i > 0 && all[i-1].RAMGB > p.RAMGB {
			t.Fatalf("%s came before %s", all[i-1].Type, p.Type)
		}
	}

	// The two families worth renting, and the numbers the picker shows.
	cx23, ok := Lookup("cx23")
	if !ok || cx23.VCPU != 2 || cx23.RAMGB != 4 || cx23.ARM {
		t.Fatalf("cx23 = %+v, %v", cx23, ok)
	}
	if rate, ok := Rate("cx23"); !ok || rate != 0.0096 {
		t.Fatalf("cx23 rate = %v, %v", rate, ok)
	}
	cax11, ok := Lookup("cax11")
	if !ok || !cax11.ARM || cax11.RAMGB != 4 {
		t.Fatalf("cax11 = %+v, %v", cax11, ok)
	}

	// A plan this table has never heard of is unknown, not free and not refused.
	if _, ok := Lookup("cx99"); ok {
		t.Fatal("an unknown plan was reported as known")
	}
	if _, ok := Rate("cx99"); ok {
		t.Fatal("an unknown plan was given a price")
	}
}

func TestSmallestFitPicksTheCheapestPlanThatHoldsThePack(t *testing.T) {
	// 4 GB fits cx23 and cax11; cx23 is cheaper.
	got, ok := SmallestFit(4)
	if !ok || got.Type != "cx23" {
		t.Fatalf("SmallestFit(4) = %+v, %v", got, ok)
	}
	if got, _ := SmallestFit(6); got.Type != "cx33" {
		t.Fatalf("SmallestFit(6) = %s, want cx33", got.Type)
	}
	if got, _ := SmallestFit(1); got.Type != "cx23" {
		t.Fatalf("SmallestFit(1) = %s, want the smallest plan anyway", got.Type)
	}
	// Nothing in the table holds a 64 GB pack, and saying so is the refusal.
	if _, ok := SmallestFit(64); ok {
		t.Fatal("64 GB was reported as fittable")
	}
}
