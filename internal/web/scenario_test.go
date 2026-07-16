package web

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"net/http/cookiejar"
	"net/http/httptest"
	"sort"
	"strconv"
	"testing"
	"time"

	"timemon/internal/domain"
	"timemon/internal/snapshot"
	"timemon/internal/sse"
	"timemon/internal/store"
)

// ---------------------------------------------------------------------
// scenario HTTP client
// ---------------------------------------------------------------------

// scenarioClient drives a running httptest server over real HTTP (not direct
// handler calls, unlike the rest of the package's tests) so the full
// Routes() stack - mux routing, withCacheControl, withCSRFGuard, withAuth/
// withAdmin, and the tm_session cookie set by handleAPISetup - is exercised
// end-to-end. A cookiejar carries the session cookie across requests exactly
// like a browser would.
type scenarioClient struct {
	t       *testing.T
	http    *http.Client
	baseURL string
}

func newScenarioClient(t *testing.T, baseURL string) *scenarioClient {
	t.Helper()
	jar, err := cookiejar.New(nil)
	if err != nil {
		t.Fatalf("cookiejar.New: %v", err)
	}
	return &scenarioClient{
		t: t,
		http: &http.Client{
			Jar: jar,
			// Do not auto-follow redirects: the /my -> /mypage 301 check
			// needs the redirect response itself, not its target.
			CheckRedirect: func(req *http.Request, via []*http.Request) error {
				return http.ErrUseLastResponse
			},
		},
		baseURL: baseURL,
	}
}

// do issues method to path with an optional JSON body and returns the
// response plus its fully-read body. Sec-Fetch-Site: same-origin is set on
// state-changing methods to mirror what a real same-origin browser request
// sends; withCSRFGuard (middleware.go) also allows the header being absent
// entirely (older browsers), which is what Go's http.Client would otherwise
// send, but setting it keeps this test representative of the real gate.
func (c *scenarioClient) do(method, path string, body any) (*http.Response, []byte) {
	c.t.Helper()
	var reader io.Reader
	if body != nil {
		b, err := json.Marshal(body)
		if err != nil {
			c.t.Fatalf("marshal body for %s %s: %v", method, path, err)
		}
		reader = bytes.NewReader(b)
	}
	req, err := http.NewRequest(method, c.baseURL+path, reader)
	if err != nil {
		c.t.Fatalf("new request %s %s: %v", method, path, err)
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	switch method {
	case http.MethodPost, http.MethodPut, http.MethodDelete:
		req.Header.Set("Sec-Fetch-Site", "same-origin")
	}
	resp, err := c.http.Do(req)
	if err != nil {
		c.t.Fatalf("%s %s: %v", method, path, err)
	}
	defer resp.Body.Close()
	respBody, err := io.ReadAll(resp.Body)
	if err != nil {
		c.t.Fatalf("read body %s %s: %v", method, path, err)
	}
	return resp, respBody
}

func (c *scenarioClient) get(path string) (*http.Response, []byte) {
	return c.do(http.MethodGet, path, nil)
}

func (c *scenarioClient) postJSON(path string, body any) (*http.Response, []byte) {
	return c.do(http.MethodPost, path, body)
}

// ---------------------------------------------------------------------
// small decode helpers
// ---------------------------------------------------------------------

func decodeJSON[T any](t *testing.T, body []byte) T {
	t.Helper()
	var v T
	if err := json.Unmarshal(body, &v); err != nil {
		t.Fatalf("unmarshal JSON: %v (body=%s)", err, body)
	}
	return v
}

// sortedKeys returns m's keys, sorted, for a stable key-set comparison.
func sortedKeys(m map[string]json.RawMessage) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}

func topLevelKeys(t *testing.T, body []byte) []string {
	t.Helper()
	m := decodeJSON[map[string]json.RawMessage](t, body)
	return sortedKeys(m)
}

// assertKeySet compares got against want with a diff-friendly failure
// message: this is the "payload shape freeze" check - a future refactor
// that renames/drops/adds a field in this response must fail here.
func assertKeySet(t *testing.T, what string, got, want []string) {
	t.Helper()
	gotSorted := append([]string(nil), got...)
	sort.Strings(gotSorted)
	wantSorted := append([]string(nil), want...)
	sort.Strings(wantSorted)
	if len(gotSorted) != len(wantSorted) {
		t.Errorf("%s: key set changed\n got:  %v\n want: %v", what, gotSorted, wantSorted)
		return
	}
	for i := range gotSorted {
		if gotSorted[i] != wantSorted[i] {
			t.Errorf("%s: key set changed\n got:  %v\n want: %v", what, gotSorted, wantSorted)
			return
		}
	}
}

