package publish

import (
	"strings"
	"testing"
)

// Where the boundaries are is not a guess: it is what the mods themselves declare.
// fabric-api says `java >=17` for 1.17–1.20.4, `>=21` for 1.20.5–1.21.x and `>=25`
// for 26.x, and a server on the wrong JDK dies on "Unsupported class file major
// version" after the VPS has been paid for.
func TestJavaForFollowsTheRanges(t *testing.T) {
	for _, tc := range []struct {
		mc   string
		java int
		ok   bool
	}{
		{"1.17", 17, true},
		{"1.18.2", 17, true},
		{"1.19.4", 17, true}, // inside a range, not a button in the panel
		{"1.20.1", 17, true}, // the default
		{"1.20.4", 17, true}, // the last Java 17 version
		{"1.20.5", 21, true}, // the first Java 21 version
		{"1.20.6", 21, true}, // a range can start mid-family
		{"1.21.11", 21, true},
		{"1.21.99", 21, true}, // a patch nobody has released yet
		{"26.1", 25, true},
		{"26.3", 25, true},
		{"1.16.5", 0, false}, // before the first range
		{"1.22", 0, false},   // between 1.21.x and 26.1
		{"26.0", 0, false},   // below the 26 range
		{"", 0, false},
	} {
		java, ok := JavaFor(tc.mc)
		if ok != tc.ok || java != tc.java {
			t.Fatalf("JavaFor(%q) = %d, %v; want %d, %v", tc.mc, java, ok, tc.java, tc.ok)
		}
	}
}

func TestCompareMCOrdersByNumberNotByString(t *testing.T) {
	for _, tc := range []struct {
		a, b string
		want int
	}{
		{"1.20.4", "1.20.10", -1}, // the string comparison gets this wrong
		{"1.20.10", "1.20.4", 1},
		{"1.21.11", "26.1", -1},
		{"26.3", "26.3", 0},
		{"1.21", "1.21.0", 0},
	} {
		if got := compareMC(tc.a, tc.b); got != tc.want {
			t.Fatalf("compareMC(%q, %q) = %d, want %d", tc.a, tc.b, got, tc.want)
		}
	}
}

// The picker is a shortlist, and it says which rows somebody has actually booted.
func TestTheOffersMatchWhatHasBeenBooted(t *testing.T) {
	if len(Minecrafts) < 2 {
		t.Fatalf("the picker offers %d versions", len(Minecrafts))
	}
	if Minecrafts[0].MC != SupportedMC {
		t.Fatalf("row zero is %s but the default is %s", Minecrafts[0].MC, SupportedMC)
	}
	booted := map[string]bool{}
	for _, m := range Minecrafts {
		java, ok := JavaFor(m.MC)
		if !ok {
			t.Fatalf("%s is offered and no range covers it", m.MC)
		}
		if java != m.Java {
			t.Fatalf("%s is offered as Java %d, the ranges say %d", m.MC, m.Java, java)
		}
		if m.Booted {
			booted[m.MC] = true
		}
	}
	// The three that have had a real rent come up on them, and nothing that has not.
	want := []string{"1.20.1", "1.21.1", "26.3"}
	for _, mc := range want {
		if !booted[mc] {
			t.Fatalf("%s has booted on this project and is not marked as such", mc)
		}
	}
	if len(booted) != len(want) {
		t.Fatalf("marked booted: %v, want %v", booted, want)
	}
}

func TestMinecraftListNamesTheRanges(t *testing.T) {
	list := MinecraftList()
	for _, want := range []string{"1.17–1.20.4 (Java 17)", "1.20.5–1.21.x (Java 21)", "26.1–26.x (Java 25)"} {
		if !strings.Contains(list, want) {
			t.Fatalf("MinecraftList() = %q, missing %q", list, want)
		}
	}
}

// Ordering is not validation: compareMC reads the digits it finds, so a string that
// is not a version at all would sort inside a range and reach pack.json.
func TestAVersionThatIsNotAVersionIsRefused(t *testing.T) {
	// Surrounding whitespace is trimmed on purpose (MinecraftFor's doc), so it is not
	// in this list; everything else is a string that is not a version.
	for _, mc := range []string{"1.20.4x", "1.20.4.", ".1.20", "1.21.x", "latest", "1.20.1.0.1"} {
		if _, ok := JavaFor(mc); ok {
			t.Fatalf("JavaFor(%q) accepted something that is not a Minecraft version", mc)
		}
		if _, ok := MinecraftFor(mc); ok {
			t.Fatalf("MinecraftFor(%q) accepted something that is not a Minecraft version", mc)
		}
	}
	// The shapes that are versions, with and without a patch number.
	for _, mc := range []string{"1.20", "1.20.1", "26.3"} {
		if _, ok := JavaFor(mc); !ok {
			t.Fatalf("JavaFor(%q) refused a real version shape", mc)
		}
	}
}
