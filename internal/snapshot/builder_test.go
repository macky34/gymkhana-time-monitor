package snapshot

import (
	"encoding/json"
	"net/url"
	"path/filepath"
	"strconv"
	"testing"

	"timemon/internal/domain"
	"timemon/internal/store"
)

func intPtr(v int) *int { return &v }

// fixture holds the ids created by seedFixture so tests can refer to them.
type fixture struct {
	s *store.Store

	driverActive int64 // 現役 driver, drives the ICE car
	driverAlumni int64 // 学内OB driver, drives the EV

	vehicleICE int64 // gasoline, NA, 1595cc
	vehicleEV  int64

	logFast1 int64 // 84310ms, valid
	logFast2 int64 // 83456ms, valid (best)
	logEVRun int64 // 90000ms, valid, EV combo
	logMC    int64 // 81000ms, is_mc=true
}

// seedFixture opens a fresh temp-dir SQLite store, seeds one event with two
// driver classes / two drivetrain classes, two drivers, an ICE turbo... er,
// an ICE NA vehicle and an EV vehicle, one entry each, and four logs (three
// normal + one is_mc) covering both combinations.
func seedFixture(t *testing.T) fixture {
	t.Helper()

	dir := t.TempDir()
	s, err := store.Open(filepath.Join(dir, "test.db"))
	if err != nil {
		t.Fatalf("store.Open: %v", err)
	}
	t.Cleanup(func() { s.Close() })

	settings := store.EventRow{
		EventName:        "テスト大会",
		TimingMode:       "sensor",
		PTMode:           "add",
		PTPenaltyMS:      5000,
		HeatRanking:      false,
		RegistrationMode: "public",
		RegistrationOpen: true,
		QueueSelfEntry:   true,
		MaxCourseTimeSec: 180,
		SensorLockoutMS:  3000,
		Coef: domain.Coefficients{
			TurboGasoline: 1.7,
			TurboDiesel:   1.5,
			Rotary:        1.5,
			Supercharger:  1.7,
		},
		DispClasses: []domain.DispClass{
			{Label: "~1600cc", MaxCC: intPtr(1600)},
			{Label: "無制限", MaxCC: nil},
		},
	}
	if err := s.SeedEvent(settings, []string{"現役", "学内OB"}, []string{"2WD", "4WD"}); err != nil {
		t.Fatalf("SeedEvent: %v", err)
	}

	driverClasses, err := s.ListClassDefs("driver")
	if err != nil {
		t.Fatalf("ListClassDefs(driver): %v", err)
	}
	if len(driverClasses) < 2 {
		t.Fatalf("expected 2 driver classes, got %d", len(driverClasses))
	}
	dtClasses, err := s.ListClassDefs("drivetrain")
	if err != nil {
		t.Fatalf("ListClassDefs(drivetrain): %v", err)
	}
	if len(dtClasses) < 2 {
		t.Fatalf("expected 2 drivetrain classes, got %d", len(dtClasses))
	}

	driverActive, err := s.CreateDriver("山田", driverClasses[0].ID, "tok-yamada", "driver")
	if err != nil {
		t.Fatalf("CreateDriver active: %v", err)
	}
	driverAlumni, err := s.CreateDriver("鈴木", driverClasses[1].ID, "tok-suzuki", "driver")
	if err != nil {
		t.Fatalf("CreateDriver alumni: %v", err)
	}

	iceCC := 1595
	vehicleICE, err := s.CreateVehicle(store.Vehicle{
		Number:            1,
		Name:              "EF9シビック",
		Engine:            domain.EngineType("gasoline"),
		DisplacementCC:    &iceCC,
		ForcedInduction:   false,
		DrivetrainClassID: dtClasses[0].ID,
	})
	if err != nil {
		t.Fatalf("CreateVehicle ICE: %v", err)
	}
	vehicleEV, err := s.CreateVehicle(store.Vehicle{
		Number:            2,
		Name:              "リーフ",
		Engine:            domain.EngineType("ev"),
		DisplacementCC:    nil,
		ForcedInduction:   false,
		DrivetrainClassID: dtClasses[1].ID,
	})
	if err != nil {
		t.Fatalf("CreateVehicle EV: %v", err)
	}

	if err := s.AddEntry(driverActive, vehicleICE); err != nil {
		t.Fatalf("AddEntry ICE: %v", err)
	}
	if err := s.AddEntry(driverAlumni, vehicleEV); err != nil {
		t.Fatalf("AddEntry EV: %v", err)
	}

	activeEvent, ok, err := s.GetActiveEvent()
	if err != nil {
		t.Fatalf("GetActiveEvent: %v", err)
	}
	if !ok {
		t.Fatalf("GetActiveEvent: no active event after SeedEvent")
	}
	eventID := activeEvent.ID

	logFast1, err := s.InsertLog(store.LogRow{
		EventID:  eventID,
		DriverID: &driverActive, VehicleID: &vehicleICE,
		RawMS: 84310, PTCount: 0, IsMC: false,
		TimestampMS: 1720000000000, Source: "sensor",
	})
	if err != nil {
		t.Fatalf("InsertLog logFast1: %v", err)
	}
	logFast2, err := s.InsertLog(store.LogRow{
		EventID:  eventID,
		DriverID: &driverActive, VehicleID: &vehicleICE,
		RawMS: 83456, PTCount: 0, IsMC: false,
		TimestampMS: 1720000100000, Source: "sensor",
	})
	if err != nil {
		t.Fatalf("InsertLog logFast2: %v", err)
	}
	logEVRun, err := s.InsertLog(store.LogRow{
		EventID:  eventID,
		DriverID: &driverAlumni, VehicleID: &vehicleEV,
		RawMS: 90000, PTCount: 0, IsMC: false,
		TimestampMS: 1720000200000, Source: "sensor",
	})
	if err != nil {
		t.Fatalf("InsertLog logEVRun: %v", err)
	}
	logMC, err := s.InsertLog(store.LogRow{
		EventID:  eventID,
		DriverID: &driverActive, VehicleID: &vehicleICE,
		RawMS: 81000, PTCount: 0, IsMC: true,
		TimestampMS: 1720000300000, Source: "sensor",
	})
	if err != nil {
		t.Fatalf("InsertLog logMC: %v", err)
	}

	return fixture{
		s:            s,
		driverActive: driverActive,
		driverAlumni: driverAlumni,
		vehicleICE:   vehicleICE,
		vehicleEV:    vehicleEV,
		logFast1:     logFast1,
		logFast2:     logFast2,
		logEVRun:     logEVRun,
		logMC:        logMC,
	}
}

