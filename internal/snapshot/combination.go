package snapshot

import (
	"encoding/json"
	"net/url"
	"sort"
	"strconv"

	"timemon/internal/domain"
	"timemon/internal/store"
)

type combinationLogsResponse struct {
	Driver  refDriver       `json:"driver"`
	Vehicle refVehicleBasic `json:"vehicle"`
	Runs    []comboRunItem  `json:"runs"`
}

type comboRunItem struct {
	Heat         int    `json:"heat"`
	RawMS        int    `json:"raw_ms"`
	PTCount      int    `json:"pt_count"`
	IsMC         bool   `json:"is_mc"`
	FinalMS      int    `json:"final_ms"`
	Invalid      bool   `json:"invalid"`
	RankInFilter *int   `json:"rank_in_filter"`
	TimestampMS  int64  `json:"timestamp_ms"`
	Source       string `json:"source"`
}

// allLogs fetches every LogRow belonging to eventID. ListLogs is
// limit/offset paginated with no combo filter, so this asks for the total
// count first and then re-requests exactly that many rows. It is only
// needed for the "source" field: domain.RunRow (what ListRunsByCombo
// returns) does not carry it, so it must be joined back from LogRow by log
// id.
func (b *Builder) allLogs(eventID int64) ([]store.LogRow, error) {
	_, total, err := b.s.ListLogs(eventID, 1, 0)
	if err != nil {
		return nil, err
	}
	if total <= 0 {
		return nil, nil
	}
	all, _, err := b.s.ListLogs(eventID, total, 0)
	if err != nil {
		return nil, err
	}
	return all, nil
}

// CombinationLogs lists every run for one driver/vehicle combination, in
// heat order, together with each valid run's rank_in_filter: its 1-based
// position by final_ms among every valid run of every combination matching
// q's filters (ties share a rank; the next distinct time skips ranks
// accordingly, e.g. 1,1,3).
//
// Recognized q keys: class_driver (driver class label), drivetrain (vehicle
// drivetrain class label), disp (displacement class label, including
// "EV"), driver_id, vehicle_id, heat (heat number). All present filters
// are ANDed together; class_driver/drivetrain/disp/driver_id/vehicle_id
// restrict which combinations are pooled, heat additionally restricts
// which runs of those combinations are pooled (only runs sharing that heat
// number). Unparseable driver_id/vehicle_id/heat values are treated as
// absent. Invalid runs (as determined by domain.FinalMS) are excluded from
// the population and always report rank_in_filter: null.
func (b *Builder) CombinationLogs(driverID, vehicleID int64, q url.Values) ([]byte, error) {
	ev, activeOK, err := b.s.GetActiveEvent()
	if err != nil {
		return nil, err
	}
	return b.combinationLogsFor(ev, activeOK, driverID, vehicleID, q)
}

// CombinationLogsFor is CombinationLogs scoped to a specific event id
// (active or archived/closed) rather than the currently active one — for
// stage-2 archive/export use. Returns a zero-runs payload if eventID does
// not exist.
func (b *Builder) CombinationLogsFor(eventID, driverID, vehicleID int64, q url.Values) ([]byte, error) {
	ev, ok, err := b.s.GetEvent(eventID)
	if err != nil {
		return nil, err
	}
	return b.combinationLogsFor(ev, ok, driverID, vehicleID, q)
}

