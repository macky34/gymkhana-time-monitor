package domain

import "sort"

// ComboKey identifies a (driver,vehicle) combination — the ranking unit
// (DESIGN.md §4.3: "集計単位 = (driver_id, vehicle_id) の組み合わせ").
type ComboKey struct{ DriverID, VehicleID int64 }

// RunRow is one non-deleted logs row (is_deleted=0), as fed into heat
// numbering and ranking.
type RunRow struct {
	LogID       int64
	Combo       ComboKey
	RawMS       int
	PTCount     int
	IsMC        bool
	TimestampMS int64
}

// ComboMeta carries the tie-break metadata the web layer builds from the
// drivers/vehicles rows for a given combination.
type ComboMeta struct {
	ConvertedCC int  // ignored when IsEV is true
	IsEV        bool // true -> treated as +Inf (loses ties to any ICE)
}

// Standing is one ranked row produced by Rank.
type Standing struct {
	Combo     ComboKey
	BestMS    *int // nil when the combo has no qualifying valid run
	SecondMS  *int // nil when there is no 2nd qualifying valid run
	BestLogID *int64
	BestAtMS  int64 // timestamp_ms of the run backing BestMS (front-runner tie-break)
	Runs      int   // total run count for the combo (all runs, any heat)
	ValidRuns int   // total valid run count for the combo (all runs, any heat)
	PTTotal   int   // sum of pt_count across all runs for the combo
	Invalid   bool  // true -> trailing, unranked group
}

// Rank sorts every (driver,vehicle) combination found in runs per the full
// order defined in DESIGN.md §4.3 (this is the single place ranking order
// is computed; web/CSV layers only filter+renumber the result).
//
// Standing.Runs / ValidRuns / PTTotal always reflect each combo's complete
// run history and are unaffected by heat.
//
//   - heat == 0 (normal): BestMS/SecondMS are the smallest/2nd-smallest
//     valid final_ms across the combo's own run history.
//   - heat > 0: only the single run whose derived heat number (§4.4) equals
//     heat is used as "best" for comparison purposes; SecondMS is always
//     nil in this mode. A combo with no run at that heat number, or whose
//     run at that heat number is invalid, is placed in the trailing
//     Invalid group for this call (regardless of its ValidRuns overall).
//
// Combos with no qualifying valid run (Invalid=true) sort last, ordered
// deterministically by ComboKey (DriverID, then VehicleID ascending) per
// DESIGN.md §4.3's "有効タイムを1本も持たない組み合わせは末尾グループ".
// The same ComboKey order is used as a final fallback tie-break among valid
// rows too (not called for explicitly by DESIGN.md, since real timestamps
// make a full 4-way tie practically impossible) purely so the result never
// depends on Go's randomized map iteration order.
func Rank(runs []RunRow, meta map[ComboKey]ComboMeta, ptMode string, ptPenaltyMS int, heat int) []Standing {
	byCombo := make(map[ComboKey][]RunRow)
	for _, r := range runs {
		byCombo[r.Combo] = append(byCombo[r.Combo], r)
	}

	var heatNos map[int64]int
	if heat > 0 {
		heatNos = HeatNumbers(runs)
	}

	standings := make([]Standing, 0, len(byCombo))
	for combo, rs := range byCombo {
		st := Standing{Combo: combo, Runs: len(rs)}
		for _, r := range rs {
			_, invalid := FinalMS(r.RawMS, r.PTCount, r.IsMC, ptMode, ptPenaltyMS)
			if !invalid {
				st.ValidRuns++
			}
			st.PTTotal += r.PTCount
		}

		if heat > 0 {
			applyHeatBest(&st, rs, heatNos, heat, ptMode, ptPenaltyMS)
		} else {
			applyOverallBest(&st, rs, ptMode, ptPenaltyMS)
		}

		standings = append(standings, st)
	}

	sort.Slice(standings, func(i, j int) bool {
		return lessStanding(standings[i], standings[j], meta)
	})
	return standings
}