func TestRankingShapeOrderAndEV(t *testing.T) {
	fx := seedFixture(t)
	b := New(fx.s)

	data, err := b.Ranking()
	if err != nil {
		t.Fatalf("Ranking: %v", err)
	}

	var got struct {
		Rows []struct {
			Driver struct {
				ID   int64  `json:"id"`
				Name string `json:"name"`
			} `json:"driver"`
			DriverClass string `json:"driver_class"`
			Vehicle     struct {
				ID          int64  `json:"id"`
				Number      int    `json:"number"`
				Name        string `json:"name"`
				Engine      string `json:"engine"`
				ConvertedCC *int   `json:"converted_cc"`
				DispClass   string `json:"disp_class"`
				DTClass     string `json:"dt_class"`
			} `json:"vehicle"`
			BestMS    *int   `json:"best_ms"`
			SecondMS  *int   `json:"second_ms"`
			BestLogID *int64 `json:"best_log_id"`
			Runs      int    `json:"runs"`
			ValidRuns int    `json:"valid_runs"`
			PTTotal   int    `json:"pt_total"`
			Invalid   bool   `json:"invalid"`
		} `json:"rows"`
	}
	if err := json.Unmarshal(data, &got); err != nil {
		t.Fatalf("Unmarshal: %v\ndata=%s", err, data)
	}

	if len(got.Rows) != 2 {
		t.Fatalf("expected 2 combos, got %d: %s", len(got.Rows), data)
	}

	first, second := got.Rows[0], got.Rows[1]

	// The ICE combo's best valid run (83456ms) beats the EV combo's only
	// run (90000ms), so it must rank first.
	if first.Vehicle.ID != fx.vehicleICE {
		t.Fatalf("expected ICE combo ranked first, got vehicle id %d first: %s", first.Vehicle.ID, data)
	}
	if first.Driver.ID != fx.driverActive || first.Driver.Name != "山田" {
		t.Fatalf("first row driver = %+v, want id=%d name=山田", first.Driver, fx.driverActive)
	}
	if first.DriverClass != "現役" {
		t.Fatalf("first row driver_class = %q, want 現役", first.DriverClass)
	}
	if first.BestMS == nil || *first.BestMS != 83456 {
		t.Fatalf("ICE best_ms = %v, want 83456", first.BestMS)
	}
	if first.BestLogID == nil || *first.BestLogID != fx.logFast2 {
		t.Fatalf("ICE best_log_id = %v, want %d", first.BestLogID, fx.logFast2)
	}
	if first.Vehicle.Engine != "gasoline" {
		t.Fatalf("ICE engine = %q, want gasoline", first.Vehicle.Engine)
	}
	if first.Vehicle.ConvertedCC == nil || *first.Vehicle.ConvertedCC != 1595 {
		t.Fatalf("ICE converted_cc = %v, want 1595 (non-turbo NA should be unconverted)", first.Vehicle.ConvertedCC)
	}
	if first.Vehicle.DTClass != "2WD" {
		t.Fatalf("ICE dt_class = %q, want 2WD", first.Vehicle.DTClass)
	}

	if second.Vehicle.ID != fx.vehicleEV {
		t.Fatalf("expected EV combo ranked second, got vehicle id %d: %s", second.Vehicle.ID, data)
	}
	if second.Vehicle.ConvertedCC != nil {
		t.Fatalf("EV converted_cc = %v, want null", *second.Vehicle.ConvertedCC)
	}
	if second.Vehicle.Engine != "ev" {
		t.Fatalf("EV engine = %q, want ev", second.Vehicle.Engine)
	}
	if second.Vehicle.DispClass != "EV" {
		t.Fatalf("EV disp_class = %q, want EV", second.Vehicle.DispClass)
	}
	if second.DriverClass != "学内OB" {
		t.Fatalf("second row driver_class = %q, want 学内OB", second.DriverClass)
	}
}

