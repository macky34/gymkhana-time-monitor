package timing

import (
	"context"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"timemon/internal/store"
)

// ---------------------------------------------------------------------------
// fakes & helpers
// ---------------------------------------------------------------------------

// fakeCourse is a CourseController that records calls and can be switched to
// "no pairing target" mode. It wraps ErrNoTarget (rather than returning it
// bare) to verify the dispatcher matches with errors.Is.
type fakeCourse struct {
	mu       sync.Mutex
	noTarget bool
	starts   []int64
	goals    []int64
	startCh  chan int64
	goalCh   chan int64
}

var _ CourseController = (*fakeCourse)(nil)

func newFakeCourse(noTarget bool) *fakeCourse {
	return &fakeCourse{
		noTarget: noTarget,
		startCh:  make(chan int64, 16),
		goalCh:   make(chan int64, 16),
	}
}

func (f *fakeCourse) SensorStart(tUS int64) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.noTarget {
		return fmt.Errorf("fake course: %w", ErrNoTarget)
	}
	f.starts = append(f.starts, tUS)
	f.startCh <- tUS
	return nil
}

func (f *fakeCourse) SensorGoal(tUS int64) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.noTarget {
		return fmt.Errorf("fake course: %w", ErrNoTarget)
	}
	f.goals = append(f.goals, tUS)
	f.goalCh <- tUS
	return nil
}

func newTestStore(t *testing.T) *store.Store {
	t.Helper()
	s, err := store.Open(filepath.Join(t.TempDir(), "timing_test.db"))
	if err != nil {
		t.Fatalf("store.Open: %v", err)
	}
	t.Cleanup(func() { s.Close() })
	return s
}

// listenHarness runs Listen on an ephemeral 127.0.0.1 port and provides a UDP
// sender wired to it.
type listenHarness struct {
	send    func(payload string)
	cancel  context.CancelFunc
	stopped chan struct{} // closed once Listen has returned
	err     error         // Listen's return value; read only after <-stopped
}

func startListener(t *testing.T, deps Deps) *listenHarness {
	t.Helper()

	addrCh := make(chan net.Addr, 1)
	deps.boundAddr = addrCh

	ctx, cancel := context.WithCancel(context.Background())
	h := &listenHarness{cancel: cancel, stopped: make(chan struct{})}
	go func() {
		h.err = Listen(ctx, "127.0.0.1:0", deps)
		close(h.stopped)
	}()

	var bound net.Addr
	select {
	case bound = <-addrCh:
	case <-h.stopped:
		t.Fatalf("Listen exited before binding: %v", h.err)
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for Listen to bind")
	}

	conn, err := net.Dial("udp", bound.String())
	if err != nil {
		t.Fatalf("dial udp: %v", err)
	}

	h.send = func(payload string) {
		t.Helper()
		if _, err := conn.Write([]byte(payload)); err != nil {
			t.Fatalf("udp write: %v", err)
		}
	}

	t.Cleanup(func() {
		cancel()
		select {
		case <-h.stopped:
		case <-time.After(2 * time.Second):
			t.Error("Listen did not return after context cancel")
		}
		conn.Close()
	})

	return h
}

func triggerJSON(sensor string, bootID, seq, tsUS int64) string {
	return fmt.Sprintf(`{"type":"trigger","sensor_id":%q,"boot_id":%d,"seq":%d,"timestamp_us":%d}`,
		sensor, bootID, seq, tsUS)
}

func hbJSON(sensor string, bootID, seq int64, ntpOffsetMS float64) string {
	return fmt.Sprintf(`{"type":"hb","sensor_id":%q,"boot_id":%d,"seq":%d,"ntp_offset_ms":%g}`,
		sensor, bootID, seq, ntpOffsetMS)
}

func recvTS(t *testing.T, ch <-chan int64, what string) int64 {
	t.Helper()
	select {
	case v := <-ch:
		return v
	case <-time.After(time.Second):
		t.Fatalf("timed out waiting for %s", what)
		return 0
	}
}

func assertQuiet(t *testing.T, ch <-chan int64, wait time.Duration, what string) {
	t.Helper()
	select {
	case v := <-ch:
		t.Fatalf("unexpected %s (ts=%d)", what, v)
	case <-time.After(wait):
	}
}

// ---------------------------------------------------------------------------
// (1) trigger burst-of-3 -> SensorStart called exactly once
// ---------------------------------------------------------------------------

func TestTriggerBurstCallsSensorStartOnce(t *testing.T) {
	course := newFakeCourse(false)
	h := startListener(t, Deps{Store: newTestStore(t), Course: course})

	const ts = int64(1720000000000000)
	burst := triggerJSON("start", 42, 1, ts)
	for i := 0; i < 3; i++ { // ESP32 sends the identical trigger 3 times
		h.send(burst)
	}

	if got := recvTS(t, course.startCh, "SensorStart"); got != ts {
		t.Fatalf("SensorStart ts = %d, want %d", got, ts)
	}
	assertQuiet(t, course.startCh, 300*time.Millisecond, "second SensorStart for duplicate burst")

	// A new seq from the same boot must pair again.
	h.send(triggerJSON("start", 42, 2, ts+1_000_000))
	if got := recvTS(t, course.startCh, "SensorStart for seq 2"); got != ts+1_000_000 {
		t.Fatalf("SensorStart ts = %d, want %d", got, ts+1_000_000)
	}
}

