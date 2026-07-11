package domain

import (
	"reflect"
	"testing"
)

func TestHeatNumbers(t *testing.T) {
	comboA := ComboKey{DriverID: 1, VehicleID: 1}
	comboB := ComboKey{DriverID: 2, VehicleID: 2}

	runs := []RunRow{
		{LogID: 10, Combo: comboA, TimestampMS: 3000},
		{LogID: 11, Combo: comboA, TimestampMS: 1000},
		{LogID: 12, Combo: comboA, TimestampMS: 2000, IsMC: true}, // MC still consumes a heat number
		{LogID: 21, Combo: comboB, TimestampMS: 500},
		{LogID: 20, Combo: comboB, TimestampMS: 500}, // tied timestamp; LogID breaks the tie
	}

	got := HeatNumbers(runs)

	want := map[int64]int{
		11: 1, // comboA earliest timestamp
		12: 2, // comboA MC run still consumes heat 2 (no gap)
		10: 3, // comboA latest timestamp
		20: 1, // comboB: timestamp tie, smaller LogID wins heat 1
		21: 2,
	}

	if !reflect.DeepEqual(got, want) {
		t.Errorf("HeatNumbers() = %v, want %v", got, want)
	}
}