func TestSettingsDerivesEVClass(t *testing.T) {
	fx := seedFixture(t)
	b := New(fx.s)

	data, err := b.Settings()
	if err != nil {
		t.Fatalf("Settings: %v", err)
	}

	var got struct {
		EventName     string   `json:"event_name"`
		TimingMode    string   `json:"timing_mode"`
		PTMode        string   `json:"pt_mode"`
		PTPenaltyMS   int      `json:"pt_penalty_ms"`
		DispClasses   []string `json:"disp_classes"`
		DriverClasses []struct {
			ID    int64  `json:"id"`
			Label string `json:"label"`
		} `json:"driver_classes"`
		DTClasses []struct {
			ID    int64  `json:"id"`
			Label string `json:"label"`
		} `json:"dt_classes"`
	}
	if err := json.Unmarshal(data, &got); err != nil {
		t.Fatalf("Unmarshal: %v\ndata=%s", err, data)
	}

	if got.EventName != "テスト大会" {
		t.Fatalf("event_name = %q: %s", got.EventName, data)
	}
	if got.PTMode != "add" || got.PTPenaltyMS != 5000 {
		t.Fatalf("pt_mode/pt_penalty_ms = %q/%d, want add/5000: %s", got.PTMode, got.PTPenaltyMS, data)
	}
	wantDisp := []string{"~1600cc", "無制限", "EV"}
	if len(got.DispClasses) != len(wantDisp) {
		t.Fatalf("disp_classes = %v, want %v", got.DispClasses, wantDisp)
	}
	for i, w := range wantDisp {
		if got.DispClasses[i] != w {
			t.Fatalf("disp_classes[%d] = %q, want %q: %s", i, got.DispClasses[i], w, data)
		}
	}
	if len(got.DriverClasses) != 2 {
		t.Fatalf("driver_classes = %v, want 2 entries", got.DriverClasses)
	}
	if len(got.DTClasses) != 2 {
		t.Fatalf("dt_classes = %v, want 2 entries", got.DTClasses)
	}
}

