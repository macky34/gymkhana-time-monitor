package snapshot

import "encoding/json"

type queueResponse struct {
	Items []queueItem `json:"items"`
}

type queueItem struct {
	QueueID int64           `json:"queue_id"`
	Driver  refDriver       `json:"driver"`
	Vehicle refVehicleBasic `json:"vehicle"`
}

// Queue builds the waiting-line snapshot, ordered by queue position (the
// same order store.ListQueue("waiting") returns).
func (b *Builder) Queue() ([]byte, error) {
	rows, err := b.s.ListQueue("waiting")
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
			QueueID: r.ID,
			Driver:  refDriver{ID: drv.ID, Name: drv.Name},
			Vehicle: refVehicleBasic{ID: veh.ID, Number: veh.Number, Name: veh.Name},
		})
	}
	return json.Marshal(queueResponse{Items: items})
}
