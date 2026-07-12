// Package timing receives UDP trigger/heartbeat packets from the gymkhana
// start/goal sensors (ESP32 devices), records trigger events in the store,
// pairs triggers with cars on course via a CourseController, and reports
// sensor health (sensor_status) and orphaned triggers to the caller through
// callbacks.
//
// Wiring to the rest of the application (course manager, SSE hub, etc.) is
// done by the caller (main.go); this package deliberately only knows about
// internal/store and the standard library.
package timing

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"net"
	"sync"
	"time"

	"timemon/internal/store"
)

// ErrNoTarget is returned by CourseController methods when there is no car
// waiting to be paired with the incoming sensor trigger. Callers of this
// package should detect it with errors.Is(err, timing.ErrNoTarget);
// implementations of CourseController must return exactly this sentinel
// (or wrap it) for that case.
var ErrNoTarget = errors.New("timing: no pairing target")

// CourseController applies paired sensor timestamps to cars on course. It is
// implemented by the web package's course manager; this package only depends
// on the interface below.
type CourseController interface {
	// SensorStart stamps tUS (the sensor's timestamp_us) as the start time
	// of the oldest on_course car still waiting for its start stamp.
	// Returns ErrNoTarget if no car is waiting.
	SensorStart(tUS int64) error

	// SensorGoal finalizes the oldest RUNNING car using tUS (the sensor's
	// timestamp_us) as its goal time. Returns ErrNoTarget if no car is
	// running.
	SensorGoal(tUS int64) error
}

// Deps holds the collaborators Listen needs. Store, Course, OnOrphan and
// OnSensorStatus are wired in by the caller (main.go); OnOrphan/OnSensorStatus
// may be left nil if the caller doesn't care about that particular signal.
type Deps struct {
	Store  *store.Store
	Course CourseController

	// OnOrphan is called when a trigger arrives with no pairing target.
	// kind is "orphan_start" or "orphan_goal"; the web package is expected
	// to forward it to the SSE orphan topic.
	OnOrphan func(kind, detail string)

	// OnSensorStatus is called with the sensor_status JSON payload every
	// StatusInterval. The web package is expected to publish it verbatim to
	// the sensor_status topic.
	OnSensorStatus func(data []byte)

	// StatusInterval sets how often sensor_status is emitted. Zero (the
	// typical production value) means the default of 5 seconds. Tests may
	// set this to a short duration to avoid slow tests.
	StatusInterval time.Duration

	// boundAddr, when non-nil, receives the UDP socket's actual local
	// address once Listen has bound (useful with a ":0" addr). Unexported
	// on purpose: it is an in-package test seam, not part of the public
	// wiring surface.
	boundAddr chan<- net.Addr
}

const defaultStatusInterval = 5 * time.Second

// packet is the shared decode target for both wire formats; irrelevant
// fields are simply left at their zero value depending on Type.
//
//	{"type":"trigger","sensor_id":"start","boot_id":123456789,"seq":1,"timestamp_us":1720000000000000}
//	{"type":"hb","sensor_id":"goal","boot_id":123456789,"seq":42,"ntp_offset_ms":0.4}
type packet struct {
	Type        string  `json:"type"`
	SensorID    string  `json:"sensor_id"`
	BootID      int64   `json:"boot_id"`
	Seq         int64   `json:"seq"`
	TimestampUS int64   `json:"timestamp_us"`
	NTPOffsetMS float64 `json:"ntp_offset_ms"`
}

func validSensorID(id string) bool {
	return id == "start" || id == "goal"
}

