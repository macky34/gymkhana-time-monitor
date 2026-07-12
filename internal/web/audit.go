package web

import (
	"encoding/json"
	"log"
	"time"
)

// audit records a best-effort audit trail entry. Failures are logged, not
// surfaced to the caller - the mutation itself already succeeded.
func (s *Server) audit(driverID *int64, action string, detail map[string]any) {
	b, err := json.Marshal(detail)
	if err != nil {
		b = []byte("{}")
	}
	if err := s.Store.AppendAudit(time.Now().UnixMilli(), driverID, action, string(b)); err != nil {
		log.Printf("web: audit append failed action=%s: %v", action, err)
	}
}