// ---------------------------------------------------------------------------
// (2) goal trigger -> SensorGoal
// ---------------------------------------------------------------------------

func TestGoalTriggerCallsSensorGoal(t *testing.T) {
	course := newFakeCourse(false)
	h := startListener(t, Deps{Store: newTestStore(t), Course: course})

	const ts = int64(1720000123456789)
	h.send(triggerJSON("goal", 7, 1, ts))

	if got := recvTS(t, course.goalCh, "SensorGoal"); got != ts {
		t.Fatalf("SensorGoal ts = %d, want %d", got, ts)
	}
	assertQuiet(t, course.startCh, 200*time.Millisecond, "SensorStart call from a goal trigger")
}

// ---------------------------------------------------------------------------
// (3) no pairing target -> OnOrphan
// ---------------------------------------------------------------------------

func TestOrphanCallbacks(t *testing.T) {
	orphans := make(chan [2]string, 16)
	course := newFakeCourse(true) // always ErrNoTarget (wrapped)
	h := startListener(t, Deps{
		Store:  newTestStore(t),
		Course: course,
		OnOrphan: func(kind, detail string) {
			orphans <- [2]string{kind, detail}
		},
	})

	h.send(triggerJSON("start", 11, 1, 111))
	h.send(triggerJSON("goal", 11, 2, 222))

	for _, want := range []struct {
		kind, tsFrag string
	}{
		{"orphan_start", "ts=111"},
		{"orphan_goal", "ts=222"},
	} {
		select {
		case got := <-orphans:
			if got[0] != want.kind {
				t.Fatalf("orphan kind = %q, want %q", got[0], want.kind)
			}
			if !strings.Contains(got[1], want.tsFrag) {
				t.Fatalf("orphan detail %q does not contain %q", got[1], want.tsFrag)
			}
		case <-time.After(time.Second):
			t.Fatalf("timed out waiting for %s", want.kind)
		}
	}
}

// ---------------------------------------------------------------------------
// (4) invalid JSON / unknown type / unknown sensor_id are harmless
// ---------------------------------------------------------------------------

func TestMalformedPacketsAreIgnored(t *testing.T) {
	orphans := make(chan [2]string, 16)
	course := newFakeCourse(false)
	h := startListener(t, Deps{
		Store:    newTestStore(t),
		Course:   course,
		OnOrphan: func(kind, detail string) { orphans <- [2]string{kind, detail} },
	})

	h.send(`{this is not json`)
	h.send(`"a bare json string"`)
	h.send(`{"type":"mystery","sensor_id":"start","boot_id":1,"seq":1}`)
	h.send(`{"type":"trigger","sensor_id":"lane3","boot_id":1,"seq":2,"timestamp_us":333}`)

	// The listener must still be alive and process a valid trigger.
	h.send(triggerJSON("start", 99, 1, 444))
	if got := recvTS(t, course.startCh, "SensorStart after malformed packets"); got != 444 {
		t.Fatalf("SensorStart ts = %d, want 444", got)
	}
	assertQuiet(t, course.startCh, 200*time.Millisecond, "extra SensorStart from malformed packets")
	assertQuiet(t, course.goalCh, 10*time.Millisecond, "SensorGoal from malformed packets")
	select {
	case got := <-orphans:
		t.Fatalf("unexpected orphan from malformed packets: %v", got)
	default:
	}
}

// ---------------------------------------------------------------------------
// (5) hb -> OnSensorStatus JSON shape (short injected ticker interval)
// ---------------------------------------------------------------------------

// decodeStatus parses a sensor_status payload into generic maps so the tests
// can assert the exact wire shape (key set), not just Go-struct round-trips.
func decodeStatus(t *testing.T, raw []byte) []map[string]any {
	t.Helper()
	var top map[string]any
	if err := json.Unmarshal(raw, &top); err != nil {
		t.Fatalf("sensor_status is not valid JSON: %v (raw=%s)", err, raw)
	}
	if len(top) != 1 {
		t.Fatalf("sensor_status top-level keys = %v, want exactly [sensors] (raw=%s)", top, raw)
	}
	arr, ok := top["sensors"].([]any)
	if !ok {
		t.Fatalf(`sensor_status "sensors" is not an array (raw=%s)`, raw)
	}
	out := make([]map[string]any, 0, len(arr))
	for _, el := range arr {
		m, ok := el.(map[string]any)
		if !ok {
			t.Fatalf("sensor_status element is not an object (raw=%s)", raw)
		}
		for _, key := range []string{"sensor_id", "last_seen_ms", "loss_rate", "ntp_offset_ms"} {
			if _, present := m[key]; !present {
				t.Fatalf("sensor_status element missing key %q (raw=%s)", key, raw)
			}
		}
		if len(m) != 4 {
			t.Fatalf("sensor_status element has extra keys: %v (raw=%s)", m, raw)
		}
		out = append(out, m)
	}
	return out
}

