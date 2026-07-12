package snapshot

import "encoding/json"

type onCourseResponse struct {
	Cars []onCourseCar `json:"cars"`
}

type onCourseCar struct {
	QueueID  int64           `json:"queue_id"`
	Driver   refDriver       `json:"driver"`
	Vehicle  refVehicleBasic `json:"vehicle"`
	TStartUS *int64          `json:"t_start_us"`
	PTCount  int             `json:"pt_count"`
	MCFlag   bool            `json:"mc_flag"`
	Finish   interface{}     `json:"finish"`
}

// OnCourse builds the snapshot of cars currently running, ordered by queue
// id (the same order store.ListQueue("on_course") returns). finish is
// always null here; it is reserved for the timing extension planned for
// workstream W3.
func (b *Builder) OnCourse() ([]byte, error) {
	rows, err := b.s.ListQueue("on_course")
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

	cars := make([]onCourseCar, 0, len(rows))
	for _, r := range rows {
		drv, ok := driverByID[r.DriverID]
		if !ok {
			continue
		}
		veh, ok := vehicleByID[r.VehicleID]
		if !ok {
			continue
		}
		cars = append(cars, onCourseCar{
			QueueID:  r.ID,
			Driver:   refDriver{ID: drv.ID, Name: drv.Name},
			Vehicle:  refVehicleBasic{ID: veh.ID, Number: veh.Number, Name: veh.Name},
			TStartUS: r.TStartUS,
			PTCount:  r.PTCount,
			MCFlag:   r.MCFlag,
			Finish:   nil,
		})
	}
	return json.Marshal(onCourseResponse{Cars: cars})
}
