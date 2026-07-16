package snapshot

import "encoding/json"

type queueResponse struct {
	Items []queueItem `json:"items"`
}

type queueItem struct {
	QueueID  int64           `json:"queue_id"`
	Driver   refDriver       `json:"driver"`
	Vehicle  refVehicleBasic `json:"vehicle"`
	Position float64         `json:"position"` // admin drag-reorder computes midpoints from this
}

// Queue builds the waiting-line snapshot for the current active event,
// ordered by queue position (the same order store.ListQueue(eventID,
// "waiting") returns). With no active event, this is an empty list.
func (b *Builder) Queue() ([]byte, error) {
	ev, ok, err := b.s.GetActiveEvent()
	if err != nil {
		return nil, err
	}
	if !ok {
		return json.Marshal(queueResponse{Items: []queueItem{}})
	}

	rows, err := b.s.ListQueue(ev.ID, "waiting")
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

	items := make([]queueItem, 0, len(rows))
	for _, r := range rows {
		drv, ok := driverByID[r.DriverID]
		if !ok {
			continue
		}
		veh, ok := vehicleByID[r.VehicleID]
		if !ok {
			continue
		}
		items = append(items, queueItem{
			QueueID:  r.ID,
			Driver:   newRefDriver(drv),
			Vehicle:  newRefVehicle(veh),
			Position: r.Position,
		})
	}
	return json.Marshal(queueResponse{Items: items})
}
