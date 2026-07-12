package snapshot

import (
	"encoding/json"

	"timemon/internal/domain"
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
}

// Ranking builds the current standings for every driver/vehicle combination
// that has at least one run, in exactly the order domain.Rank returns them
// (its full §4.3 tie-break order).
func (b *Builder) Ranking() ([]byte, error) {
	settings, _, err := b.s.GetSettings()
	if err != nil {
		return nil, err
	}
	runs, err := b.s.ListRuns()
	if err != nil {
		return nil, err
	}
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
	conv := buildVehicleConv(vehicles, settings.Coef, settings.DispClasses)

	meta := make(map[domain.ComboKey]domain.ComboMeta, len(runs))
	for _, r := range runs {
		if _, ok := meta[r.Combo]; ok {
			continue
		}
		c := conv[r.Combo.VehicleID]
		meta[r.Combo] = domain.ComboMeta{ConvertedCC: c.cc, IsEV: !c.ok}
	}

	standings := domain.Rank(runs, meta, settings.PTMode, settings.PTPenaltyMS, 0)

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
			Driver:      refDriver{ID: drv.ID, Name: drv.Name},
			DriverClass: driverClassLabel[drv.DriverClassID],
			Vehicle: rankVehicle{
				ID:          veh.ID,
				Number:      veh.Number,
				Name:        veh.Name,
				Engine:      string(veh.Engine),
				ConvertedCC: ccPtr,
				DispClass:   c.dispClass,
				DTClass:     dtClassLabel[veh.DrivetrainClassID],
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

	return json.Marshal(rankingResponse{Rows: rows})
}
