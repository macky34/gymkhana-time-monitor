package timing

import (
	"encoding/json"
	"log"
	"time"
)

// hbWindow is the number of most recent heartbeat sequence numbers kept per
// sensor (per boot_id) for computing loss_rate.
const hbWindow = 100

// sensorState is the in-memory heartbeat/health state tracked per sensor_id
// ("start" or "goal"). Only ever touched from dispatcher.run's goroutine, so
// it needs no locking of its own.
type sensorState struct {
	lastSeenMS  int64
	ntpOffsetMS float64

	haveBoot bool
	bootID   int64
	seqs     []int64 // recent seq numbers this boot_id, oldest first, capped at hbWindow
}

func (s *sensorState) recordHeartbeat(bootID, seq int64, ntpOffsetMS float64, nowMS int64) {
	s.lastSeenMS = nowMS
	s.ntpOffsetMS = ntpOffsetMS

	if !s.haveBoot || bootID != s.bootID {
		s.haveBoot = true
		s.bootID = bootID
		s.seqs = s.seqs[:0]
	}

	s.seqs = append(s.seqs, seq)
	if len(s.seqs) > hbWindow {
		s.seqs = s.seqs[len(s.seqs)-hbWindow:]
	}
}

// lossRate reports the fraction of heartbeats missing from the current
// window: expected = maxSeq-minSeq+1 over the window, loss = 1 -
// received/expected. Returns 0 for an empty window or when the window holds
// no gaps (including the degenerate case of duplicate/out-of-order seqs).
func (s *sensorState) lossRate() float64 {
	if len(s.seqs) == 0 {
		return 0
	}
	minSeq, maxSeq := s.seqs[0], s.seqs[0]
	for _, sq := range s.seqs[1:] {
		if sq < minSeq {
			minSeq = sq
		}
		if sq > maxSeq {
			maxSeq = sq
		}
	}
	expected := maxSeq - minSeq + 1
	if expected <= 0 {
		return 0
	}
	received := int64(len(s.seqs))
	if received >= expected {
		return 0
	}
	return 1 - float64(received)/float64(expected)
}

func (d *dispatcher) handleHeartbeat(p packet) {
	s := d.sensors[p.SensorID]
	if s == nil {
		s = &sensorState{}
		d.sensors[p.SensorID] = s
	}
	s.recordHeartbeat(p.BootID, p.Seq, p.NTPOffsetMS, time.Now().UnixMilli())
	// hb packets are never persisted via InsertSensorEvent; only triggers are.
}

// sensorStatusEntry and sensorStatusPayload define the exact JSON shape of
// the sensor_status payload delivered to Deps.OnSensorStatus:
//
//	{"sensors":[{"sensor_id":"start","last_seen_ms":...,"loss_rate":0.021,"ntp_offset_ms":0.4}, ...]}
//
// Sensors that have not been received yet are omitted entirely (no
// placeholder element).
type sensorStatusEntry struct {
	SensorID    string  `json:"sensor_id"`
	LastSeenMS  int64   `json:"last_seen_ms"`
	LossRate    float64 `json:"loss_rate"`
	NTPOffsetMS float64 `json:"ntp_offset_ms"`
}

type sensorStatusPayload struct {
	Sensors []sensorStatusEntry `json:"sensors"`
}

// sensorIDReportOrder fixes the iteration order of the sensor_status payload
// (start, then goal) when both have reported.
var sensorIDReportOrder = []string{"start", "goal"}

func (d *dispatcher) emitStatus() {
	if d.deps.OnSensorStatus == nil {
		return
	}

	payload := sensorStatusPayload{Sensors: []sensorStatusEntry{}}
	for _, id := range sensorIDReportOrder {
		s := d.sensors[id]
		if s == nil {
			continue // not yet received: omit rather than emit a placeholder
		}
		payload.Sensors = append(payload.Sensors, sensorStatusEntry{
			SensorID:    id,
			LastSeenMS:  s.lastSeenMS,
			LossRate:    s.lossRate(),
			NTPOffsetMS: s.ntpOffsetMS,
		})
	}

	data, err := json.Marshal(payload)
	if err != nil {
		log.Printf("timing: failed to marshal sensor_status: %v", err)
		return
	}
	d.deps.OnSensorStatus(data)
}
