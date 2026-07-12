package web

import (
	"encoding/csv"
	"encoding/json"
	"fmt"
	"net/http"
	"strconv"

	"timemon/internal/store"
)

func adminWriteCSVHeader(w http.ResponseWriter, filename string) {
	w.Header().Set("Content-Type", "text/csv; charset=utf-8")
	w.Header().Set("Content-Disposition", `attachment; filename="`+filename+`"`)
}

// adminFmtMS formats milliseconds as "m:ss.mmm".
func adminFmtMS(ms int) string {
	if ms < 0 {
		ms = 0
	}
	return fmt.Sprintf("%d:%02d.%03d", ms/60000, (ms%60000)/1000, ms%1000)
}

// handleAdminExport implements GET /api/admin/export?type=ranking|combination|logs.
func (s *Server) handleAdminExport(w http.ResponseWriter, r *http.Request, admin store.Driver) {
	switch r.URL.Query().Get("type") {
	case "ranking":
		s.adminExportRanking(w)
	case "combination":
		s.adminExportCombination(w, r)
	case "logs":
		s.adminExportLogs(w)
	default:
		writeJSONError(w, http.StatusBadRequest, "invalid type")
	}
}

// The CSV exporters decode the exact snapshot JSON shapes (docs/CONTRACTS.md
// §3) into typed structs rather than guessing key names — the snapshot layer
// and these exporters share one frozen contract.

type csvRankingResp struct {
	Rows []csvRankingRow `json:"rows"`
}

type csvRankingRow struct {
	Driver      refID          `json:"driver"`
	DriverClass string         `json:"driver_class"`
	Vehicle     csvRankVehicle `json:"vehicle"`
	BestMS      *int           `json:"best_ms"`
	SecondMS    *int           `json:"second_ms"`
	Runs        int            `json:"runs"`
	PTTotal     int            `json:"pt_total"`
	Invalid     bool           `json:"invalid"`
}

type refID struct {
	ID   int64  `json:"id"`
	Name string `json:"name"`
}

type csvRankVehicle struct {
	Number      int    `json:"number"`
	Name        string `json:"name"`
	ConvertedCC *int   `json:"converted_cc"`
	DispClass   string `json:"disp_class"`
	DTClass     string `json:"dt_class"`
}

// adminExportRanking writes the current ranking snapshot as UTF-8 BOM CSV.
func (s *Server) adminExportRanking(w http.ResponseWriter) {
	raw, err := s.Snap.Ranking()
	if err != nil {
		writeErr(w, err)
		return
	}
	var resp csvRankingResp
	if err := json.Unmarshal(raw, &resp); err != nil {
		writeErr(w, err)
		return
	}

	adminWriteCSVHeader(w, "ranking.csv")
	_, _ = w.Write([]byte{0xEF, 0xBB, 0xBF})
	cw := csv.NewWriter(w)
	_ = cw.Write([]string{"順位", "ドライバー", "区分", "号車", "車両", "換算cc", "排気量クラス", "駆動", "ベスト", "セカンド", "走行本数", "PT計", "状態"})

	rank := 0
	for _, row := range resp.Rows {
		pos := ""
		state := ""
		if row.Invalid {
			state = "無効" // trailing unranked group
		} else {
			rank++
			pos = strconv.Itoa(rank)
		}
		convertedCC := ""
		if row.Vehicle.ConvertedCC != nil {
			convertedCC = strconv.Itoa(*row.Vehicle.ConvertedCC)
		}
		best, second := "", ""
		if row.BestMS != nil {
			best = adminFmtMS(*row.BestMS)
		}
		if row.SecondMS != nil {
			second = adminFmtMS(*row.SecondMS)
		}
		_ = cw.Write([]string{
			pos,
			row.Driver.Name,
			row.DriverClass,
			strconv.Itoa(row.Vehicle.Number),
			row.Vehicle.Name,
			convertedCC,
			row.Vehicle.DispClass,
			row.Vehicle.DTClass,
			best,
			second,
			strconv.Itoa(row.Runs),
			strconv.Itoa(row.PTTotal),
			state,
		})
	}
	cw.Flush()
}

