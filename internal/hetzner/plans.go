package hetzner

import "sort"

// Plan is one rentable Hetzner Cloud server type: what it is, what it holds and
// what it costs.
type Plan struct {
	Type  string
	VCPU  int
	RAMGB int

	// HourlyEUR is Hetzner's list price per hour, excluding VAT and *including*
	// the primary IPv4 address a Minecraft server needs to be reachable at all.
	// It is the rate a new order is billed at since the 15 June 2026 adjustment;
	// an existing server keeps its original rate, which is why the session stores
	// the rate it was rented at rather than looking one up later.
	HourlyEUR float64

	// ARM marks the Ampere line. Same vCPU and RAM, a little more money, and no
	// consequence for anything Outpost runs: the image and the Java server are
	// both multi-arch.
	ARM bool
}

// plans is a snapshot of the two Hetzner families worth renting here, smallest
// first: CX (shared x86) and CAX (shared Arm). CPX and CCX are deliberately
// absent — they cost several times as much per GB of RAM and a Minecraft server
// with four players does not need dedicated vCPUs.
//
// A table rather than an API call on purpose: `outpost publish` runs on the Mac,
// needs only R2 credentials, and `--dry-run` should work with no network at all.
// A plan this table does not know is "unknown", never "too small": a stale table
// must not refuse a rental or a publish over a name Hetzner invented afterwards.
var plans = []Plan{
	{Type: "cx23", VCPU: 2, RAMGB: 4, HourlyEUR: 0.0096},
	{Type: "cax11", VCPU: 2, RAMGB: 4, HourlyEUR: 0.0104, ARM: true},
	{Type: "cx33", VCPU: 4, RAMGB: 8, HourlyEUR: 0.0144},
	{Type: "cax21", VCPU: 4, RAMGB: 8, HourlyEUR: 0.0176, ARM: true},
	{Type: "cx43", VCPU: 8, RAMGB: 16, HourlyEUR: 0.0264},
	{Type: "cax31", VCPU: 8, RAMGB: 16, HourlyEUR: 0.0344, ARM: true},
	{Type: "cx53", VCPU: 16, RAMGB: 32, HourlyEUR: 0.0481},
	{Type: "cax41", VCPU: 16, RAMGB: 32, HourlyEUR: 0.0665, ARM: true},
}

// Plans returns every plan this knows about, smallest first: RAM, then price,
// then name so two plans with the same shape have a stable order.
func Plans() []Plan {
	out := make([]Plan, len(plans))
	copy(out, plans)
	sort.Slice(out, func(i, j int) bool {
		if out[i].RAMGB != out[j].RAMGB {
			return out[i].RAMGB < out[j].RAMGB
		}
		if out[i].HourlyEUR != out[j].HourlyEUR {
			return out[i].HourlyEUR < out[j].HourlyEUR
		}
		return out[i].Type < out[j].Type
	})
	return out
}

// Lookup reports what is known about a server type.
func Lookup(serverType string) (Plan, bool) {
	for _, p := range plans {
		if p.Type == serverType {
			return p, true
		}
	}
	return Plan{}, false
}

// PlanRAM reports the memory of a server plan and whether the table knows it.
func PlanRAM(serverType string) (int, bool) {
	if p, ok := Lookup(serverType); ok {
		return p.RAMGB, true
	}
	return 0, false
}

// Rate is the hourly list price of a plan. An unknown plan answers false: a cost
// readout must say "unknown", not a made-up number.
func Rate(serverType string) (float64, bool) {
	if p, ok := Lookup(serverType); ok {
		return p.HourlyEUR, true
	}
	return 0, false
}

// SmallestFit is the cheapest plan that can hold a pack asking for ramGB. It is
// what the picker defaults to, and it answers false only when nothing in the
// table is big enough — the same question `publish` asks when it refuses a pack.
func SmallestFit(ramGB int) (Plan, bool) {
	var best Plan
	found := false
	for _, p := range Plans() {
		if p.RAMGB < ramGB {
			continue
		}
		if !found || p.HourlyEUR < best.HourlyEUR {
			best, found = p, true
		}
	}
	return best, found
}
