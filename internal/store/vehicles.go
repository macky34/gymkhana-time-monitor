package store

import (
	"database/sql"
	"errors"
	"fmt"

	"timemon/internal/domain"
)

// Vehicle mirrors a vehicles row (minus the icon BLOB itself — see
// GetVehicleIcon/SetVehicleIcon). DisplacementCC is nil for EVs.
type Vehicle struct {
	ID                int64
	Number            int
	Name              string
	Engine            domain.EngineType
	DisplacementCC    *int
	ForcedInduction   bool
	DrivetrainClassID int64
	HasIcon           bool
}

const vehicleSelectCols = `id, number, name, engine_type, displacement_cc, forced_induction, drivetrain_class_id, icon IS NOT NULL`

func scanVehicle(row rowScanner) (Vehicle, error) {
	var v Vehicle
	var engine string
	var dispCC sql.NullInt64
	var fi int
	var hasIcon int
	if err := row.Scan(&v.ID, &v.Number, &v.Name, &engine, &dispCC, &fi, &v.DrivetrainClassID, &hasIcon); err != nil {
		return Vehicle{}, err
	}
	v.Engine = domain.EngineType(engine)
	if dispCC.Valid {
		cc := int(dispCC.Int64)
		v.DisplacementCC = &cc
	}
	v.ForcedInduction = fi != 0
	v.HasIcon = hasIcon != 0
	return v, nil
}

// CreateVehicle inserts a new vehicle and returns its id.
func (s *Store) CreateVehicle(v Vehicle) (int64, error) {
	s.writeMu.Lock()
	defer s.writeMu.Unlock()

	res, err := s.db.Exec(`INSERT INTO vehicles (number, name, engine_type, displacement_cc, forced_induction, drivetrain_class_id)
		VALUES (?, ?, ?, ?, ?, ?)`,
		v.Number, v.Name, string(v.Engine), nullableInt(v.DisplacementCC), boolToInt(v.ForcedInduction), v.DrivetrainClassID)
	if err != nil {
		return 0, fmt.Errorf("store: create vehicle: %w", err)
	}
	id, err := res.LastInsertId()
	if err != nil {
		return 0, fmt.Errorf("store: create vehicle: last insert id: %w", err)
	}
	return id, nil
}

// UpdateVehicle overwrites a vehicle's fields (v.ID selects the row).
func (s *Store) UpdateVehicle(v Vehicle) error {
	s.writeMu.Lock()
	defer s.writeMu.Unlock()

	_, err := s.db.Exec(`UPDATE vehicles SET number=?, name=?, engine_type=?, displacement_cc=?, forced_induction=?, drivetrain_class_id=?
		WHERE id=?`,
		v.Number, v.Name, string(v.Engine), nullableInt(v.DisplacementCC), boolToInt(v.ForcedInduction), v.DrivetrainClassID, v.ID)
	if err != nil {
		return fmt.Errorf("store: update vehicle: %w", err)
	}
	return nil
}

// DeleteVehicle soft-deletes a vehicle (is_deleted=1). Existing logs keep
// referencing it by id so historical rankings/CSV stay correct; GetVehicle
// therefore does not filter on is_deleted.
func (s *Store) DeleteVehicle(id int64) error {
	s.writeMu.Lock()
	defer s.writeMu.Unlock()
	_, err := s.db.Exec(`UPDATE vehicles SET is_deleted = 1 WHERE id = ?`, id)
	if err != nil {
		return fmt.Errorf("store: delete vehicle: %w", err)
	}
	return nil
}

// GetVehicle looks up a vehicle by id regardless of is_deleted, so that
// past logs referencing a since-deleted vehicle can still be rendered
// (ranking/CSV/log history all resolve driver/vehicle by id).
func (s *Store) GetVehicle(id int64) (Vehicle, bool, error) {
	row := s.db.QueryRow(`SELECT `+vehicleSelectCols+` FROM vehicles WHERE id = ?`, id)
	v, err := scanVehicle(row)
	if errors.Is(err, sql.ErrNoRows) {
		return Vehicle{}, false, nil
	}
	if err != nil {
		return Vehicle{}, false, fmt.Errorf("store: get vehicle: %w", err)
	}
	return v, true, nil
}

// ListVehicles returns all non-deleted vehicles, ordered by id.
func (s *Store) ListVehicles() ([]Vehicle, error) {
	rows, err := s.db.Query(`SELECT ` + vehicleSelectCols + ` FROM vehicles WHERE is_deleted = 0 ORDER BY id`)
	if err != nil {
		return nil, fmt.Errorf("store: list vehicles: %w", err)
	}
	defer rows.Close()

	var out []Vehicle
	for rows.Next() {
		v, err := scanVehicle(rows)
		if err != nil {
			return nil, fmt.Errorf("store: list vehicles: scan: %w", err)
		}
		out = append(out, v)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("store: list vehicles: %w", err)
	}
	return out, nil
}

