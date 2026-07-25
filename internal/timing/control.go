package timing

import (
	"errors"
	"time"
)

// DefaultUnresponsiveAfter is how long a sensor may go without a heartbeat
// before ForgetSensor is willing to drop it. It matches the "無応答" (age >
// 15s) threshold the admin UI uses to decide when to show the delete button
// (web/templates/admin.html renderSensors), so the server-side check never
// rejects a request the UI itself would not have offered.
const DefaultUnresponsiveAfter = 15 * time.Second

var (
	// ErrSensorUnknown is returned by ForgetSensor when the given sensor_id
	// has never been seen (or was already forgotten).
	ErrSensorUnknown = errors.New("timing: unknown sensor")

	// ErrSensorAlive is returned by ForgetSensor when the sensor has reported
	// within the unresponsive-after window, refusing to drop a sensor that
	// is still actively sending heartbeats.
	ErrSensorAlive = errors.New("timing: sensor still responding")

	// ErrControlUnavailable is returned when no dispatcher is running to
	// service the request (Listen not yet started, or already returned).
	ErrControlUnavailable = errors.New("timing: dispatcher not running")
)

// controlTimeout bounds how long ForgetSensor waits for the dispatcher's
// run loop to pick up and answer a request, so a stalled/absent dispatcher
// fails fast as ErrControlUnavailable instead of hanging the caller (an
// admin HTTP handler) forever.
const controlTimeout = 2 * time.Second

// forgetReq is a request to drop a sensor's in-memory heartbeat/health
// state, answered on reply by dispatcher.run's goroutine.
type forgetReq struct {
	sensorID string
	reply    chan error
}

// Control lets callers outside the dispatcher's own goroutine (namely admin
// HTTP handlers) safely request state changes. dispatcher.sensors is only
// ever touched from within dispatcher.run, so every operation here is
// modeled as a request sent over a channel and served by that goroutine's
// select loop, rather than by taking a lock.
type Control struct {
	forget chan forgetReq
}

// NewControl creates a Control ready to be passed to Deps.Control and
// wired into Listen before the dispatcher starts.
func NewControl() *Control {
	return &Control{forget: make(chan forgetReq, 4)}
}

// ForgetSensor asks the dispatcher to drop sensorID's in-memory heartbeat
// state (as if it had never reported), causing it to disappear from the
// next sensor_status payload until it reports again. It refuses (returning
// ErrSensorAlive) if the sensor has reported within
// Deps.UnresponsiveAfter (DefaultUnresponsiveAfter if unset), and returns
// ErrSensorUnknown if the sensor has never been seen.
func (c *Control) ForgetSensor(sensorID string) error {
	if c == nil {
		return ErrControlUnavailable
	}
	reply := make(chan error, 1)
	select {
	case c.forget <- forgetReq{sensorID: sensorID, reply: reply}:
	case <-time.After(controlTimeout):
		return ErrControlUnavailable
	}
	select {
	case err := <-reply:
		return err
	case <-time.After(controlTimeout):
		return ErrControlUnavailable
	}
}

// ValidSensorID reports whether id is a recognized sensor_id ("start" or
// "goal"). Exported so callers outside this package (the admin HTTP
// handler) can validate a path parameter before calling ForgetSensor.
func ValidSensorID(id string) bool {
	return validSensorID(id)
}
