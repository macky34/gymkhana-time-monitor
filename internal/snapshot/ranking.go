package snapshot

import (
	"encoding/json"

	"timemon/internal/domain"
	"timemon/internal/store"
)

type rankingResponse struct {
	Rows []rankingRow `json:"rows"`
}

type rankingRow struct {
	Driver      refDriver   `json:"driver"`
	DriverClass string      `json:"driver_class"`
	Vehicle     rankVehicle `json:"vehicle"`
	BestMS      *int        `json:"best_ms"`
	SecondMS    *int        `json:"second_ms"`
	BestLogID   *int64      `json:"best_log_id"`
	Runs        int         `json:"runs"`
	ValidRuns   int         `json:"valid_runs"`
	PTTotal     int         `json:"pt_total"`
	Invalid     bool        `json:"invalid"`
}

type rankVehicle struct {
	ID          int64  `json:"id"`
	Number      int    `json:"number"`
	Name        string `json:"name"`
	Engine      string `json:"engine"`
	ConvertedCC *int   `json:"converted_cc"`
	DispClass   string `json:"disp_class"`
	DTClass     string `json:"dt_class"`
	HasIcon     bool   `json:"has_icon"`
}

// buildRanking builds the current standings for ev's event, in exactly the
// order domain.Rank returns them (its full §4.3 tie-break order).
func (b *Builder) buildRanking(ev store.EventRow) (rankingResponse, error) {
	runs, err := b.s.ListRuns(ev.ID)
	if err != nil {
		return rankingResponse{}, err
	}
	drivers, err := b.s.ListDrivers()
	if err != nil {
		return rankingResponse{}, err
	}
	vehicles, err := b.s.ListVehicles()
	if err != nil {
		return rankingResponse{}, err
	}
	driverClasses, err := b.s.ListClassDefs("driver")
	if err != nil {
		return rankingResponse{}, err
	}
	dtClasses, err := b.s.ListClassDefs("drivetrain")
	if err != nil {
		return rankingResponse{}, err
	}

	driverByID := indexDrivers(drivers)
	vehicleByID := indexVehicles(vehicles)
	driverClassLabel := indexClassLabels(driverClasses)
	dtClassLabel := indexClassLabels(dtClasses)
	conv := buildVehicleConv(vehicles, ev.Coef, ev.DispClasses)

	meta := make(map[domain.ComboKey]domain.ComboMeta, len(runs))
	for _, r := range runs {
		if _, ok := meta[r.Combo]; ok {
			continue
		}
		c := conv[r.Combo.VehicleID]
		meta[r.Combo] = domain.ComboMeta{ConvertedCC: c.cc, IsEV: !c.ok}
	}

	standings := domain.Rank(runs, meta, ev.PTMode, ev.PTPenaltyMS, 0)

	rows := make([]rankingRow, 0, len(standings))
	for _, st := range standings {
		drv, ok := driverByID[st.Combo.DriverID]
		if !ok {
			continue
		}
		veh, ok := vehicleByID[st.Combo.VehicleID]
		if !ok {
			continue
		}
		c := conv[veh.ID]
		var ccPtr *int
		if c.ok {
			cc := c.cc
			ccPtr = &cc
		}
		rows = append(rows, rankingRow{
			Driver:      newRefDriver(drv),
			DriverClass: driverClassLabel[drv.DriverClassID],
			Vehicle: rankVehicle{
				ID:          veh.ID,
				Number:      veh.Number,
				Name:        veh.Name,
				Engine:      string(veh.Engine),
				ConvertedCC: ccPtr,
				DispClass:   c.dispClass,
				DTClass:     dtClassLabel[veh.DrivetrainClassID],
				HasIcon:     veh.HasIcon,
			},
			BestMS:    st.BestMS,
			SecondMS:  st.SecondMS,
			BestLogID: st.BestLogID,
			Runs:      st.Runs,
			ValidRuns: st.ValidRuns,
			PTTotal:   st.PTTotal,
			Invalid:   st.Invalid,
		})
	}

	return rankingResponse{Rows: rows}, nil
}

// Ranking builds the ranking snapshot for the current active event. With no
// active event, this is an empty list.
func (b *Builder) Ranking() ([]byte, error) {
	ev, ok, err := b.s.GetActiveEvent()
	if err != nil {
		return nil, err
	}
	if !ok {
		return json.Marshal(rankingResponse{Rows: []rankingRow{}})
	}
	resp, err := b.buildRanking(ev)
	if err != nil {
		return nil, err
	}
	return json.Marshal(resp)
}

// RankingPayloadFor builds the ranking payload for a specific event id
// (active or archived/closed) rather than the currently active one — for
// stage-2 archive APIs. Returns an empty-rows payload if eventID does not
// exist.
func (b *Builder) RankingPayloadFor(eventID int64) (any, error) {
	ev, ok, err := b.s.GetEvent(eventID)
	if err != nil {
		return nil, err
	}
	if !ok {
		return rankingResponse{Rows: []rankingRow{}}, nil
	}
	return b.buildRanking(ev)
}