// NumberInUse reports whether an active (non-deleted) vehicle other than
// excludeID already uses number. Used for the (non-blocking) duplicate
// number warning; pass excludeID=0 when checking a brand-new vehicle.
func (s *Store) NumberInUse(number int, excludeID int64) (bool, error) {
	var n int
	err := s.db.QueryRow(`SELECT COUNT(*) FROM vehicles WHERE number = ? AND id != ? AND is_deleted = 0`,
		number, excludeID).Scan(&n)
	if err != nil {
		return false, fmt.Errorf("store: number in use: %w", err)
	}
	return n > 0, nil
}

// AddEntry links a driver and vehicle (carpool / additional vehicle). It is
// idempotent: adding an already-existing link is a no-op success rather
// than a constraint error, since re-establishing an existing link is not a
// meaningful failure for callers (registration "join existing vehicle" and
// POST /api/my/vehicles both treat it as immediate success).
//
// Within the same transaction, if driverID currently has no main_vehicle_id
// (NULL), it is set to vehicleID: a driver's first linked vehicle becomes
// their main vehicle automatically, so registering one vehicle and then
// adding more later does not leave main_vehicle_id unset. This never
// overwrites an already-set main_vehicle_id, so it does not conflict with
// explicit SetMainVehicle callers (register.go, PUT /api/my/main-vehicle) —
// it only ever fires the first time a driver gains a vehicle link.
func (s *Store) AddEntry(driverID, vehicleID int64) error {
	s.writeMu.Lock()
	defer s.writeMu.Unlock()

	tx, err := s.db.Begin()
	if err != nil {
		return fmt.Errorf("store: add entry: begin: %w", err)
	}
	defer tx.Rollback()

	if _, err := tx.Exec(`INSERT OR IGNORE INTO entries (driver_id, vehicle_id) VALUES (?, ?)`, driverID, vehicleID); err != nil {
		return fmt.Errorf("store: add entry: %w", err)
	}
	if _, err := tx.Exec(`UPDATE drivers SET main_vehicle_id = ? WHERE id = ? AND main_vehicle_id IS NULL`, vehicleID, driverID); err != nil {
		return fmt.Errorf("store: add entry: set main vehicle: %w", err)
	}

	if err := tx.Commit(); err != nil {
		return fmt.Errorf("store: add entry: commit: %w", err)
	}
	return nil
}

// DeleteEntry removes a driver/vehicle link. It never touches logs —
// historical runs remain attributed to (driver_id, vehicle_id) regardless
// of whether the entries link still exists.
func (s *Store) DeleteEntry(driverID, vehicleID int64) error {
	s.writeMu.Lock()
	defer s.writeMu.Unlock()
	_, err := s.db.Exec(`DELETE FROM entries WHERE driver_id = ? AND vehicle_id = ?`, driverID, vehicleID)
	if err != nil {
		return fmt.Errorf("store: delete entry: %w", err)
	}
	return nil
}

// ListEntriesByDriver returns the active (non-deleted) vehicles linked to a
// driver.
func (s *Store) ListEntriesByDriver(driverID int64) ([]Vehicle, error) {
	rows, err := s.db.Query(`SELECT v.id, v.number, v.name, v.engine_type, v.displacement_cc, v.forced_induction, v.drivetrain_class_id, v.icon IS NOT NULL
		FROM vehicles v
		JOIN entries e ON e.vehicle_id = v.id
		WHERE e.driver_id = ? AND v.is_deleted = 0
		ORDER BY v.id`, driverID)
	if err != nil {
		return nil, fmt.Errorf("store: list entries by driver: %w", err)
	}
	defer rows.Close()

	var out []Vehicle
	for rows.Next() {
		v, err := scanVehicle(rows)
		if err != nil {
			return nil, fmt.Errorf("store: list entries by driver: scan: %w", err)
		}
		out = append(out, v)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("store: list entries by driver: %w", err)
	}
	return out, nil
}

// ListDriversByVehicle returns the active (non-deleted) drivers linked to a
// vehicle.
func (s *Store) ListDriversByVehicle(vehicleID int64) ([]Driver, error) {
	rows, err := s.db.Query(`SELECT d.id, d.name, d.driver_class_id, d.token, d.role, d.main_vehicle_id, d.icon IS NOT NULL
		FROM drivers d
		JOIN entries e ON e.driver_id = d.id
		WHERE e.vehicle_id = ? AND d.is_deleted = 0
		ORDER BY d.id`, vehicleID)
	if err != nil {
		return nil, fmt.Errorf("store: list drivers by vehicle: %w", err)
	}
	defer rows.Close()

	var out []Driver
	for rows.Next() {
		d, err := scanDriver(rows)
		if err != nil {
			return nil, fmt.Errorf("store: list drivers by vehicle: scan: %w", err)
		}
		out = append(out, d)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("store: list drivers by vehicle: %w", err)
	}
	return out, nil
}

// SetVehicleIcon stores a vehicle's icon JPEG bytes (already validated/
// re-encoded to 128x128 by the caller).
func (s *Store) SetVehicleIcon(id int64, jpeg []byte) error {
	return s.setIcon("vehicles", "set vehicle icon", id, jpeg)
}

// GetVehicleIcon returns a vehicle's icon bytes. ok=false covers both "no
// such vehicle" and "vehicle has no icon" — either way there is nothing to
// serve.
func (s *Store) GetVehicleIcon(id int64) ([]byte, bool, error) {
	return s.getIcon("vehicles", "get vehicle icon", id)
}