// applyHeatBest fills st's Best*/Invalid fields using only the single run
// (if any) whose heat number equals heat.
func applyHeatBest(st *Standing, rs []RunRow, heatNos map[int64]int, heat int, ptMode string, ptPenaltyMS int) {
	for _, r := range rs {
		if heatNos[r.LogID] != heat {
			continue
		}
		fms, invalid := FinalMS(r.RawMS, r.PTCount, r.IsMC, ptMode, ptPenaltyMS)
		if invalid {
			st.Invalid = true
			return
		}
		ms := fms
		logID := r.LogID
		st.BestMS = &ms
		st.BestLogID = &logID
		st.BestAtMS = r.TimestampMS
		return
	}
	st.Invalid = true // no run carries this heat number for this combo
}

// applyOverallBest fills st's Best*/Second*/Invalid fields from the combo's
// full valid run history (smallest and 2nd-smallest final_ms).
func applyOverallBest(st *Standing, rs []RunRow, ptMode string, ptPenaltyMS int) {
	type finalRun struct {
		ms    int
		logID int64
		atMS  int64
	}
	finals := make([]finalRun, 0, len(rs))
	for _, r := range rs {
		fms, invalid := FinalMS(r.RawMS, r.PTCount, r.IsMC, ptMode, ptPenaltyMS)
		if invalid {
			continue
		}
		finals = append(finals, finalRun{fms, r.LogID, r.TimestampMS})
	}
	if len(finals) == 0 {
		st.Invalid = true
		return
	}
	sort.Slice(finals, func(i, j int) bool {
		if finals[i].ms != finals[j].ms {
			return finals[i].ms < finals[j].ms
		}
		// Deterministic pick among a combo's own equal-time runs: earliest
		// timestamp, then LogID. Not separately specified by DESIGN.md,
		// but consistent with the front-runner ("先出し") tie-break
		// philosophy already used one level up.
		if finals[i].atMS != finals[j].atMS {
			return finals[i].atMS < finals[j].atMS
		}
		return finals[i].logID < finals[j].logID
	})

	best := finals[0]
	bms, blid := best.ms, best.logID
	st.BestMS = &bms
	st.BestLogID = &blid
	st.BestAtMS = best.atMS
	if len(finals) > 1 {
		sms := finals[1].ms
		st.SecondMS = &sms
	}
}

func lessCombo(a, b ComboKey) bool {
	if a.DriverID != b.DriverID {
		return a.DriverID < b.DriverID
	}
	return a.VehicleID < b.VehicleID
}

// lessStanding implements the DESIGN.md §4.3 total order.
func lessStanding(a, b Standing, meta map[ComboKey]ComboMeta) bool {
	if a.Invalid != b.Invalid {
		return !a.Invalid // valid rows sort before the trailing invalid group
	}
	if a.Invalid { // both invalid: deterministic ComboKey order
		return lessCombo(a.Combo, b.Combo)
	}

	// 1. best ascending.
	if *a.BestMS != *b.BestMS {
		return *a.BestMS < *b.BestMS
	}

	// 2. second: having one beats not having one; else ascending.
	aHas, bHas := a.SecondMS != nil, b.SecondMS != nil
	if aHas != bHas {
		return aHas
	}
	if aHas && *a.SecondMS != *b.SecondMS {
		return *a.SecondMS < *b.SecondMS
	}

	// 3. converted cc ascending; EV = +Inf.
	am, bm := meta[a.Combo], meta[b.Combo]
	if am.IsEV != bm.IsEV {
		return !am.IsEV
	}
	if !am.IsEV && am.ConvertedCC != bm.ConvertedCC {
		return am.ConvertedCC < bm.ConvertedCC
	}

	// 4. earliest best-run timestamp wins ("先出し").
	if a.BestAtMS != b.BestAtMS {
		return a.BestAtMS < b.BestAtMS
	}

	// Deterministic fallback (see Rank's doc comment).
	return lessCombo(a.Combo, b.Combo)
}