// waitStatus reads status payloads until cond is satisfied or a deadline hits.
func waitStatus(t *testing.T, statuses <-chan []byte, what string, cond func([]map[string]any) bool) []map[string]any {
	t.Helper()
	deadline := time.After(2 * time.Second)
	for {
		select {
		case raw := <-statuses:
			sensors := decodeStatus(t, raw)
			if cond(sensors) {
				return sensors
			}
		case <-deadline:
			t.Fatalf("timed out waiting for sensor_status: %s", what)
			return nil
		}
	}
}

func TestHeartbeatSensorStatus(t *testing.T) {
	statuses := make(chan []byte, 64)
	h := startListener(t, Deps{
		Store:  newTestStore(t),
		Course: newFakeCourse(false),
		OnSensorStatus: func(data []byte) {
			select {
			case statuses <- data:
			default:
			}
		},
		StatusInterval: 50 * time.Millisecond, // injected: default is 5s
	})

	before := time.Now().UnixMilli()

	// Only "start" has reported: payload must contain exactly one element.
	h.send(hbJSON("start", 100, 1, 0.4))
	sensors := waitStatus(t, statuses, "start only", func(s []map[string]any) bool {
		return len(s) == 1 && s[0]["sensor_id"] == "start"
	})
	if got := sensors[0]["ntp_offset_ms"].(float64); got != 0.4 {
		t.Fatalf("ntp_offset_ms = %v, want 0.4", got)
	}
	if got := sensors[0]["loss_rate"].(float64); got != 0 {
		t.Fatalf("loss_rate = %v, want 0 for a single hb", got)
	}
	if got := int64(sensors[0]["last_seen_ms"].(float64)); got < before || got > time.Now().UnixMilli() {
		t.Fatalf("last_seen_ms = %d, want within [%d, now]", got, before)
	}

	// Gap in start's seq (2,3,4,6,7 after 1 -> 6 received of expected 7) and
	// a first hb from goal: two elements in fixed start,goal order.
	for _, seq := range []int64{2, 3, 4, 6, 7} {
		h.send(hbJSON("start", 100, seq, 0.5))
	}
	h.send(hbJSON("goal", 200, 1, -1.25))

	const wantLoss = 1.0 / 7.0
	sensors = waitStatus(t, statuses, "start with loss + goal", func(s []map[string]any) bool {
		if len(s) != 2 || s[0]["sensor_id"] != "start" || s[1]["sensor_id"] != "goal" {
			return false
		}
		loss := s[0]["loss_rate"].(float64)
		return loss > wantLoss-1e-9 && loss < wantLoss+1e-9
	})
	if got := sensors[1]["ntp_offset_ms"].(float64); got != -1.25 {
		t.Fatalf("goal ntp_offset_ms = %v, want -1.25", got)
	}
	if got := sensors[1]["loss_rate"].(float64); got != 0 {
		t.Fatalf("goal loss_rate = %v, want 0", got)
	}

	// boot_id change resets the loss window.
	h.send(hbJSON("start", 101, 1, 0.6))
	waitStatus(t, statuses, "loss reset after boot_id change", func(s []map[string]any) bool {
		return len(s) == 2 && s[0]["sensor_id"] == "start" &&
			s[0]["loss_rate"].(float64) == 0 && s[0]["ntp_offset_ms"].(float64) == 0.6
	})
}

// ---------------------------------------------------------------------------
// (6) ctx cancel makes Listen return
// ---------------------------------------------------------------------------

func TestListenReturnsOnContextCancel(t *testing.T) {
	h := startListener(t, Deps{Store: newTestStore(t), Course: newFakeCourse(false)})

	h.cancel()
	select {
	case <-h.stopped:
		if h.err != nil {
			t.Fatalf("Listen returned error on cancel: %v", h.err)
		}
	case <-time.After(time.Second):
		t.Fatal("Listen did not return within 1s of context cancel")
	}
}

// ---------------------------------------------------------------------------
// (7) sensor config endpoint
// ---------------------------------------------------------------------------

func TestSensorConfigHandler(t *testing.T) {
	handler := SensorConfigHandler(func() int { return 800 })

	rr := httptest.NewRecorder()
	handler.ServeHTTP(rr, httptest.NewRequest(http.MethodGet, "/api/sensor/config", nil))

	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rr.Code)
	}
	if ct := rr.Header().Get("Content-Type"); ct != "application/json" {
		t.Fatalf("Content-Type = %q, want application/json", ct)
	}
	if body := rr.Body.String(); body != `{"lockout_ms":800}` {
		t.Fatalf("body = %q, want %q", body, `{"lockout_ms":800}`)
	}
}
