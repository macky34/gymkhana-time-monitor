package domain

import "testing"

func comboOrder(sts []Standing) []ComboKey {
	out := make([]ComboKey, len(sts))
	for i, s := range sts {
		out[i] = s.Combo
	}
	return out
}

// TestRank is table-driven over full ranking scenarios (Architecture wiki: ランキング・集計仕様).
// Each case names which PM-required scenario it covers.
func TestRank(t *testing.T) {
	const addMode = "add"
	const penalty = 5000

	cases := []struct {
		name      string
		runs      []RunRow
		meta      map[ComboKey]ComboMeta
		ptMode    string
		penalty   int
		heat      int
		wantOrder []ComboKey
		check     func(t *testing.T, got []Standing)
	}{
		{
			name: "no second ranks below has second despite identical best (required case)",
			runs: []RunRow{
				{LogID: 1, Combo: ComboKey{DriverID: 1, VehicleID: 1}, RawMS: 80000, TimestampMS: 1000},
				{LogID: 2, Combo: ComboKey{DriverID: 2, VehicleID: 2}, RawMS: 80000, TimestampMS: 2000},
				{LogID: 3, Combo: ComboKey{DriverID: 2, VehicleID: 2}, RawMS: 85000, TimestampMS: 3000},
			},
			meta: map[ComboKey]ComboMeta{
				{DriverID: 1, VehicleID: 1}: {ConvertedCC: 1300},
				{DriverID: 2, VehicleID: 2}: {ConvertedCC: 1300},
			},
			ptMode:  addMode,
			penalty: penalty,
			wantOrder: []ComboKey{
				{DriverID: 2, VehicleID: 2},
				{DriverID: 1, VehicleID: 1},
			},
			check: func(t *testing.T, got []Standing) {
				if got[0].SecondMS == nil || *got[0].SecondMS != 85000 {
					t.Errorf("winner (has second) SecondMS = %v, want 85000", got[0].SecondMS)
				}
				if got[1].SecondMS != nil {
					t.Errorf("loser (no second) SecondMS = %v, want nil", *got[1].SecondMS)
				}
			},
		},
		{
			name: "same best+second, converted cc breaks the tie; EV is +Inf (required case)",
			runs: []RunRow{
				{LogID: 1, Combo: ComboKey{DriverID: 1, VehicleID: 1}, RawMS: 80000, TimestampMS: 1000},
				{LogID: 2, Combo: ComboKey{DriverID: 1, VehicleID: 1}, RawMS: 85000, TimestampMS: 1100},
				{LogID: 3, Combo: ComboKey{DriverID: 2, VehicleID: 2}, RawMS: 80000, TimestampMS: 2000},
				{LogID: 4, Combo: ComboKey{DriverID: 2, VehicleID: 2}, RawMS: 85000, TimestampMS: 2100},
				{LogID: 5, Combo: ComboKey{DriverID: 3, VehicleID: 3}, RawMS: 80000, TimestampMS: 3000},
				{LogID: 6, Combo: ComboKey{DriverID: 3, VehicleID: 3}, RawMS: 85000, TimestampMS: 3100},
			},
			meta: map[ComboKey]ComboMeta{
				{DriverID: 1, VehicleID: 1}: {ConvertedCC: 1300},
				{DriverID: 2, VehicleID: 2}: {ConvertedCC: 1600},
				{DriverID: 3, VehicleID: 3}: {IsEV: true},
			},
			ptMode:  addMode,
			penalty: penalty,
			wantOrder: []ComboKey{
				{DriverID: 1, VehicleID: 1}, // 1300cc
				{DriverID: 2, VehicleID: 2}, // 1600cc
				{DriverID: 3, VehicleID: 3}, // EV = +Inf, loses to any ICE
			},
		},
		{
			name: "all tied down to timestamp: earliest best-run wins (required case)",
			runs: []RunRow{
				{LogID: 1, Combo: ComboKey{DriverID: 1, VehicleID: 1}, RawMS: 80000, TimestampMS: 1000},
				{LogID: 2, Combo: ComboKey{DriverID: 2, VehicleID: 2}, RawMS: 80000, TimestampMS: 2000},
			},
			meta: map[ComboKey]ComboMeta{
				{DriverID: 1, VehicleID: 1}: {ConvertedCC: 1300},
				{DriverID: 2, VehicleID: 2}: {ConvertedCC: 1300},
			},
			ptMode:  addMode,
			penalty: penalty,
			wantOrder: []ComboKey{
				{DriverID: 1, VehicleID: 1}, // ts 1000, recorded first
				{DriverID: 2, VehicleID: 2}, // ts 2000
			},
		},
		{
			name: "zero valid runs trail, ordered deterministically by ComboKey (required case)",
			runs: []RunRow{
				{LogID: 1, Combo: ComboKey{DriverID: 2, VehicleID: 2}, RawMS: 90000, TimestampMS: 1000},            // valid
				{LogID: 2, Combo: ComboKey{DriverID: 5, VehicleID: 5}, RawMS: 70000, TimestampMS: 500, IsMC: true}, // invalid; would be "fastest" if it counted
				{LogID: 3, Combo: ComboKey{DriverID: 1, VehicleID: 1}, RawMS: 60000, TimestampMS: 200, IsMC: true}, // invalid
			},
			meta: map[ComboKey]ComboMeta{
				{DriverID: 2, VehicleID: 2}: {ConvertedCC: 1300},
				{DriverID: 5, VehicleID: 5}: {ConvertedCC: 1300},
				{DriverID: 1, VehicleID: 1}: {ConvertedCC: 1300},
			},
			ptMode:  addMode,
			penalty: penalty,
			wantOrder: []ComboKey{
				{DriverID: 2, VehicleID: 2}, // only valid combo, ranked
				{DriverID: 1, VehicleID: 1}, // invalid tail, ComboKey(1,1) < (5,5)
				{DriverID: 5, VehicleID: 5},
			},
			check: func(t *testing.T, got []Standing) {
				if got[0].Invalid {
					t.Errorf("valid combo incorrectly marked Invalid: %+v", got[0])
				}
				if !got[1].Invalid || !got[2].Invalid {
					t.Errorf("zero-valid-run combos not marked Invalid: %+v, %+v", got[1], got[2])
				}
			},
		},
		{
			name: "invalidate mode without MC also excludes the combo (required case: pt_mode x MC)",
			runs: []RunRow{
				{LogID: 1, Combo: ComboKey{DriverID: 1, VehicleID: 1}, RawMS: 70000, TimestampMS: 1000},
				{LogID: 2, Combo: ComboKey{DriverID: 2, VehicleID: 2}, RawMS: 60000, PTCount: 1, TimestampMS: 500}, // faster raw, but pt>0 invalidates under this mode
			},
			meta: map[ComboKey]ComboMeta{
				{DriverID: 1, VehicleID: 1}: {ConvertedCC: 1300},
				{DriverID: 2, VehicleID: 2}: {ConvertedCC: 1300},
			},
			ptMode:  "invalidate",
			penalty: penalty,
			wantOrder: []ComboKey{
				{DriverID: 1, VehicleID: 1},
				{DriverID: 2, VehicleID: 2},
			},
			check: func(t *testing.T, got []Standing) {
				if got[1].ValidRuns != 0 || !got[1].Invalid {
					t.Errorf("pt>0 under invalidate mode should yield ValidRuns=0/Invalid=true, got %+v", got[1])
				}
			},
		},
		{
			name: "heat filter uses only that heat's single run per combo, not the overall best (required case)",
			runs: []RunRow{
				{LogID: 1, Combo: ComboKey{DriverID: 1, VehicleID: 1}, RawMS: 70000, TimestampMS: 1000},             // A heat1 (fastest overall; must be ignored for heat=2)
				{LogID: 2, Combo: ComboKey{DriverID: 1, VehicleID: 1}, RawMS: 90000, TimestampMS: 2000},             // A heat2
				{LogID: 3, Combo: ComboKey{DriverID: 2, VehicleID: 2}, RawMS: 75000, TimestampMS: 1500},             // B heat1 only, no heat2 run exists
				{LogID: 4, Combo: ComboKey{DriverID: 3, VehicleID: 3}, RawMS: 60000, TimestampMS: 500},              // C heat1
				{LogID: 5, Combo: ComboKey{DriverID: 3, VehicleID: 3}, RawMS: 95000, TimestampMS: 2500, IsMC: true}, // C heat2, MC -> invalid
			},
			meta: map[ComboKey]ComboMeta{
				{DriverID: 1, VehicleID: 1}: {ConvertedCC: 1300},
				{DriverID: 2, VehicleID: 2}: {ConvertedCC: 1300},
				{DriverID: 3, VehicleID: 3}: {ConvertedCC: 1300},
			},
			ptMode:  addMode,
			penalty: penalty,
			heat:    2,
			wantOrder: []ComboKey{
				{DriverID: 1, VehicleID: 1}, // only combo with a valid heat-2 run
				{DriverID: 2, VehicleID: 2}, // no heat-2 run -> tail, ComboKey(2,2) < (3,3)
				{DriverID: 3, VehicleID: 3}, // heat-2 run exists but is MC -> tail
			},
			check: func(t *testing.T, got []Standing) {
				a := got[0]
				if a.Invalid || a.BestMS == nil || *a.BestMS != 90000 {
					t.Fatalf("combo A (heat2) standing = %+v, want valid BestMS=90000 (its overall best of 70000 must be ignored)", a)
				}
				if a.SecondMS != nil {
					t.Errorf("heat mode must never populate SecondMS, got %v", *a.SecondMS)
				}
				if a.BestAtMS != 2000 {
					t.Errorf("A BestAtMS = %d, want 2000 (the heat-2 run's own timestamp)", a.BestAtMS)
				}
				if a.Runs != 2 || a.ValidRuns != 2 {
					t.Errorf("A Runs/ValidRuns = %d/%d, want 2/2 (totals are unaffected by heat filtering)", a.Runs, a.ValidRuns)
				}

				b := got[1]
				if !b.Invalid {
					t.Errorf("combo B (no heat-2 run) must be Invalid for heat=2, got %+v", b)
				}
				if b.Runs != 1 || b.ValidRuns != 1 {
					t.Errorf("B Runs/ValidRuns = %d/%d, want 1/1 (its only run is valid overall, just absent from heat 2)", b.Runs, b.ValidRuns)
				}

				c := got[2]
				if !c.Invalid {
					t.Errorf("combo C (heat-2 run is MC) must be Invalid for heat=2, got %+v", c)
				}
				if c.Runs != 2 || c.ValidRuns != 1 {
					t.Errorf("C Runs/ValidRuns = %d/%d, want 2/1 (heat1 valid, heat2 MC invalid)", c.Runs, c.ValidRuns)
				}
			},
		},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := Rank(c.runs, c.meta, c.ptMode, c.penalty, c.heat)
			if len(got) != len(c.wantOrder) {
				t.Fatalf("Rank() returned %d standings, want %d (got order=%v)", len(got), len(c.wantOrder), comboOrder(got))
			}
			for i, want := range c.wantOrder {
				if got[i].Combo != want {
					t.Errorf("position %d: combo = %v, want %v (full got order=%v, want order=%v)",
						i, got[i].Combo, want, comboOrder(got), c.wantOrder)
				}
			}
			if c.check != nil {
				c.check(t, got)
			}
		})
	}
}
