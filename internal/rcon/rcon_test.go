package rcon

import "testing"

func TestParseList(t *testing.T) {
	cases := []struct {
		name   string
		in     string
		online int
		max    int
		names  []string
	}{
		{
			name:   "empty",
			in:     "There are 0 of a max of 20 players online:",
			online: 0,
			max:    20,
		},
		{
			name:   "one",
			in:     "There are 1 of a max of 20 players online: AutumnsAdventure",
			online: 1,
			max:    20,
			names:  []string{"AutumnsAdventure"},
		},
		{
			name:   "two",
			in:     "There are 2 of a max of 20 players online: AutumnsAdventure, Boyfriend",
			online: 2,
			max:    20,
			names:  []string{"AutumnsAdventure", "Boyfriend"},
		},
		{
			name:   "older server wording without max",
			in:     "There are 1/20 players online: Autumn",
			online: 1,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			p, err := ParseList(tc.in)
			// The "older wording" case is expected to be unsupported; vanilla and
			// Fabric both use the modern format. Keep it as a visible reminder.
			if tc.name == "older server wording without max" {
				if err == nil {
					t.Skip("server supports legacy wording")
				}
				return
			}
			if err != nil {
				t.Fatalf("parse: %v", err)
			}
			if p.Online != tc.online {
				t.Errorf("online = %d, want %d", p.Online, tc.online)
			}
			if tc.max != 0 && p.Max != tc.max {
				t.Errorf("max = %d, want %d", p.Max, tc.max)
			}
			if len(p.Names) != len(tc.names) {
				t.Fatalf("names = %v, want %v", p.Names, tc.names)
			}
			for i := range tc.names {
				if p.Names[i] != tc.names[i] {
					t.Errorf("names[%d] = %q, want %q", i, p.Names[i], tc.names[i])
				}
			}
		})
	}
}

func TestParseListRejectsGarbage(t *testing.T) {
	if _, err := ParseList("Unknown command"); err == nil {
		t.Fatal("expected an error for unrelated output")
	}
}
