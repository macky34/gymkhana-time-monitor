package domain

import "testing"

// TestFinalMS covers every combination of pt_mode x isMC required by the PM
// task ("pt_mode両方(add/invalidate) x MC").
func TestFinalMS(t *testing.T) {
	const penalty = 5000
	cases := []struct {
		name        string
		raw, pt     int
		isMC        bool
		mode        string
		wantFinal   int
		wantInvalid bool
	}{
		{"add/no-pt/no-mc", 80000, 0, false, "add", 80000, false},
		{"add/pt2/no-mc", 80000, 2, false, "add", 90000, false},
		{"add/no-pt/mc", 80000, 0, true, "add", 80000, true},
		{"add/pt2/mc", 80000, 2, true, "add", 90000, true}, // add still applies to final even though invalid
		{"invalidate/no-pt/no-mc", 80000, 0, false, "invalidate", 80000, false},
		{"invalidate/pt1/no-mc", 80000, 1, false, "invalidate", 80000, true}, // raw unchanged, but pt>0 invalidates
		{"invalidate/no-pt/mc", 80000, 0, true, "invalidate", 80000, true},
		{"invalidate/pt1/mc", 80000, 1, true, "invalidate", 80000, true},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			gotFinal, gotInvalid := FinalMS(c.raw, c.pt, c.isMC, c.mode, penalty)
			if gotFinal != c.wantFinal || gotInvalid != c.wantInvalid {
				t.Errorf("FinalMS(raw=%d,pt=%d,mc=%v,%q,penalty=%d) = (%d,%v), want (%d,%v)",
					c.raw, c.pt, c.isMC, c.mode, penalty, gotFinal, gotInvalid, c.wantFinal, c.wantInvalid)
			}
		})
	}
}