type csvComboResp struct {
	Runs []csvComboRun `json:"runs"`
}

type csvComboRun struct {
	Heat         int  `json:"heat"`
	RawMS        int  `json:"raw_ms"`
	PTCount      int  `json:"pt_count"`
	FinalMS      int  `json:"final_ms"`
	Invalid      bool `json:"invalid"`
	RankInFilter *int `json:"rank_in_filter"`
}

// adminExportCombination writes every run for one driver/vehicle combo as
// UTF-8 BOM CSV (heat order), including each run's rank within the current
// filter.
func (s *Server) adminExportCombination(w http.ResponseWriter, r *http.Request) {
	dq := r.URL.Query().Get("driver_id")
	vq := r.URL.Query().Get("vehicle_id")
	if dq == "" || vq == "" {
		writeJSONError(w, http.StatusBadRequest, "driver_id and vehicle_id are required")
		return
	}
	driverID, err := strconv.ParseInt(dq, 10, 64)
	if err != nil {
		writeJSONError(w, http.StatusBadRequest, "invalid driver_id")
		return
	}
	vehicleID, err := strconv.ParseInt(vq, 10, 64)
	if err != nil {
		writeJSONError(w, http.StatusBadRequest, "invalid vehicle_id")
		return
	}

	raw, err := s.Snap.CombinationLogs(driverID, vehicleID, r.URL.Query())
	if err != nil {
		writeErr(w, err)
		return
	}
	var resp csvComboResp
	if err := json.Unmarshal(raw, &resp); err != nil {
		writeErr(w, err)
		return
	}

	adminWriteCSVHeader(w, "combination.csv")
	_, _ = w.Write([]byte{0xEF, 0xBB, 0xBF})
	cw := csv.NewWriter(w)
	_ = cw.Write([]string{"heat", "raw", "pt", "final", "状態", "フィルタ内順位"})

	for _, run := range resp.Runs {
		state := ""
		if run.Invalid {
			state = "無効"
		}
		rankInFilter := ""
		if run.RankInFilter != nil {
			rankInFilter = strconv.Itoa(*run.RankInFilter)
		}
		_ = cw.Write([]string{
			strconv.Itoa(run.Heat),
			adminFmtMS(run.RawMS),
			strconv.Itoa(run.PTCount),
			adminFmtMS(run.FinalMS),
			state,
			rankInFilter,
		})
	}
	cw.Flush()
}

// adminExportLogs writes every raw timing log row (deleted/unassigned
// included) as UTF-8 BOM CSV.
func (s *Server) adminExportLogs(w http.ResponseWriter) {
	logs, _, err := s.Store.ListLogs(1<<30, 0)
	if err != nil {
		writeErr(w, err)
		return
	}

	adminWriteCSVHeader(w, "logs.csv")
	_, _ = w.Write([]byte{0xEF, 0xBB, 0xBF})
	cw := csv.NewWriter(w)
	_ = cw.Write([]string{"id", "driver_id", "vehicle_id", "raw_ms", "pt_count", "is_mc", "timestamp_ms", "source", "edited_at", "is_deleted"})

	for _, l := range logs {
		driverID, vehicleID, editedAt := "", "", ""
		if l.DriverID != nil {
			driverID = strconv.FormatInt(*l.DriverID, 10)
		}
		if l.VehicleID != nil {
			vehicleID = strconv.FormatInt(*l.VehicleID, 10)
		}
		if l.EditedAt != nil {
			editedAt = strconv.FormatInt(*l.EditedAt, 10)
		}
		_ = cw.Write([]string{
			strconv.FormatInt(l.ID, 10),
			driverID,
			vehicleID,
			strconv.Itoa(l.RawMS),
			strconv.Itoa(l.PTCount),
			strconv.FormatBool(l.IsMC),
			strconv.FormatInt(l.TimestampMS, 10),
			l.Source,
			editedAt,
			strconv.FormatBool(l.IsDeleted),
		})
	}
	cw.Flush()
}
