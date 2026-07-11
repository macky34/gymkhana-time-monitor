package domain

import "sort"

// HeatNumbers assigns a 1-based, gap-free heat number to every run within
// each (driver,vehicle) combination, ordered by timestamp_ms then LogID
// (DESIGN.md §4.4). MC runs still consume a number.
func HeatNumbers(runs []RunRow) map[int64]int {
	byCombo := make(map[ComboKey][]RunRow, len(runs))
	for _, r := range runs {
		byCombo[r.Combo] = append(byCombo[r.Combo], r)
	}

	result := make(map[int64]int, len(runs))
	for _, rs := range byCombo {
		sorted := make([]RunRow, len(rs))
		copy(sorted, rs)
		sort.Slice(sorted, func(i, j int) bool {
			if sorted[i].TimestampMS != sorted[j].TimestampMS {
				return sorted[i].TimestampMS < sorted[j].TimestampMS
			}
			return sorted[i].LogID < sorted[j].LogID
		})
		for i, r := range sorted {
			result[r.LogID] = i + 1
		}
	}
	return result
}
