package web

import (
	"net/http"

	"timemon/internal/domain"
	"timemon/internal/store"
)

type driverRef struct {
	ID   int64  `json:"id"`
	Name string `json:"name"`
}

type vehicleOut struct {
	ID              int64       `json:"id"`
	Number          int         `json:"number"`
	Name            string      `json:"name"`
	Engine          string      `json:"engine"`
	DisplacementCC  *int        `json:"displacement_cc"`
	ForcedInduction bool        `json:"forced_induction"`
	ConvertedCC     *int        `json:"converted_cc"`
	DispClass       string      `json:"disp_class"`
	DTClass         string      `json:"dt_class"`
	Drivers         []driverRef `json:"drivers"`
}

// loadVehicleContext loads the pieces needed to project a store.Vehicle
// into a vehicleOut: engine-conversion coefficients + displacement class
// table (from settings) and a drivetrain-class-id -> label map.
func (s *Server) loadVehicleContext() (domain.Coefficients, []domain.DispClass, map[int64]string, error) {
	var coef domain.Coefficients
	var dispClasses []domain.DispClass

	set, ok, err := s.Store.GetSettings()
	if err != nil {
		return coef, dispClasses, nil, err
	}
	if ok {
		coef = set.Coef
		dispClasses = set.DispClasses
	}

	dtClasses, err := s.Store.ListClassDefs("drivetrain")
	if err != nil {
		return coef, dispClasses, nil, err
	}
	dtLabel := make(map[int64]string, len(dtClasses))
	for _, c := range dtClasses {
		dtLabel[c.ID] = c.Label
	}
	return coef, dispClasses, dtLabel, nil
}

func (s *Server) buildVehicleOut(v store.Vehicle, coef domain.Coefficients, dispClasses []domain.DispClass, dtLabel map[int64]string) (vehicleOut, error) {
	cc := 0
	if v.DisplacementCC != nil {
		cc = *v.DisplacementCC
	}
	conv, ok := domain.ConvertedCC(cc, v.Engine, v.ForcedInduction, coef)
	var convPtr *int
	if ok {
		c := conv
		convPtr = &c
	}
	dispClass := domain.DispClassOf(conv, ok, dispClasses)

	drivers, err := s.Store.ListDriversByVehicle(v.ID)
	if err != nil {
		return vehicleOut{}, err
	}
	drefs := make([]driverRef, 0, len(drivers))
	for _, dr := range drivers {
		drefs = append(drefs, driverRef{ID: dr.ID, Name: dr.Name})
	}

	return vehicleOut{
		ID:              v.ID,
		Number:          v.Number,
		Name:            v.Name,
		Engine:          string(v.Engine),
		DisplacementCC:  v.DisplacementCC,
		ForcedInduction: v.ForcedInduction,
		ConvertedCC:     convPtr,
		DispClass:       dispClass,
		DTClass:         dtLabel[v.DrivetrainClassID],
		Drivers:         drefs,
	}, nil
}

func (s *Server) handleAPIDrivers(w http.ResponseWriter, r *http.Request) {
	drivers, err := s.Store.ListDrivers()
	if err != nil {
		writeJSONError(w, http.StatusInternalServerError, err.Error())
		return
	}
	classes, err := s.Store.ListClassDefs("driver")
	if err != nil {
		writeJSONError(w, http.StatusInternalServerError, err.Error())
		return
	}
	classLabel := make(map[int64]string, len(classes))
	for _, c := range classes {
		classLabel[c.ID] = c.Label
	}

	type driverOut struct {
		ID          int64  `json:"id"`
		Name        string `json:"name"`
		DriverClass string `json:"driver_class"`
		HasIcon     bool   `json:"has_icon"`
	}
	out := make([]driverOut, 0, len(drivers))
	for _, d := range drivers {
		out = append(out, driverOut{
			ID:          d.ID,
			Name:        d.Name,
			DriverClass: classLabel[d.DriverClassID],
			HasIcon:     d.HasIcon,
		})
	}
	writeJSON(w, http.StatusOK, map[string]any{"drivers": out})
}

func (s *Server) handleAPIVehicles(w http.ResponseWriter, r *http.Request) {
	vehicles, err := s.Store.ListVehicles()
	if err != nil {
		writeJSONError(w, http.StatusInternalServerError, err.Error())
		return
	}
	coef, dispClasses, dtLabel, err := s.loadVehicleContext()
	if err != nil {
		writeJSONError(w, http.StatusInternalServerError, err.Error())
		return
	}

	out := make([]vehicleOut, 0, len(vehicles))
	for _, v := range vehicles {
		vo, err := s.buildVehicleOut(v, coef, dispClasses, dtLabel)
		if err != nil {
			writeJSONError(w, http.StatusInternalServerError, err.Error())
			return
		}
		out = append(out, vo)
	}
	writeJSON(w, http.StatusOK, map[string]any{"vehicles": out})
}

// handleDriverIcon implements GET /api/drivers/{id}/icon with ETag-based
// conditional GET support.
func (s *Server) handleDriverIcon(w http.ResponseWriter, r *http.Request) {
	id, err := parsePathInt64(r, "id")
	if err != nil {
		http.NotFound(w, r)
		return
	}
	data, ok, err := s.Store.GetIcon(id)
	if err != nil {
		writeJSONError(w, http.StatusInternalServerError, err.Error())
		return
	}
	if !ok {
		http.NotFound(w, r)
		return
	}

	etag := etagFor(data)
	if r.Header.Get("If-None-Match") == etag {
		w.WriteHeader(http.StatusNotModified)
		return
	}
	w.Header().Set("Content-Type", "image/jpeg")
	w.Header().Set("ETag", etag)
	w.Header().Set("Cache-Control", "public, max-age=86400")
	_, _ = w.Write(data)
}
