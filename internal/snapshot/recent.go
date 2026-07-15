package snapshot

import (
	"encoding/json"

	"timemon/internal/domain"
	"timemon/internal/store"
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

// buildRecent builds the most recent `limit` logs of ev's event
// (timestamp_ms descending), excluding deleted and not-yet-assigned
// (orphan) entries.
//
// Note: it fetches store.ListLogs(ev.ID, limit, 0) and filters is_deleted/
// unassigned rows out of that single page. If deleted or orphan rows are
// present among the most recent `limit` database rows, the result can
// contain fewer than `limit` items rather than backfilling from the next
// page — see the questions section of the implementation report.
func (b *Builder) buildRecent(ev store.EventRow, limit int) (recentResponse, error) {
	logs, _, err := b.s.ListLogs(ev.ID, limit, 0)
	if err != nil {
		return recentResponse{}, err
	}
	drivers, err := b.s.ListDrivers()
	if err != nil {
		return recentResponse{}, err
	}
	vehicles, err := b.s.ListVehicles()
	if err != nil {
		return recentResponse{}, err
	}
	driverByID := indexDrivers(drivers)
	vehicleByID := indexVehicles(vehicles)

	runs, err := b.s.ListRuns(ev.ID)
	if err != nil {
		return recentResponse{}, err
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
		fms, invalid := domain.FinalMS(l.RawMS, l.PTCount, l.IsMC, ev.PTMode, ev.PTPenaltyMS)

		items = append(items, recentItem{
			LogID:       l.ID,
			Driver:      refDriver{ID: drv.ID, Name: drv.Name, HasIcon: drv.HasIcon},
			Vehicle:     refVehicleBasic{ID: veh.ID, Number: veh.Number, Name: veh.Name, HasIcon: veh.HasIcon},
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
	return recentResponse{Items: items}, nil
}

// Recent builds the "most recent logs" snapshot for the current active
// event. With no active event, this is an empty list.
func (b *Builder) Recent(limit int) ([]byte, error) {
	ev, ok, err := b.s.GetActiveEvent()
	if err != nil {
		return nil, err
	}
	if !ok {
		return json.Marshal(recentResponse{Items: []recentItem{}})
	}
	resp, err := b.buildRecent(ev, limit)
	if err != nil {
		return nil, err
	}
	return json.Marshal(resp)
}

// RecentPayloadFor builds the "most recent logs" payload for a specific
// event id (active or archived/closed) rather than the currently active
// one — for stage-2 archive APIs. Returns an empty-items payload if
// eventID does not exist.
func (b *Builder) RecentPayloadFor(eventID int64, limit int) (any, error) {
	ev, ok, err := b.s.GetEvent(eventID)
	if err != nil {
		return nil, err
	}
	if !ok {
		return recentResponse{Items: []recentItem{}}, nil
	}
	return b.buildRecent(ev, limit)
}
