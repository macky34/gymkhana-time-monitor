package domain

// FinalMS applies the pit-touch penalty policy to one run (DESIGN.md §4.2).
//
//	invalid = isMC || (ptMode == "invalidate" && ptCount > 0)
//	final   = ptMode == "add" ? rawMS + ptCount*ptPenaltyMS : rawMS
//
// final is always computed (even when invalid), so callers that need the
// raw "add" total for display/audit purposes can still get it; ranking and
// other consumers must check invalid before using finalMS.
func FinalMS(rawMS, ptCount int, isMC bool, ptMode string, ptPenaltyMS int) (finalMS int, invalid bool) {
	invalid = isMC || (ptMode == "invalidate" && ptCount > 0)
	if ptMode == "add" {
		finalMS = rawMS + ptCount*ptPenaltyMS
	} else {
		finalMS = rawMS
	}
	return finalMS, invalid
}
