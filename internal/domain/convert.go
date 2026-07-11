// Package domain contains the pure, dependency-free business rules shared by
// the rest of timemon: displacement conversion, class lookup, penalty/final
// time computation, heat numbering, and ranking with tie-breaks.
//
// Nothing in this package performs I/O. See docs/CONTRACTS.md §1 for the
// frozen type/function shapes and plan/DESIGN.md §4 for the authoritative
// rule text these implementations follow.
package domain

// EngineType is the vehicle engine category used for displacement
// conversion. Mirrors vehicles.engine_type in the DB / JSON.
type EngineType string

const (
	EngineGasoline EngineType = "gasoline"
	EngineDiesel   EngineType = "diesel"
	EngineRotary   EngineType = "rotary"
	EngineEV       EngineType = "ev"
)

// Coefficients mirrors the settings.coefficients JSON shape.
type Coefficients struct {
	TurboGasoline float64 `json:"turbo_gasoline"`
	TurboDiesel   float64 `json:"turbo_diesel"`
	Rotary        float64 `json:"rotary"`
	Supercharger  float64 `json:"supercharger"`
}

// DispClass mirrors one entry of the settings.displacement_classes JSON
// array.
type DispClass struct {
	Label string `json:"label"`
	MaxCC *int   `json:"max_cc"` // nil = unlimited catch-all
}

// ConvertedCC computes the converted displacement used for class lookup and
// ranking tie-breaks (DESIGN.md §4.1).
//
//	ev       -> (0, false)                                     — no conversion
//	rotary   -> cc * coef.Rotary * (fi ? coef.TurboGasoline : 1)
//	gasoline -> cc * (fi ? coef.TurboGasoline : 1)
//	diesel   -> cc * (fi ? coef.TurboDiesel : 1)
//
// The fractional part is truncated (floored) to an integer.
//
// NOTE: Coefficients.Supercharger is intentionally unused by this formula —
// vehicles carry a single forced_induction bool with no turbo/supercharger
// distinction, so forced induction always applies the turbo_* coefficient
// per the frozen CONTRACTS.md formula (see final report for this callout).
func ConvertedCC(cc int, engine EngineType, forcedInduction bool, c Coefficients) (int, bool) {
	switch engine {
	case EngineEV:
		return 0, false
	case EngineRotary:
		mult := c.Rotary
		if forcedInduction {
			mult *= c.TurboGasoline
		}
		return int(float64(cc) * mult), true
	case EngineDiesel:
		mult := 1.0
		if forcedInduction {
			mult = c.TurboDiesel
		}
		return int(float64(cc) * mult), true
	default:
		// "gasoline", and defensively any unrecognized value, follow the
		// gasoline rule (no distinct multiplier besides turbo_gasoline).
		mult := 1.0
		if forcedInduction {
			mult = c.TurboGasoline
		}
		return int(float64(cc) * mult), true
	}
}

// DispClassOf resolves the display label for a converted displacement.
//
// ok=false (EV) always yields "EV". Otherwise it returns the label of the
// class with the smallest MaxCC that still satisfies convertedCC<=MaxCC
// (MaxCC=nil is an unlimited catch-all, treated as +Inf so any finite class
// wins the comparison). Selecting the minimum qualifying MaxCC rather than
// trusting slice order makes the result correct regardless of how the
// caller ordered classes, while being identical to a simple left-to-right
// scan when classes are already ascending (the normal case, e.g. from
// defaults.json / settings.displacement_classes).
func DispClassOf(convertedCC int, ok bool, classes []DispClass) string {
	if !ok {
		return "EV"
	}
	label := ""
	var bestMax *int
	have := false
	for _, c := range classes {
		if c.MaxCC != nil && convertedCC > *c.MaxCC {
			continue // doesn't qualify
		}
		switch {
		case !have:
			have, label, bestMax = true, c.Label, c.MaxCC
		case c.MaxCC == nil:
			// an unlimited candidate never beats an already-qualifying entry
		case bestMax == nil || *c.MaxCC < *bestMax:
			label, bestMax = c.Label, c.MaxCC
		}
	}
	return label
}