// ---------------------------------------------------------------------
// scenario test
// ---------------------------------------------------------------------

// TestScenario_FullEventLifecycle drives a full, real-SQLite, real-HTTP
// event lifecycle end to end: first-run setup, every page/static route,
// admin user/vehicle creation, a queue -> course -> sensor-timed run,
// log/ranking/queue/settings payload shapes, event close/archive, and a new
// event that reuses the same driver/vehicle. It is meant to be the
// regression detector for the refactor/dedup work: any change that alters a
// route, a JSON payload shape, or a state-machine transition covered here
// should fail this test.
func TestScenario_FullEventLifecycle(t *testing.T) {
	dbPath := t.TempDir() + "/scenario.sqlite3"
	st, err := store.Open(dbPath)
	if err != nil {
		t.Fatalf("store.Open: %v", err)
	}
	t.Cleanup(func() { st.Close() })

	hub := sse.NewHub()
	snap := snapshot.New(st)
	srv, err := NewServer(st, hub, snap, "http://test")
	if err != nil {
		t.Fatalf("NewServer: %v", err)
	}
	// Keep the finish-confirmation grace window short so this test does not
	// need to sleep for the production 3s (same technique as
	// course_test.go's newTestServer).
	srv.course.graceMS = 50

	ts := httptest.NewServer(srv.Routes())
	t.Cleanup(ts.Close)

	client := newScenarioClient(t, ts.URL)

	// -------------------------------------------------------------
	// 1. POST /api/setup: first-run event + admin creation.
	// -------------------------------------------------------------
	const eventName = "Scenario Event"

	setupTok := srv.setupToken
	if setupTok == "" {
		t.Fatal("step 1: srv.setupToken is empty, expected a token before setup")
	}

	setupReq := setupRequest{
		Token:     setupTok,
		EventName: eventName,
		Event: setupEventInput{
			TimingMode:       "sensor",
			PTMode:           "add",
			PTPenaltyMS:      5000,
			HeatRanking:      false,
			RegistrationMode: "public",
			QueueSelfEntry:   true,
			MaxCourseTimeSec: 180,
			SensorLockoutMS:  800,
		},
		Coefficients: domain.Coefficients{TurboGasoline: 1.7, TurboDiesel: 1.5, Rotary: 1.7, Supercharger: 1.7},
		DisplacementClasses: []domain.DispClass{
			{Label: "~660cc", MaxCC: intp(660)},
			{Label: "~1600cc", MaxCC: intp(1600)},
			{Label: "無制限", MaxCC: nil},
		},
	}
	setupReq.Classes.Driver = []string{"現役", "学内OB", "社会人"}
	setupReq.Classes.Drivetrain = []string{"2WD", "4WD"}
	setupReq.Admin.Name = "牧野"
	setupReq.Admin.DriverClass = "現役"

	resp, body := client.postJSON("/api/setup", setupReq)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("step 1: POST /api/setup: status = %d, want 200; body=%s", resp.StatusCode, body)
	}
	if len(resp.Cookies()) == 0 {
		t.Fatal("step 1: POST /api/setup: no Set-Cookie in response, expected a session cookie")
	}
	var sawSession bool
	for _, c := range resp.Cookies() {
		if c.Name == "tm_session" && c.Value != "" {
			sawSession = true
		}
	}
	if !sawSession {
		t.Fatalf("step 1: POST /api/setup: cookies = %v, expected a non-empty tm_session cookie", resp.Cookies())
	}

	// -------------------------------------------------------------
	// 2. Pages and static assets.
	// -------------------------------------------------------------
	for _, path := range []string{"/", "/ranking", "/archive", "/mypage", "/register", "/admin"} {
		resp, body := client.get(path)
		if resp.StatusCode != http.StatusOK {
			t.Fatalf("step 2: GET %s: status = %d, want 200; body=%s", path, resp.StatusCode, body)
		}
		if got := resp.Header.Get("Cache-Control"); got != "no-store" {
			t.Errorf("step 2: GET %s: Cache-Control = %q, want %q", path, got, "no-store")
		}
	}

	resp, body = client.get("/static/sortable.min.js")
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("step 2: GET /static/sortable.min.js: status = %d, want 200; body=%s", resp.StatusCode, body)
	}
	if got := resp.Header.Get("Cache-Control"); got != "public, max-age=604800" {
		t.Errorf("step 2: GET /static/sortable.min.js: Cache-Control = %q, want %q", got, "public, max-age=604800")
	}

	resp, _ = client.get("/my")
	if resp.StatusCode != http.StatusMovedPermanently {
		t.Fatalf("step 2: GET /my: status = %d, want 301", resp.StatusCode)
	}
	if got := resp.Header.Get("Location"); got != "/mypage" {
		t.Errorf("step 2: GET /my: Location = %q, want %q", got, "/mypage")
	}

	// -------------------------------------------------------------
	// 3. Admin user + vehicle creation (two combos: A runs the course,
	// B stays in the waiting queue so step 6 has a queue row to sample).
	// -------------------------------------------------------------
	driverClasses, err := srv.Store.ListClassDefs("driver")
	if err != nil || len(driverClasses) == 0 {
		t.Fatalf("step 3: ListClassDefs driver: %v", err)
	}
	dtClasses, err := srv.Store.ListClassDefs("drivetrain")
	if err != nil || len(dtClasses) == 0 {
		t.Fatalf("step 3: ListClassDefs drivetrain: %v", err)
	}

	resp, body = client.postJSON("/api/admin/users", map[string]any{
		"name": "テストドライバーA", "driver_class_id": driverClasses[0].ID,
	})
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("step 3: POST /api/admin/users (A): status = %d, want 200; body=%s", resp.StatusCode, body)
	}
	driverA := decodeJSON[struct {
		DriverID int64 `json:"driver_id"`
	}](t, body).DriverID

	resp, body = client.postJSON("/api/admin/users", map[string]any{
		"name": "テストドライバーB", "driver_class_id": driverClasses[0].ID,
	})
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("step 3: POST /api/admin/users (B): status = %d, want 200; body=%s", resp.StatusCode, body)
	}
	driverB := decodeJSON[struct {
		DriverID int64 `json:"driver_id"`
	}](t, body).DriverID

	resp, body = client.postJSON("/api/admin/vehicles", map[string]any{
		"number": 10, "name": "テスト車両A", "engine_type": "gasoline",
		"displacement_cc": 1300, "forced_induction": false, "drivetrain_class_id": dtClasses[0].ID,
	})
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("step 3: POST /api/admin/vehicles (A): status = %d, want 200; body=%s", resp.StatusCode, body)
	}
	vehicleA := decodeJSON[struct {
		VehicleID int64 `json:"vehicle_id"`
	}](t, body).VehicleID

	resp, body = client.postJSON("/api/admin/vehicles", map[string]any{
		"number": 11, "name": "テスト車両B", "engine_type": "gasoline",
		"displacement_cc": 1500, "forced_induction": false, "drivetrain_class_id": dtClasses[0].ID,
	})
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("step 3: POST /api/admin/vehicles (B): status = %d, want 200; body=%s", resp.StatusCode, body)
	}
	vehicleB := decodeJSON[struct {
		VehicleID int64 `json:"vehicle_id"`
	}](t, body).VehicleID

	// -------------------------------------------------------------
	// 4. Queue -> course -> sensor-timed run (combo A), combo B stays
	// waiting.
	// -------------------------------------------------------------
	resp, body = client.postJSON("/api/admin/queue", map[string]any{"driver_id": driverA, "vehicle_id": vehicleA})
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("step 4: POST /api/admin/queue (A): status = %d, want 200; body=%s", resp.StatusCode, body)
	}
	queueA := decodeJSON[struct {
		ID int64 `json:"id"`
	}](t, body).ID

	resp, body = client.postJSON("/api/admin/queue", map[string]any{"driver_id": driverB, "vehicle_id": vehicleB})
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("step 4: POST /api/admin/queue (B): status = %d, want 200; body=%s", resp.StatusCode, body)
	}

	resp, body = client.postJSON("/api/admin/course", nil)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("step 4: POST /api/admin/course: status = %d, want 200; body=%s", resp.StatusCode, body)
	}
	launched := decodeJSON[struct {
		QueueID int64 `json:"queue_id"`
	}](t, body).QueueID
	if launched != queueA {
		t.Fatalf("step 4: POST /api/admin/course launched queue_id = %d, want %d (combo A, head of queue)", launched, queueA)
	}

	tStartUS := time.Now().UnixMicro()
	if err := srv.SensorController().SensorStart(tStartUS); err != nil {
		t.Fatalf("step 4: SensorStart: %v", err)
	}
	tGoalUS := tStartUS + 2_000_000 // +2.000s
	if err := srv.SensorController().SensorGoal(tGoalUS); err != nil {
		t.Fatalf("step 4: SensorGoal: %v", err)
	}

	// Let the (shortened) finish-confirmation grace window elapse so the
	// queue row moves to "done" and the run is picked up by ranking.
	time.Sleep(150 * time.Millisecond)

	qrow, ok, err := srv.Store.GetQueueRow(queueA)
	if err != nil || !ok {
		t.Fatalf("step 4: GetQueueRow(%d): ok=%v err=%v", queueA, ok, err)
	}
	if qrow.Status != "done" {
		t.Fatalf("step 4: queue row %d status = %q, want done after grace window", queueA, qrow.Status)
	}

	// -------------------------------------------------------------
	// 5. GET /api/admin/logs: exactly one log, raw_ms=2000.
	// -------------------------------------------------------------
	resp, body = client.get("/api/admin/logs")
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("step 5: GET /api/admin/logs: status = %d, want 200; body=%s", resp.StatusCode, body)
	}
	logsResp := decodeJSON[struct {
		Logs []struct {
			RawMS int `json:"raw_ms"`
		} `json:"logs"`
	}](t, body)
	if len(logsResp.Logs) != 1 {
		t.Fatalf("step 5: GET /api/admin/logs: got %d logs, want 1; body=%s", len(logsResp.Logs), body)
	}
	if logsResp.Logs[0].RawMS != 2000 {
		t.Fatalf("step 5: GET /api/admin/logs: raw_ms = %d, want 2000", logsResp.Logs[0].RawMS)
	}

	// -------------------------------------------------------------
	// 6. Payload shape freeze: /api/ranking, /api/queue, /api/admin/settings.
	// -------------------------------------------------------------
	_, rankingBody := client.get("/api/ranking")
	rankingTop := topLevelKeys(t, rankingBody)
	rankingRows := decodeJSON[struct {
		Rows []map[string]json.RawMessage `json:"rows"`
	}](t, rankingBody).Rows
	if len(rankingRows) == 0 {
		t.Fatalf("step 6: GET /api/ranking: 0 rows, want >=1; body=%s", rankingBody)
	}
	rankingRowKeys := sortedKeys(rankingRows[0])
	rankingDriverKeys := sortedKeys(decodeJSON[map[string]json.RawMessage](t, rankingRows[0]["driver"]))
	rankingVehicleKeys := sortedKeys(decodeJSON[map[string]json.RawMessage](t, rankingRows[0]["vehicle"]))

	_, queueBody := client.get("/api/queue")
	queueTop := topLevelKeys(t, queueBody)
	queuePayload := decodeJSON[struct {
		Waiting  []map[string]json.RawMessage `json:"waiting"`
		OnCourse []map[string]json.RawMessage `json:"on_course"`
	}](t, queueBody)
	var queueSampleRow map[string]json.RawMessage
	switch {
	case len(queuePayload.Waiting) > 0:
		queueSampleRow = queuePayload.Waiting[0]
	case len(queuePayload.OnCourse) > 0:
		queueSampleRow = queuePayload.OnCourse[0]
	default:
		t.Fatalf("step 6: GET /api/queue: both waiting and on_course empty, want a sample row; body=%s", queueBody)
	}
	queueRowKeys := sortedKeys(queueSampleRow)
	queueDriverKeys := sortedKeys(decodeJSON[map[string]json.RawMessage](t, queueSampleRow["driver"]))
	queueVehicleKeys := sortedKeys(decodeJSON[map[string]json.RawMessage](t, queueSampleRow["vehicle"]))

	_, settingsBody := client.get("/api/admin/settings")
	settingsTop := topLevelKeys(t, settingsBody)

	// Expected key sets below were captured from this exact scenario's live
	// responses (see the task's capture step) - they are a shape freeze, not
	// a guess: any future refactor that adds/removes/renames a field in one
	// of these payloads must fail one of these checks.
	assertKeySet(t, "GET /api/ranking top-level", rankingTop, []string{"rows"})
	assertKeySet(t, "GET /api/ranking row", rankingRowKeys, []string{
		"best_log_id", "best_ms", "driver", "driver_class", "invalid",
		"pt_total", "runs", "second_ms", "valid_runs", "vehicle",
	})
	assertKeySet(t, "GET /api/ranking row.driver", rankingDriverKeys, []string{"has_icon", "id", "name"})
	assertKeySet(t, "GET /api/ranking row.vehicle", rankingVehicleKeys, []string{
		"converted_cc", "disp_class", "dt_class", "engine", "has_icon", "id", "name", "number",
	})
	assertKeySet(t, "GET /api/queue top-level", queueTop, []string{"on_course", "waiting"})
	assertKeySet(t, "GET /api/queue row", queueRowKeys, []string{"driver", "position", "queue_id", "vehicle"})
	assertKeySet(t, "GET /api/queue row.driver", queueDriverKeys, []string{"has_icon", "id", "name"})
	assertKeySet(t, "GET /api/queue row.vehicle", queueVehicleKeys, []string{"has_icon", "id", "name", "number"})
	assertKeySet(t, "GET /api/admin/settings top-level", settingsTop, []string{
		"coefficients", "displacement_classes", "event_name", "heat_ranking", "max_course_time_sec",
		"pt_mode", "pt_penalty_ms", "queue_self_entry", "registration_mode", "registration_open",
		"sensor_lockout_ms", "timing_mode",
	})

	// -------------------------------------------------------------
	// 7. Close the event; queue add now 409s; archive has the event.
	// -------------------------------------------------------------
	activeEv, ok, err := srv.Store.GetActiveEvent()
	if err != nil || !ok {
		t.Fatalf("step 7: GetActiveEvent: ok=%v err=%v", ok, err)
	}

	resp, body = client.postJSON("/api/admin/events/"+itoa(activeEv.ID)+"/close", nil)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("step 7: POST /api/admin/events/%d/close: status = %d, want 200; body=%s", activeEv.ID, resp.StatusCode, body)
	}

	resp, body = client.postJSON("/api/admin/queue", map[string]any{"driver_id": driverA, "vehicle_id": vehicleA})
	if resp.StatusCode != http.StatusConflict {
		t.Fatalf("step 7: POST /api/admin/queue after close: status = %d, want 409; body=%s", resp.StatusCode, body)
	}

	resp, body = client.get("/api/archive/events")
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("step 7: GET /api/archive/events: status = %d, want 200; body=%s", resp.StatusCode, body)
	}
	archEvents := decodeJSON[struct {
		Events []struct {
			ID     int64  `json:"id"`
			Name   string `json:"name"`
			Status string `json:"status"`
		} `json:"events"`
	}](t, body)
	if len(archEvents.Events) != 1 {
		t.Fatalf("step 7: GET /api/archive/events: got %d events, want 1; body=%s", len(archEvents.Events), body)
	}
	if archEvents.Events[0].Status != "closed" || archEvents.Events[0].Name != eventName {
		t.Fatalf("step 7: GET /api/archive/events: event = %+v, want status=closed name=%q", archEvents.Events[0], eventName)
	}
	closedID := archEvents.Events[0].ID

	resp, body = client.get("/api/archive/" + itoa(closedID) + "/ranking")
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("step 7: GET /api/archive/%d/ranking: status = %d, want 200; body=%s", closedID, resp.StatusCode, body)
	}
	archRanking := decodeJSON[struct {
		Rows []json.RawMessage `json:"rows"`
	}](t, body)
	if len(archRanking.Rows) == 0 {
		t.Fatalf("step 7: GET /api/archive/%d/ranking: 0 rows, want >=1 (the finished A run); body=%s", closedID, body)
	}

	// -------------------------------------------------------------
	// 8. New event (copy_from_last), same driver/vehicle queues again.
	// -------------------------------------------------------------
	resp, body = client.postJSON("/api/admin/events", map[string]any{
		"name": "Second Scenario Event", "copy_from_last": true,
	})
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("step 8: POST /api/admin/events: status = %d, want 200; body=%s", resp.StatusCode, body)
	}

	resp, body = client.postJSON("/api/admin/queue", map[string]any{"driver_id": driverA, "vehicle_id": vehicleA})
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("step 8: POST /api/admin/queue in new event: status = %d, want 200; body=%s", resp.StatusCode, body)
	}
}

func itoa(id int64) string {
	return strconv.FormatInt(id, 10)
}