func (b *Builder) combinationLogsFor(ev store.EventRow, activeOK bool, driverID, vehicleID int64, q url.Values) ([]byte, error) {
	drivers, err := b.s.ListDrivers()
	if err != nil {
		return nil, err
	}
	vehicles, err := b.s.ListVehicles()
	if err != nil {
		return nil, err
	}
	driverClasses, err := b.s.ListClassDefs("driver")
	if err != nil {
		return nil, err
	}
	dtClasses, err := b.s.ListClassDefs("drivetrain")
	if err != nil {
		return nil, err
	}

	driverByID := indexDrivers(drivers)
	vehicleByID := indexVehicles(vehicles)
	driverClassLabel := indexClassLabels(driverClasses)
	dtClassLabel := indexClassLabels(dtClasses)
	conv := buildVehicleConv(vehicles, ev.Coef, ev.DispClasses)

	// With no active event there are no runs/logs to scope to; every list
	// below stays empty and the response reports zero runs for the combo.
	var allRuns []domain.RunRow
	var logs []store.LogRow
	var comboRuns []domain.RunRow
	if activeOK {
		allRuns, err = b.s.ListRuns(ev.ID)
		if err != nil {
			return nil, err
		}
		logs, err = b.allLogs(ev.ID)
		if err != nil {
			return nil, err
		}
		comboRuns, err = b.s.ListRunsByCombo(ev.ID, driverID, vehicleID)
		if err != nil {
			return nil, err
		}
	}
	heatByLogID := domain.HeatNumbers(allRuns)

	sourceByLogID := make(map[int64]string, len(logs))
	for _, l := range logs {
		sourceByLogID[l.ID] = l.Source
	}

	comboAttrs := func(d, v int64) (driverClass, dt, disp string, ok bool) {
		drv, ok1 := driverByID[d]
		veh, ok2 := vehicleByID[v]
		if !ok1 || !ok2 {
			return "", "", "", false
		}
		return driverClassLabel[drv.DriverClassID], dtClassLabel[veh.DrivetrainClassID], conv[veh.ID].dispClass, true
	}

	fClassDriver := q.Get("class_driver")
	fDrivetrain := q.Get("drivetrain")
	fDisp := q.Get("disp")
	fDriverID, hasDriverIDFilter := parseFilterInt64(q, "driver_id")
	fVehicleID, hasVehicleIDFilter := parseFilterInt64(q, "vehicle_id")
	fHeat, hasHeatFilter := parseFilterInt(q, "heat")

	comboMatches := func(d, v int64) bool {
		dc, dt, disp, ok := comboAttrs(d, v)
		if !ok {
			return false
		}
		if fClassDriver != "" && dc != fClassDriver {
			return false
		}
		if fDrivetrain != "" && dt != fDrivetrain {
			return false
		}
		if fDisp != "" && disp != fDisp {
			return false
		}
		if hasDriverIDFilter && d != fDriverID {
			return false
		}
		if hasVehicleIDFilter && v != fVehicleID {
			return false
		}
		return true
	}

	var population []int
	for _, r := range allRuns {
		if !comboMatches(r.Combo.DriverID, r.Combo.VehicleID) {
			continue
		}
		if hasHeatFilter && heatByLogID[r.LogID] != fHeat {
			continue
		}
		fms, invalid := domain.FinalMS(r.RawMS, r.PTCount, r.IsMC, ev.PTMode, ev.PTPenaltyMS)
		if invalid {
			continue
		}
		population = append(population, fms)
	}
	sort.Ints(population)

	rankOf := func(fms int) int {
		// population is sorted ascending; the count of entries strictly
		// less than fms is exactly the insertion point of fms.
		return sort.SearchInts(population, fms) + 1
	}

	sort.Slice(comboRuns, func(i, j int) bool {
		return heatByLogID[comboRuns[i].LogID] < heatByLogID[comboRuns[j].LogID]
	})

	runs := make([]comboRunItem, 0, len(comboRuns))
	for _, r := range comboRuns {
		fms, invalid := domain.FinalMS(r.RawMS, r.PTCount, r.IsMC, ev.PTMode, ev.PTPenaltyMS)
		var rank *int
		if !invalid {
			v := rankOf(fms)
			rank = &v
		}
		runs = append(runs, comboRunItem{
			Heat:         heatByLogID[r.LogID],
			RawMS:        r.RawMS,
			PTCount:      r.PTCount,
			IsMC:         r.IsMC,
			FinalMS:      fms,
			Invalid:      invalid,
			RankInFilter: rank,
			TimestampMS:  r.TimestampMS,
			Source:       sourceByLogID[r.LogID],
		})
	}

	resp := combinationLogsResponse{Runs: runs}
	if drv, ok := driverByID[driverID]; ok {
		resp.Driver = newRefDriver(drv)
	} else {
		resp.Driver = refDriver{ID: driverID}
	}
	if veh, ok := vehicleByID[vehicleID]; ok {
		resp.Vehicle = newRefVehicle(veh)
	} else {
		resp.Vehicle = refVehicleBasic{ID: vehicleID}
	}

	return json.Marshal(resp)
}

func parseFilterInt64(q url.Values, key string) (int64, bool) {
	s := q.Get(key)
	if s == "" {
		return 0, false
	}
	v, err := strconv.ParseInt(s, 10, 64)
	if err != nil {
		return 0, false
	}
	return v, true
}

func parseFilterInt(q url.Values, key string) (int, bool) {
	s := q.Get(key)
	if s == "" {
		return 0, false
	}
	v, err := strconv.Atoi(s)
	if err != nil {
		return 0, false
	}
	return v, true
}
