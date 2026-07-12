package web

import (
	"encoding/base64"
	"encoding/json"
	"net/http"

	qrcode "github.com/skip2/go-qrcode"

	"timemon/internal/store"
)

// adminDriverClassLabels returns a map from driver ClassDef.ID to Label
// (axis "driver"), used to render the human-readable driver_class string in
// the admin user list.
func (s *Server) adminDriverClassLabels() (map[int64]string, error) {
	defs, err := s.Store.ListClassDefs("driver")
	if err != nil {
		return nil, err
	}
	out := make(map[int64]string, len(defs))
	for _, d := range defs {
		out[d.ID] = d.Label
	}
	return out, nil
}

type adminUserOut struct {
	ID          int64  `json:"id"`
	Name        string `json:"name"`
	DriverClass string `json:"driver_class"`
	Role        string `json:"role"`
	Number      any    `json:"number"`
	LoginURL    string `json:"login_url"`
}

// handleAdminUsersList implements GET /api/admin/users.
func (s *Server) handleAdminUsersList(w http.ResponseWriter, r *http.Request, admin store.Driver) {
	drivers, err := s.Store.ListDrivers()
	if err != nil {
		writeErr(w, err)
		return
	}
	labels, err := s.adminDriverClassLabels()
	if err != nil {
		writeErr(w, err)
		return
	}

	out := make([]adminUserOut, 0, len(drivers))
	for _, d := range drivers {
		var number any
		if d.MainVehicleID != nil {
			if v, ok, verr := s.Store.GetVehicle(*d.MainVehicleID); verr == nil && ok {
				number = v.Number
			}
		}
		out = append(out, adminUserOut{
			ID:          d.ID,
			Name:        d.Name,
			DriverClass: labels[d.DriverClassID],
			Role:        d.Role,
			Number:      number,
			LoginURL:    s.BaseURL + "/a/" + d.Token,
		})
	}
	writeJSON(w, http.StatusOK, map[string]any{"users": out})
}

type adminUserCreateBody struct {
	Name          string `json:"name"`
	DriverClassID int64  `json:"driver_class_id"`
}

// handleAdminUserCreate implements POST /api/admin/users: registers a new
// driver with role "user" and a fresh login token.
func (s *Server) handleAdminUserCreate(w http.ResponseWriter, r *http.Request, admin store.Driver) {
	var body adminUserCreateBody
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeJSONError(w, http.StatusBadRequest, "invalid body")
		return
	}

	tok, err := randToken()
	if err != nil {
		writeErr(w, err)
		return
	}
	id, err := s.Store.CreateDriver(body.Name, body.DriverClassID, tok, "user")
	if err != nil {
		writeErr(w, err)
		return
	}
	loginURL := s.BaseURL + "/a/" + tok

	s.audit(&admin.ID, "admin.user.create", map[string]any{
		"driver_id":       id,
		"name":            body.Name,
		"driver_class_id": body.DriverClassID,
	})

	writeJSON(w, http.StatusOK, map[string]any{"driver_id": id, "login_url": loginURL})
}

type adminUserUpdateBody struct {
	Name          string `json:"name"`
	DriverClassID int64  `json:"driver_class_id"`
}

// handleAdminUserUpdate implements PUT /api/admin/users/{id}.
func (s *Server) handleAdminUserUpdate(w http.ResponseWriter, r *http.Request, admin store.Driver) {
	id, err := parsePathInt64(r, "id")
	if err != nil {
		writeJSONError(w, http.StatusBadRequest, "invalid id")
		return
	}
	var body adminUserUpdateBody
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeJSONError(w, http.StatusBadRequest, "invalid body")
		return
	}

	if err := s.Store.UpdateDriver(id, body.Name, body.DriverClassID); err != nil {
		writeErr(w, err)
		return
	}

	s.audit(&admin.ID, "admin.user.update", map[string]any{
		"driver_id":       id,
		"name":            body.Name,
		"driver_class_id": body.DriverClassID,
	})

	writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
}

// handleAdminUserReissue implements POST /api/admin/users/{id}/reissue:
// issues a brand new login token for the driver and returns both the login
// URL and a ready-to-print QR PNG for it.
func (s *Server) handleAdminUserReissue(w http.ResponseWriter, r *http.Request, admin store.Driver) {
	id, err := parsePathInt64(r, "id")
	if err != nil {
		writeJSONError(w, http.StatusBadRequest, "invalid id")
		return
	}

	tok, err := randToken()
	if err != nil {
		writeErr(w, err)
		return
	}
	if err := s.Store.ReissueToken(id, tok); err != nil {
		writeErr(w, err)
		return
	}
	loginURL := s.BaseURL + "/a/" + tok

	png, err := qrcode.Encode(loginURL, qrcode.Medium, 256)
	if err != nil {
		writeErr(w, err)
		return
	}

	s.audit(&admin.ID, "admin.user.reissue", map[string]any{"driver_id": id})

	writeJSON(w, http.StatusOK, map[string]any{
		"login_url":  loginURL,
		"qr_png_b64": base64.StdEncoding.EncodeToString(png),
	})
}

type adminUserRoleBody struct {
	Role string `json:"role"`
}

// handleAdminUserRole implements PUT /api/admin/users/{id}/role: promotes or
// demotes a driver between "user" and "admin", refusing to strip admin role
// from the last remaining admin.
func (s *Server) handleAdminUserRole(w http.ResponseWriter, r *http.Request, admin store.Driver) {
	id, err := parsePathInt64(r, "id")
	if err != nil {
		writeJSONError(w, http.StatusBadRequest, "invalid id")
		return
	}
	var body adminUserRoleBody
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeJSONError(w, http.StatusBadRequest, "invalid body")
		return
	}
	if body.Role != "admin" && body.Role != "user" {
		writeJSONError(w, http.StatusBadRequest, "invalid role")
		return
	}

	if body.Role == "user" {
		target, ok, err := s.Store.GetDriver(id)
		if err != nil {
			writeErr(w, err)
			return
		}
		if !ok {
			writeJSONError(w, http.StatusNotFound, "not found")
			return
		}
		if target.Role == "admin" {
			n, err := s.Store.CountAdmins()
			if err != nil {
				writeErr(w, err)
				return
			}
			if n <= 1 {
				writeErr(w, conflictf("cannot remove last admin"))
				return
			}
		}
	}

	if err := s.Store.SetRole(id, body.Role); err != nil {
		writeErr(w, err)
		return
	}

	s.audit(&admin.ID, "admin.user.role", map[string]any{"driver_id": id, "role": body.Role})

	writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
}
