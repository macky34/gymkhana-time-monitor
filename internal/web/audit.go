package web

import (
	"encoding/json"
	"log"
	"time"
)

// audit records a best-effort audit trail entry. Failures are logged, not
// surfaced to the caller - the mutation itself already succeeded. The
// current active event's id is attached automatically (nil if none is
// active), so call sites do not each need to resolve it themselves.
func (s *Server) audit(driverID *int64, action string, detail map[string]any) {
	b, err := json.Marshal(detail)
	if err != nil {
		b = []byte("{}")
	}
	var eventID *int64
	if ev, ok, everr := s.Store.GetActiveEvent(); everr == nil && ok {
		id := ev.ID
		eventID = &id
	}
	if err := s.Store.AppendAudit(time.Now().UnixMilli(), driverID, action, string(b), eventID); err != nil {
		log.Printf("web: audit append failed action=%s: %v", action, err)
	}
}
