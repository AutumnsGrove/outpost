package hetzner

// planRAMGB is the memory of the Hetzner Cloud plans, in GB, after the June 2026
// rename (CX22 → CX23 and friends).
//
// A table rather than an API call on purpose: publish runs on the Mac, needs
// only R2 credentials, and `--dry-run` should work with no network at all.
// PlanRAM's answer for a plan it does not know is "unknown", which skips the
// check rather than guessing — a stale table must never refuse a pack outright.
var planRAMGB = map[string]int{
	"cx23": 4, "cx33": 8, "cx43": 16, "cx53": 32,
	"cax11": 4, "cax21": 8, "cax31": 16, "cax41": 32,
	"cpx11": 2, "cpx21": 4, "cpx31": 8, "cpx41": 16, "cpx51": 32,
	"ccx13": 8, "ccx23": 16, "ccx33": 32,
}

// PlanRAM reports the memory of a server plan and whether the table knows it.
func PlanRAM(serverType string) (int, bool) {
	gb, ok := planRAMGB[serverType]
	return gb, ok
}
