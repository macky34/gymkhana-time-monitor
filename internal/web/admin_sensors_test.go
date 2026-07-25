package web

import (
	"context"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"timemon/internal/store"
	"timemon/internal/timing"
)

// callAdminSensorDelete drives handleAdminSensorDelete directly (bypassing
// Routes()), same style as callAdminUsersByID but with a string sensor_id
// path value instead of an integer id.
func callAdminSensorDelete(t *testing.T, srv *Server, sensorID string, admin store.Driver) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(http.MethodDelete, "/api/admin/sensors/"+sensorID, nil)
	req.SetPathValue("id", sensorID)
	rec := httptest.NewRecorder()
	srv.handleAdminSensorDelete(rec, req, admin)
	return rec
}

func TestAdminSensorDeleteRejectsUnknownSensorID(t *testing.T) {
	srv, _, driverID, _ := newTestServer(t, "sensor")
	admin, ok, err := srv.Store.GetDriver(driverID)
	if err != nil || !ok {
		t.Fatalf("GetDriver: ok=%v err=%v", ok, err)
	}
	srv.SetSensorControl(timing.NewControl())

	rec := callAdminSensorDelete(t, srv, "bogus", admin)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400; body=%s", rec.Code, rec.Body.String())
	}
}

func TestAdminSensorDeleteWithoutControlIsUnavailable(t *testing.T) {
	srv, _, driverID, _ := newTestServer(t, "sensor")
	admin, ok, err := srv.Store.GetDriver(driverID)
	if err != nil || !ok {
		t.Fatalf("GetDriver: ok=%v err=%v", ok, err)
	}
	// srv.sensorControl left nil, as in tests that don't wire timing.Listen.

	rec := callAdminSensorDelete(t, srv, "start", admin)
	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("status = %d, want 503; body=%s", rec.Code, rec.Body.String())
	}
}

// startRealDispatcher runs timing.Listen for real against a reserved local
// UDP port, so this test exercises handleAdminSensorDelete against an actual
// live dispatcher rather than a synthetic error value: sensorControl in
// production is only ever backed by a running dispatcher, and the
// ErrSensorAlive/ErrSensorUnknown paths originate entirely from dispatcher
// state private to the timing package.
func startRealDispatcher(t *testing.T, deps timing.Deps) func(sensorID string, bootID, seq int64, ntpOffsetMS float64) {
	t.Helper()

	// Reserve a free port by briefly binding, then hand the same address to
	// Listen. The gap between Close and Listen's bind is negligible in
	// practice for a same-process, localhost-only test.
	probe, err := net.ListenPacket("udp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("reserve udp port: %v", err)
	}
	addr := probe.LocalAddr().String()
	probe.Close()

	ctx, cancel := context.WithCancel(context.Background())
	stopped := make(chan struct{})
	go func() {
		if err := timing.Listen(ctx, addr, deps); err != nil {
			t.Logf("timing.Listen: %v", err)
		}
		close(stopped)
	}()
	t.Cleanup(func() {
		cancel()
		select {
		case <-stopped:
		case <-time.After(2 * time.Second):
			t.Error("timing.Listen did not return after context cancel")
		}
	})

	var conn net.Conn
	deadline := time.Now().Add(time.Second)
	for time.Now().Before(deadline) {
		conn, err = net.Dial("udp", addr)
		if err == nil {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	if conn == nil {
		t.Fatalf("dial udp %s: %v", addr, err)
	}
	t.Cleanup(func() { conn.Close() })

	return func(sensorID string, bootID, seq int64, ntpOffsetMS float64) {
		t.Helper()
		payload := fmt.Sprintf(`{"type":"hb","sensor_id":%q,"boot_id":%d,"seq":%d,"ntp_offset_ms":%g}`,
			sensorID, bootID, seq, ntpOffsetMS)
		if _, err := conn.Write([]byte(payload)); err != nil {
			t.Fatalf("udp write: %v", err)
		}
	}
}

func TestAdminSensorDeleteRefusesLiveSensor(t *testing.T) {
	srv, _, driverID, _ := newTestServer(t, "sensor")
	admin, ok, err := srv.Store.GetDriver(driverID)
	if err != nil || !ok {
		t.Fatalf("GetDriver: ok=%v err=%v", ok, err)
	}

	ctl := timing.NewControl()
	srv.SetSensorControl(ctl)

	statuses := make(chan []byte, 8)
	sendHB := startRealDispatcher(t, timing.Deps{
		Store:  srv.Store,
		Course: srv.SensorController(),
		OnSensorStatus: func(data []byte) {
			select {
			case statuses <- data:
			default:
			}
		},
		StatusInterval: 30 * time.Millisecond,
		Control:        ctl,
	})

	// UDP is connectionless, so net.Dial succeeding does not guarantee the
	// server side has bound yet; resend the heartbeat on a short interval
	// until it shows up in sensor_status rather than sending it once.
	resend := time.NewTicker(20 * time.Millisecond)
	defer resend.Stop()
	deadline := time.After(2 * time.Second)
waitLoop:
	for {
		select {
		case raw := <-statuses:
			var payload struct {
				Sensors []struct {
					SensorID string `json:"sensor_id"`
				} `json:"sensors"`
			}
			if err := json.Unmarshal(raw, &payload); err == nil {
				for _, s := range payload.Sensors {
					if s.SensorID == "start" {
						break waitLoop
					}
				}
			}
		case <-resend.C:
			sendHB("start", 1, 1, 0)
		case <-deadline:
			t.Fatal("timed out waiting for start heartbeat to be reflected in sensor_status")
		}
	}

	rec := callAdminSensorDelete(t, srv, "start", admin)
	if rec.Code != http.StatusConflict {
		t.Fatalf("status = %d, want 409; body=%s", rec.Code, rec.Body.String())
	}
}