// Listen receives UDP packets on addr (e.g. ":9999") until ctx is done, then
// closes the socket and returns nil. It returns a non-nil error only if the
// initial bind fails, or if reading from the socket fails for a reason other
// than ctx cancellation.
//
// The goroutine running the receive loop (this call) only parses and
// validates packets; the actual trigger/heartbeat handling is serialized on
// a single internal worker goroutine fed through a channel, so pairing order
// is preserved regardless of UDP delivery timing.
func Listen(ctx context.Context, addr string, deps Deps) error {
	conn, err := net.ListenPacket("udp", addr)
	if err != nil {
		return fmt.Errorf("timing: listen %s: %w", addr, err)
	}
	defer conn.Close()

	if deps.boundAddr != nil {
		deps.boundAddr <- conn.LocalAddr()
	}

	statusInterval := deps.StatusInterval
	if statusInterval <= 0 {
		statusInterval = defaultStatusInterval
	}

	d := &dispatcher{deps: deps, sensors: make(map[string]*sensorState)}

	work := make(chan packet, 64)
	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		d.run(work, statusInterval)
	}()

	// conn.Close() is how we unblock the ReadFrom loop below on shutdown;
	// stopWatch lets us retire this goroutine if Listen exits for some other
	// reason (a genuine read error) before ctx is ever cancelled.
	stopWatch := make(chan struct{})
	go func() {
		select {
		case <-ctx.Done():
			conn.Close()
		case <-stopWatch:
		}
	}()

	var retErr error
	buf := make([]byte, 2048)
	for {
		n, _, readErr := conn.ReadFrom(buf)
		if readErr != nil {
			select {
			case <-ctx.Done():
				// Expected: conn.Close() from the watcher goroutine above.
			default:
				retErr = fmt.Errorf("timing: udp read: %w", readErr)
			}
			break
		}

		var p packet
		if err := json.Unmarshal(buf[:n], &p); err != nil {
			log.Printf("timing: dropping invalid json packet: %v", err)
			continue
		}
		if p.Type != "trigger" && p.Type != "hb" {
			log.Printf("timing: dropping packet with unknown type %q", p.Type)
			continue
		}
		if !validSensorID(p.SensorID) {
			log.Printf("timing: dropping packet with unknown sensor_id %q", p.SensorID)
			continue
		}

		work <- p
	}

	close(stopWatch)
	close(work)
	wg.Wait()
	return retErr
}

// dispatcher owns all mutable pairing/heartbeat state. Every field is only
// ever touched from within run's goroutine (both the work-channel branch and
// the ticker branch), so no locking is required.
type dispatcher struct {
	deps    Deps
	sensors map[string]*sensorState
}

func (d *dispatcher) run(work <-chan packet, statusInterval time.Duration) {
	ticker := time.NewTicker(statusInterval)
	defer ticker.Stop()

	for {
		select {
		case p, ok := <-work:
			if !ok {
				return
			}
			d.handle(p)
		case <-ticker.C:
			d.emitStatus()
		}
	}
}

func (d *dispatcher) handle(p packet) {
	switch p.Type {
	case "trigger":
		d.handleTrigger(p)
	case "hb":
		d.handleHeartbeat(p)
	}
}

const (
	orphanKindStart = "orphan_start"
	orphanKindGoal  = "orphan_goal"
)

func (d *dispatcher) handleTrigger(p packet) {
	if d.deps.Store == nil {
		log.Printf("timing: no store configured, dropping %s trigger", p.SensorID)
		return
	}

	receivedAtMS := time.Now().UnixMilli()
	inserted, err := d.deps.Store.InsertSensorEvent(p.SensorID, p.BootID, p.Seq, p.TimestampUS, receivedAtMS)
	if err != nil {
		log.Printf("timing: store insert failed for %s trigger (boot_id=%d seq=%d): %v", p.SensorID, p.BootID, p.Seq, err)
		return
	}
	if !inserted {
		// Duplicate delivery: the 2nd/3rd copy of the ESP32's burst-of-3
		// resend for the same (sensor_id, boot_id, seq). Silently ignore.
		return
	}

	if d.deps.Course == nil {
		log.Printf("timing: no course controller configured, dropping %s trigger", p.SensorID)
		return
	}

	var pairErr error
	var orphanKind string
	switch p.SensorID {
	case "start":
		pairErr = d.deps.Course.SensorStart(p.TimestampUS)
		orphanKind = orphanKindStart
	case "goal":
		pairErr = d.deps.Course.SensorGoal(p.TimestampUS)
		orphanKind = orphanKindGoal
	}

	switch {
	case pairErr == nil:
		return
	case errors.Is(pairErr, ErrNoTarget):
		if d.deps.OnOrphan != nil {
			d.deps.OnOrphan(orphanKind, fmt.Sprintf("%s trigger with no waiting car (ts=%d)", p.SensorID, p.TimestampUS))
		}
	default:
		log.Printf("timing: %s pairing error (ts=%d): %v", p.SensorID, p.TimestampUS, pairErr)
	}
}
