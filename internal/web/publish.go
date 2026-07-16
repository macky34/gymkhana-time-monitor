// Package web: best-effort snapshot publish helpers (errors are logged, not
// surfaced - the DB mutation that triggered them already succeeded).
package web

import "log"

func (s *Server) publishQueue() {
	if err := s.Snap.PublishQueue(s.Hub); err != nil {
		log.Printf("web: publish queue failed: %v", err)
	}
}

func (s *Server) publishOnCourse() {
	if err := s.Snap.PublishOnCourse(s.Hub); err != nil {
		log.Printf("web: publish on_course failed: %v", err)
	}
}

func (s *Server) publishRanking() {
	if err := s.Snap.PublishRanking(s.Hub); err != nil {
		log.Printf("web: publish ranking failed: %v", err)
	}
}

// publishDirectory notifies subscribers that the driver/vehicle directory
// (users, vehicles, or their linkage) changed, so the admin page knows to
// refetch /api/admin/users and /api/vehicles.
func (s *Server) publishDirectory() {
	if err := s.Snap.PublishDirectory(s.Hub); err != nil {
		log.Printf("web: publish directory failed: %v", err)
	}
}

// publishAll regenerates and pushes ranking, queue, on_course, and settings
// in one call (snapshot.Builder.PublishAll) - used where a single mutation
// invalidates several public-facing snapshots at once (e.g. an icon change
// touches the driver/vehicle references embedded in each of them).
func (s *Server) publishAll() {
	if err := s.Snap.PublishAll(s.Hub); err != nil {
		log.Printf("web: publish all failed: %v", err)
	}
}
