package snapshot

import (
	"encoding/json"

	"timemon/internal/domain"
)

type recentResponse struct {
	Items []recentItem `json:"items"`
}

type recentItem struct {
	LogID       int64           `json:"log_id"`
	Driver      refDriver       `json:"driver"`
	Vehicle     refVehicleBasic `json:"vehicle"`
	Heat        int             `json:"heat"`
	RawMS       int             `json:"raw_ms"`
	PTCount     int             `json:"pt_count"`
	IsMC        bool            `json:"is_mc"`
	FinalMS     int             `json:"final_ms"`
	Invalid     bool            `json:"invalid"`
	Source      string          `json:"source"`
	TimestampMS int64           `json:"timestamp_ms"`
}

// Recent builds the most recent `limit` logs (timestamp_ms descending),
// excluding deleted and not-yet-assigned (orphan) entries.
//
// Note: it fetches store.ListLogs(limit, 0) and filters is_deleted/
// unassigned rows out of that single page. If deleted or orphan rows are
// present among the most recent `limit` database rows, the result can
// contain fewer than `limit` items rather than backfilling from the next
// page — see the questions section of the implementation report.
func (b *Builder) Recent(limit int) ([]byte, error) {
	settings, _, err := b.s.GetSettings()
	if err != nil {
		return nil, err
	}
	logs, _, err := b.s.ListLogs(limit, 0)
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
	driverByID := indexDrivers(drivers)
	vehicleByID := indexVehicles(vehicles)

	runs, err := b.s.ListRuns()
	if err != nil {
		return nil, err
	}
	heatByLogID := domain.HeatNumbers(runs)

	items := make([]recentItem, 0, len(logs))
	for _, l := range logs {
		if l.IsDeleted || l.DriverID == nil || l.VehicleID == nil {
			continue
		}
		drv, ok := driverByID[*l.DriverID]
		if !ok {
			continue
		}
		veh, ok := vehicleByID[*l.VehicleID]
		if !ok {
			continue
		}
		fms, invalid := domain.FinalMS(l.RawMS, l.PTCount, l.IsMC, settings.PTMode, settings.PTPenaltyMS)

		items = append(items, recentItem{
			LogID:       l.ID,
			Driver:      refDriver{ID: drv.ID, Name: drv.Name},
			Vehicle:     refVehicleBasic{ID: veh.ID, Number: veh.Number, Name: veh.Name},
			Heat:        heatByLogID[l.ID],
			RawMS:       l.RawMS,
			PTCount:     l.PTCount,
			IsMC:        l.IsMC,
			FinalMS:     fms,
			Invalid:     invalid,
			Source:      l.Source,
			TimestampMS: l.TimestampMS,
		})
	}
	return json.Marshal(recentResponse{Items: items})
}
