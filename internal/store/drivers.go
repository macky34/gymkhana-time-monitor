package store

import (
	"database/sql"
	"errors"
	"fmt"
)

// Driver mirrors a drivers row (minus the icon BLOB itself — see
// GetIcon/SetIcon).
type Driver struct {
	ID            int64
	Name          string
	DriverClassID int64
	Token         string
	Role          string
	MainVehicleID *int64
	HasIcon       bool
}

const driverSelectCols = `id, name, driver_class_id, token, role, main_vehicle_id, icon IS NOT NULL`

func scanDriver(row rowScanner) (Driver, error) {
	var d Driver
	var mainVehicleID sql.NullInt64
	var hasIcon int
	if err := row.Scan(&d.ID, &d.Name, &d.DriverClassID, &d.Token, &d.Role, &mainVehicleID, &hasIcon); err != nil {
		return Driver{}, err
	}
	if mainVehicleID.Valid {
		v := mainVehicleID.Int64
		d.MainVehicleID = &v
	}
	d.HasIcon = hasIcon != 0
	return d, nil
}

// CreateDriver inserts a new driver (registration or admin user creation)
// and returns its id.
func (s *Store) CreateDriver(name string, classID int64, token, role string) (int64, error) {
	s.writeMu.Lock()
	defer s.writeMu.Unlock()

	res, err := s.db.Exec(`INSERT INTO drivers (name, driver_class_id, token, role) VALUES (?, ?, ?, ?)`,
		name, classID, token, role)
	if err != nil {
		return 0, fmt.Errorf("store: create driver: %w", err)
	}
	id, err := res.LastInsertId()
	if err != nil {
		return 0, fmt.Errorf("store: create driver: last insert id: %w", err)
	}
	return id, nil
}

// GetDriverByToken looks up a driver by exact login-token match. This is a
// plain equality SELECT — the caller is responsible for treating "not
// found" as a bare 404 (no existence leakage), constant-time comparison is
// not needed here since tokens are indexed, high-entropy random values, not
// user-chosen secrets compared byte-by-byte.
func (s *Store) GetDriverByToken(token string) (Driver, bool, error) {
	row := s.db.QueryRow(`SELECT `+driverSelectCols+` FROM drivers WHERE token = ?`, token)
	d, err := scanDriver(row)
	if errors.Is(err, sql.ErrNoRows) {
		return Driver{}, false, nil
	}
	if err != nil {
		return Driver{}, false, fmt.Errorf("store: get driver by token: %w", err)
	}
	return d, true, nil
}

// GetDriver looks up a driver by id, regardless of is_deleted (there is
// currently no store method that ever sets is_deleted=1 on a driver; see
// final report).
func (s *Store) GetDriver(id int64) (Driver, bool, error) {
	row := s.db.QueryRow(`SELECT `+driverSelectCols+` FROM drivers WHERE id = ?`, id)
	d, err := scanDriver(row)
	if errors.Is(err, sql.ErrNoRows) {
		return Driver{}, false, nil
	}
	if err != nil {
		return Driver{}, false, fmt.Errorf("store: get driver: %w", err)
	}
	return d, true, nil
}

// ListDrivers returns all non-deleted drivers, ordered by id.
func (s *Store) ListDrivers() ([]Driver, error) {
	rows, err := s.db.Query(`SELECT ` + driverSelectCols + ` FROM drivers WHERE is_deleted = 0 ORDER BY id`)
	if err != nil {
		return nil, fmt.Errorf("store: list drivers: %w", err)
	}
	defer rows.Close()

	var out []Driver
	for rows.Next() {
		d, err := scanDriver(rows)
		if err != nil {
			return nil, fmt.Errorf("store: list drivers: scan: %w", err)
		}
		out = append(out, d)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("store: list drivers: %w", err)
	}
	return out, nil
}

