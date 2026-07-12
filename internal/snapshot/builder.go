// Package snapshot builds the JSON payloads served over HTTP and pushed
// through the SSE hub (package sse) from the current contents of the
// SQLite-backed store (package store). Every exported method reads the
// store fresh and returns a ready-to-send JSON document; callers decide
// whether that document is served directly or handed to sse.Hub.Publish
// (see the Publish* helpers in publish.go).
package snapshot

import (
	"timemon/internal/domain"
	"timemon/internal/store"
)

// FinishProvider reports the in-flight finish for a queue row (a car that
// has crossed the line but is still inside the confirmation grace window).
// ok is false when the row has no pending finish. The web layer's course
// manager supplies one via Builder.SetFinishProvider so OnCourse snapshots
// can render the "finish" object without snapshot depending on package web.
type FinishProvider func(queueID int64) (finMS int, untilMS int64, ok bool)

// Builder generates topic snapshots from a *store.Store.
type Builder struct {
	s        *store.Store
	finishFn FinishProvider
}

// New creates a Builder reading from s.
func New(s *store.Store) *Builder {
	return &Builder{s: s}
}

// SetFinishProvider registers the callback OnCourse uses to embed pending
// finish info. Passing nil (the default) makes every car's "finish" null.
func (b *Builder) SetFinishProvider(fn FinishProvider) {
	b.finishFn = fn
}

// refDriver is the minimal driver reference embedded in every snapshot.
type refDriver struct {
	ID   int64  `json:"id"`
	Name string `json:"name"`
}

// refVehicleBasic is the minimal vehicle reference embedded in snapshots
// that do not need engine/class detail (queue, on_course, recent,
// combination logs). Ranking uses the richer rankVehicle instead.
type refVehicleBasic struct {
	ID     int64  `json:"id"`
	Number int    `json:"number"`
	Name   string `json:"name"`
}

func indexDrivers(drivers []store.Driver) map[int64]store.Driver {
	m := make(map[int64]store.Driver, len(drivers))
	for _, d := range drivers {
		m[d.ID] = d
	}
	return m
}

func indexVehicles(vehicles []store.Vehicle) map[int64]store.Vehicle {
	m := make(map[int64]store.Vehicle, len(vehicles))
	for _, v := range vehicles {
		m[v.ID] = v
	}
	return m
}

func indexClassLabels(defs []store.ClassDef) map[int64]string {
	m := make(map[int64]string, len(defs))
	for _, c := range defs {
		m[c.ID] = c.Label
	}
	return m
}

// evEngine is the domain.EngineType value identifying an electric vehicle.
const evEngine domain.EngineType = "ev"

// vehicleConv holds the displacement-conversion result for one vehicle: cc
// is the converted displacement, ok mirrors domain.ConvertedCC's second
// return value (false for EV, per its documented contract), and dispClass
// is the pre-computed domain.DispClassOf label ("EV" when ok is false).
type vehicleConv struct {
	cc        int
	ok        bool
	dispClass string
}

func buildVehicleConv(vehicles []store.Vehicle, coef domain.Coefficients, dispClasses []domain.DispClass) map[int64]vehicleConv {
	m := make(map[int64]vehicleConv, len(vehicles))
	for _, v := range vehicles {
		cc := 0
		if v.DisplacementCC != nil {
			cc = *v.DisplacementCC
		}
		conv, ok := domain.ConvertedCC(cc, v.Engine, v.ForcedInduction, coef)
		m[v.ID] = vehicleConv{
			cc:        conv,
			ok:        ok,
			dispClass: domain.DispClassOf(conv, ok, dispClasses),
		}
	}
	return m
}
