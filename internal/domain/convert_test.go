package domain

import "testing"

// stdCoef mirrors defaults.json's coefficients block; shared by other _test
// files in this package.
func stdCoef() Coefficients {
	return Coefficients{TurboGasoline: 1.7, TurboDiesel: 1.5, Rotary: 1.7, Supercharger: 1.7}
}

// stdClasses mirrors defaults.json's displacement_classes block; shared by
// other _test files in this package.
func stdClasses() []DispClass {
	c660, c1600 := 660, 1600
	return []DispClass{
		{Label: "~660cc", MaxCC: &c660},
		{Label: "~1600cc", MaxCC: &c1600},
		{Label: "無制限", MaxCC: nil},
	}
}

func TestConvertedCC(t *testing.T) {
	coef := stdCoef()
	cases := []struct {
		name   string
		cc     int
		engine EngineType
		fi     bool
		wantCC int
		wantOK bool
	}{
		{"gasoline NA", 1300, EngineGasoline, false, 1300, true},
		// 658*1.7 = 1118.599999999999909... (float64) -> truncates to 1118,
		// confirmed via `go run` before writing this expectation.
		{"gasoline turbo kei (float truncation)", 658, EngineGasoline, true, 1118, true},
		{"diesel NA", 2000, EngineDiesel, false, 2000, true},
		// 2000*1.5 = 3000.0 exactly.
		{"diesel turbo", 2000, EngineDiesel, true, 3000, true},
		// 1308*1.7 = 2223.599999999999909... (float64) -> truncates to 2223.
		{"rotary NA", 1308, EngineRotary, false, 2223, true},
		// Required case: rotary x turbo. 1308*1.7*1.7 =
		// 3780.11999999999989086064 (float64) -> truncates to 3780, NOT
		// 3779 and NOT a naive-decimal 3780 assumed without checking the
		// float64 rounding direction. Confirmed via `go run` before writing
		// this expectation (see PM task notes).
		{"rotary turbo (required case)", 1308, EngineRotary, true, 3780, true},
		{"ev ignores cc/fi", 0, EngineEV, false, 0, false},
		{"ev ignores cc/fi even when both set", 9999, EngineEV, true, 0, false},
		// Required case: converted cc lands exactly on a class boundary.
		{"kei NA exact 660 boundary", 660, EngineGasoline, false, 660, true},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			gotCC, gotOK := ConvertedCC(c.cc, c.engine, c.fi, coef)
			if gotCC != c.wantCC || gotOK != c.wantOK {
				t.Errorf("ConvertedCC(%d,%q,fi=%v) = (%d,%v), want (%d,%v)",
					c.cc, c.engine, c.fi, gotCC, gotOK, c.wantCC, c.wantOK)
			}
		})
	}
}

func TestDispClassOf(t *testing.T) {
	classes := stdClasses()
	cases := []struct {
		name        string
		convertedCC int
		ok          bool
		want        string
	}{
		{"ev", 0, false, "EV"},
		{"ev ignores convertedCC value", 99999, false, "EV"},
		{"well under first boundary", 500, true, "~660cc"},
		{"exact 660 boundary is inclusive (required case)", 660, true, "~660cc"},
		{"just over 660 falls to next class", 661, true, "~1600cc"},
		{"exact 1600 boundary is inclusive", 1600, true, "~1600cc"},
		{"just over 1600 falls to unlimited", 1601, true, "無制限"},
		{"far over falls to unlimited", 999999, true, "無制限"},
		{"integration: kei turbo 658cc -> 1118 -> ~1600cc", 1118, true, "~1600cc"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := DispClassOf(c.convertedCC, c.ok, classes)
			if got != c.want {
				t.Errorf("DispClassOf(%d, ok=%v) = %q, want %q", c.convertedCC, c.ok, got, c.want)
			}
		})
	}
}

// TestDispClassOf_OrderIndependent locks in a deliberate implementation
// choice: DispClassOf selects the minimum qualifying MaxCC rather than
// trusting the slice's physical order, so a shuffled / unlimited-first
// classes slice still yields the same result as the canonical ascending
// order used elsewhere in this file.
func TestDispClassOf_OrderIndependent(t *testing.T) {
	c660, c1600 := 660, 1600
	shuffled := []DispClass{
		{Label: "無制限", MaxCC: nil},
		{Label: "~1600cc", MaxCC: &c1600},
		{Label: "~660cc", MaxCC: &c660},
	}
	cases := []struct {
		convertedCC int
		want        string
	}{
		{500, "~660cc"},
		{660, "~660cc"},
		{1000, "~1600cc"},
		{1601, "無制限"},
	}
	for _, c := range cases {
		if got := DispClassOf(c.convertedCC, true, shuffled); got != c.want {
			t.Errorf("DispClassOf(%d, shuffled classes) = %q, want %q", c.convertedCC, got, c.want)
		}
	}
}