func TestRecentHeatAssignmentAndOrder(t *testing.T) {
	fx := seedFixture(t)
	b := New(fx.s)

	data, err := b.Recent(10)
	if err != nil {
		t.Fatalf("Recent: %v", err)
	}

	var got struct {
		Items []struct {
			LogID   int64 `json:"log_id"`
			Vehicle struct {
				ID int64 `json:"id"`
			} `json:"vehicle"`
			Heat        int    `json:"heat"`
			TimestampMS int64  `json:"timestamp_ms"`
			IsMC        bool   `json:"is_mc"`
			Invalid     bool   `json:"invalid"`
			Source      string `json:"source"`
		} `json:"items"`
	}
	if err := json.Unmarshal(data, &got); err != nil {
		t.Fatalf("Unmarshal: %v\ndata=%s", err, data)
	}

	if len(got.Items) != 4 {
		t.Fatalf("expected 4 log items (deleted/unassigned excluded), got %d: %s", len(got.Items), data)
	}
	for i := 1; i < len(got.Items); i++ {
		if got.Items[i-1].TimestampMS < got.Items[i].TimestampMS {
			t.Fatalf("items not in descending timestamp_ms order: %s", data)
		}
	}
	if got.Items[0].LogID != fx.logMC {
		t.Fatalf("first (most recent) item log_id = %d, want %d (the MC run): %s", got.Items[0].LogID, fx.logMC, data)
	}
	if got.Items[0].Source != "sensor" {
		t.Fatalf("source = %q, want sensor: %s", got.Items[0].Source, data)
	}

	iceHeats := map[int]bool{}
	for _, it := range got.Items {
		if it.Heat < 1 {
			t.Fatalf("heat not assigned (want >=1) for log %d: %s", it.LogID, data)
		}
		if it.Vehicle.ID == fx.vehicleICE {
			if iceHeats[it.Heat] {
				t.Fatalf("duplicate heat %d within the ICE combo: %s", it.Heat, data)
			}
			iceHeats[it.Heat] = true
		}
	}
	if len(iceHeats) != 3 {
		t.Fatalf("expected 3 distinct heats for the ICE combo (2 normal + 1 MC), got %v: %s", iceHeats, data)
	}
}

func TestCombinationLogsRankInFilter(t *testing.T) {
	fx := seedFixture(t)
	b := New(fx.s)

	data, err := b.CombinationLogs(fx.driverActive, fx.vehicleICE, url.Values{})
	if err != nil {
		t.Fatalf("CombinationLogs: %v", err)
	}

	var got struct {
		Driver struct {
			ID   int64  `json:"id"`
			Name string `json:"name"`
		} `json:"driver"`
		Vehicle struct {
			ID     int64  `json:"id"`
			Number int    `json:"number"`
			Name   string `json:"name"`
		} `json:"vehicle"`
		Runs []struct {
			Heat         int    `json:"heat"`
			RawMS        int    `json:"raw_ms"`
			IsMC         bool   `json:"is_mc"`
			Invalid      bool   `json:"invalid"`
			RankInFilter *int   `json:"rank_in_filter"`
			Source       string `json:"source"`
		} `json:"runs"`
	}
	if err := json.Unmarshal(data, &got); err != nil {
		t.Fatalf("Unmarshal: %v\ndata=%s", err, data)
	}

	if got.Driver.ID != fx.driverActive || got.Vehicle.ID != fx.vehicleICE {
		t.Fatalf("driver/vehicle mismatch: %s", data)
	}
	if len(got.Runs) != 3 {
		t.Fatalf("expected 3 runs for the ICE combo, got %d: %s", len(got.Runs), data)
	}
	for i := 1; i < len(got.Runs); i++ {
		if got.Runs[i-1].Heat > got.Runs[i].Heat {
			t.Fatalf("runs not sorted by heat ascending: %s", data)
		}
	}
	if got.Runs[0].Source != "sensor" {
		t.Fatalf("run source = %q, want sensor (joined from LogRow): %s", got.Runs[0].Source, data)
	}

	rankByRaw := map[int]*int{}
	var mcInvalid *bool
	for _, r := range got.Runs {
		rankByRaw[r.RawMS] = r.RankInFilter
		if r.IsMC {
			v := r.Invalid
			mcInvalid = &v
		}
	}

	// Population (no filters => every combo): ICE valid runs 84310, 83456
	// plus the EV combo's 90000. 83456 is the fastest => rank 1; 84310 has
	// exactly one faster time => rank 2.
	if rank := rankByRaw[83456]; rank == nil || *rank != 1 {
		t.Fatalf("rank_in_filter for 83456 = %v, want 1: %s", derefInt(rank), data)
	}
	if rank := rankByRaw[84310]; rank == nil || *rank != 2 {
		t.Fatalf("rank_in_filter for 84310 = %v, want 2: %s", derefInt(rank), data)
	}
	if mcInvalid == nil {
		t.Fatalf("no is_mc run found in result: %s", data)
	}
	if *mcInvalid {
		if rank := rankByRaw[81000]; rank != nil {
			t.Fatalf("rank_in_filter for invalid MC run = %d, want null", *rank)
		}
	}
}

func derefInt(p *int) string {
	if p == nil {
		return "<nil>"
	}
	return strconv.Itoa(*p)
}