// UpdateDriver changes a driver's name/class (profile edit, by self or
// admin).
func (s *Store) UpdateDriver(id int64, name string, classID int64) error {
	s.writeMu.Lock()
	defer s.writeMu.Unlock()
	_, err := s.db.Exec(`UPDATE drivers SET name = ?, driver_class_id = ? WHERE id = ?`, name, classID, id)
	if err != nil {
		return fmt.Errorf("store: update driver: %w", err)
	}
	return nil
}

// SetRole changes a driver's role ("user"/"admin"). The caller is
// responsible for the "last admin can't demote themselves" 409 rule
// (CountAdmins is provided for that check).
func (s *Store) SetRole(id int64, role string) error {
	s.writeMu.Lock()
	defer s.writeMu.Unlock()
	_, err := s.db.Exec(`UPDATE drivers SET role = ? WHERE id = ?`, role, id)
	if err != nil {
		return fmt.Errorf("store: set role: %w", err)
	}
	return nil
}

// CountAdmins returns the number of active (non-deleted) drivers with
// role='admin'.
func (s *Store) CountAdmins() (int, error) {
	var n int
	err := s.db.QueryRow(`SELECT COUNT(*) FROM drivers WHERE role = 'admin' AND is_deleted = 0`).Scan(&n)
	if err != nil {
		return 0, fmt.Errorf("store: count admins: %w", err)
	}
	return n, nil
}

// HasAdmin reports whether at least one non-deleted admin driver exists.
// Stage-2 (multi-event) gates first-run /setup on this rather than on
// GetActiveEvent: an operator may legitimately have zero active events
// between event runs (all closed, none created yet), but setup itself is a
// one-time, whole-database action that only makes sense before any admin
// account exists.
func (s *Store) HasAdmin() (bool, error) {
	var exists int
	err := s.db.QueryRow(`SELECT EXISTS(SELECT 1 FROM drivers WHERE role = 'admin' AND is_deleted = 0)`).Scan(&exists)
	if err != nil {
		return false, fmt.Errorf("store: has admin: %w", err)
	}
	return exists != 0, nil
}

// IsSeeded reports whether the one-time setup wizard has ever completed.
// class_defs rows are written only by SeedEvent and never deleted, so their
// existence is a permanent "setup done" marker (HasAdmin is not: admins can
// in principle drop to zero later, e.g. by manual DB edits).
func (s *Store) IsSeeded() (bool, error) {
	var exists int
	err := s.db.QueryRow(`SELECT EXISTS(SELECT 1 FROM class_defs)`).Scan(&exists)
	if err != nil {
		return false, fmt.Errorf("store: is seeded: %w", err)
	}
	return exists != 0, nil
}

// ReissueToken overwrites a driver's login token, immediately invalidating
// the old one.
func (s *Store) ReissueToken(id int64, newToken string) error {
	s.writeMu.Lock()
	defer s.writeMu.Unlock()
	_, err := s.db.Exec(`UPDATE drivers SET token = ? WHERE id = ?`, newToken, id)
	if err != nil {
		return fmt.Errorf("store: reissue token: %w", err)
	}
	return nil
}

// SetIcon stores a driver's icon JPEG bytes (already validated/re-encoded
// to 128x128 by the caller).
func (s *Store) SetIcon(id int64, jpeg []byte) error {
	return s.setIcon("drivers", "set icon", id, jpeg)
}

// GetIcon returns a driver's icon bytes. ok=false covers both "no such
// driver" and "driver has no icon" — either way there is nothing to serve.
func (s *Store) GetIcon(id int64) ([]byte, bool, error) {
	return s.getIcon("drivers", "get icon", id)
}

// SetMainVehicle changes a driver's main_vehicle_id.
func (s *Store) SetMainVehicle(driverID, vehicleID int64) error {
	s.writeMu.Lock()
	defer s.writeMu.Unlock()
	_, err := s.db.Exec(`UPDATE drivers SET main_vehicle_id = ? WHERE id = ?`, vehicleID, driverID)
	if err != nil {
		return fmt.Errorf("store: set main vehicle: %w", err)
	}
	return nil
}
