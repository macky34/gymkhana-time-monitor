package snapshot

import "encoding/json"

type onCourseResponse struct {
	Cars []onCourseCar `json:"cars"`
}

// onCourseFinish is the finish object embedded in an on-course car while its
// finish is inside the confirmation grace window (W3 timing).
type onCourseFinish struct {
	FinMS   int   `json:"fin_ms"`
	UntilMS int64 `json:"until_ms"`
}

type onCourseCar struct {
	QueueID  int64           `json:"queue_id"`
	Driver   refDriver       `json:"driver"`
	Vehicle  refVehicleBasic `json:"vehicle"`
	TStartUS *int64          `json:"t_start_us"`
	PTCount  int             `json:"pt_count"`
	MCFlag   bool            `json:"mc_flag"`
	Finish   *onCourseFinish `json:"finish"`
}

// finishFor resolves the finish field for one car: the FinishProvider's
// answer when one is registered (SetFinishProvider) and reports an in-flight
// finish, or nil (JSON null) otherwise.
func (b *Builder) finishFor(queueID int64) *onCourseFinish {
	if b.finishFn == nil {
		return nil
	}
	finMS, untilMS, ok := b.finishFn(queueID)
	if !ok {
		return nil
	}
	return &onCourseFinish{FinMS: finMS, UntilMS: untilMS}
}

// OnCourse builds the snapshot of cars currently running in the active
// event, ordered by queue id (the same order store.ListQueue(eventID,
// "on_course") returns). A car whose finish is still inside the
// confirmation grace window carries a non-null "finish" object (via the
// registered FinishProvider); all others report null. With no active
// event, this is an empty list.
func (b *Builder) OnCourse() ([]byte, error) {
	ev, ok, err := b.s.GetActiveEvent()
	if err != nil {
		return nil, err
	}
	if !ok {
		return json.Marshal(onCourseResponse{Cars: []onCourseCar{}})
	}

	rows, err := b.s.ListQueue(ev.ID, "on_course")
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
			Driver:   newRefDriver(drv),
			Vehicle:  newRefVehicle(veh),
			TStartUS: r.TStartUS,
			PTCount:  r.PTCount,
			MCFlag:   r.MCFlag,
			Finish:   b.finishFor(r.ID),
		})
	}
	return json.Marshal(onCourseResponse{Cars: cars})
}
