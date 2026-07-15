// Package sse implements a minimal topic-based Server-Sent Events hub.
//
// The hub keeps the latest published payload for each topic ("snapshot") and
// fans out every Publish call to all currently connected subscribers of that
// topic. It is intentionally unaware of what the topics mean or what shape
// the JSON payloads have — that is the responsibility of package snapshot.
package sse

import (
	"compress/gzip"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"sync"
	"time"
)

// Topic names understood by Handler. Any other value in the "topics" query
// parameter is silently ignored.
const (
	TopicRanking      = "ranking"
	TopicOnCourse     = "on_course"
	TopicQueue        = "queue"
	TopicSensorStatus = "sensor_status"
	TopicOrphan       = "orphan"
	TopicSettings     = "settings"
	TopicTime         = "time"
	TopicDirectory    = "directory"
)

var knownTopics = map[string]bool{
	TopicRanking:      true,
	TopicOnCourse:     true,
	TopicQueue:        true,
	TopicSensorStatus: true,
	TopicOrphan:       true,
	TopicSettings:     true,
	TopicTime:         true,
	TopicDirectory:    true,
}

// subscriberBufferSize is how many pending events a subscriber may have
// queued before it is considered slow and disconnected.
const subscriberBufferSize = 16

type event struct {
	topic string
	data  []byte
}

// subscriber represents one connected SSE client.
type subscriber struct {
	ch     chan event
	topics map[string]bool

	kickOnce sync.Once
	kicked   chan struct{}
}

func newSubscriber(topics []string) *subscriber {
	s := &subscriber{
		ch:     make(chan event, subscriberBufferSize),
		topics: make(map[string]bool, len(topics)),
		kicked: make(chan struct{}),
	}
	for _, t := range topics {
		s.topics[t] = true
	}
	return s
}

func (s *subscriber) kick() {
	s.kickOnce.Do(func() { close(s.kicked) })
}

// Hub is a topic-based publish/subscribe broker for Server-Sent Events.
// The zero value is not usable; construct with NewHub.
type Hub struct {
	mu          sync.Mutex
	snapshots   map[string][]byte
	subscribers map[*subscriber]bool
}

// NewHub creates an empty, ready-to-use Hub.
func NewHub() *Hub {
	return &Hub{
		snapshots:   make(map[string][]byte),
		subscribers: make(map[*subscriber]bool),
	}
}

// Publish replaces the current snapshot for topic and immediately delivers
// data to every subscriber currently subscribed to topic. A subscriber whose
// buffer is already full is disconnected rather than allowed to block the
// caller — one stuck client must never stop delivery to everyone else.
func (h *Hub) Publish(topic string, data []byte) {
	h.mu.Lock()
	h.snapshots[topic] = data
	var targets []*subscriber
	for s := range h.subscribers {
		if s.topics[topic] {
			targets = append(targets, s)
		}
	}
	h.mu.Unlock()

	for _, s := range targets {
		select {
		case s.ch <- event{topic: topic, data: data}:
		default:
			h.disconnect(s)
		}
	}
}

// Snapshot returns the most recently published payload for topic, if any.
func (h *Hub) Snapshot(topic string) ([]byte, bool) {
	h.mu.Lock()
	defer h.mu.Unlock()
	data, ok := h.snapshots[topic]
	return data, ok
}

// subscribe registers a new subscriber and, under the same lock, captures
// the current snapshots for its topics. Doing both atomically means no
// Publish can be missed between "join" and "read initial state".
func (h *Hub) subscribe(topics []string) (*subscriber, map[string][]byte) {
	s := newSubscriber(topics)
	h.mu.Lock()
	defer h.mu.Unlock()
	h.subscribers[s] = true
	initial := make(map[string][]byte, len(topics))
	for _, t := range topics {
		if d, ok := h.snapshots[t]; ok {
			initial[t] = d
		}
	}
	return s, initial
}

func (h *Hub) unsubscribe(s *subscriber) {
	h.mu.Lock()
	delete(h.subscribers, s)
	h.mu.Unlock()
}

func (h *Hub) disconnect(s *subscriber) {
	h.unsubscribe(s)
	s.kick()
}

// Handler serves SSE connections at whatever path it is mounted on, reading
// the desired topics from the "topics" query parameter (comma separated).
// isAdmin is consulted only when TopicOrphan is requested; it decides
// whether this particular request may subscribe to that topic. isAdmin may
// be nil, which is treated the same as a function that always returns false.
func (h *Hub) Handler(isAdmin func(*http.Request) bool) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		raw := r.URL.Query().Get("topics")
		if strings.TrimSpace(raw) == "" {
			http.Error(w, "topics parameter is required", http.StatusBadRequest)
			return
		}

		var topics []string
		seen := make(map[string]bool)
		adminChecked, adminOK := false, false
		for _, part := range strings.Split(raw, ",") {
			t := strings.TrimSpace(part)
			if t == "" || !knownTopics[t] || seen[t] {
				continue
			}
			if t == TopicOrphan {
				if !adminChecked {
					adminOK = isAdmin != nil && isAdmin(r)
					adminChecked = true
				}
				if !adminOK {
					continue
				}
			}
			seen[t] = true
			topics = append(topics, t)
		}

		flusher, ok := w.(http.Flusher)
		if !ok {
			http.Error(w, "streaming unsupported", http.StatusInternalServerError)
			return
		}

		header := w.Header()
		header.Set("Content-Type", "text/event-stream")
		header.Set("Cache-Control", "no-cache")
		// Cloudflare/nginx-style reverse proxies buffer proxied responses by
		// default, which would hold every SSE event until the buffer fills
		// or the connection closes; this opts the response out of that
		// buffering so events reach the client as soon as they are flushed.
		header.Set("X-Accel-Buffering", "no")
		header.Set("Content-Encoding", "gzip")
		w.WriteHeader(http.StatusOK)
		// Push the status line/headers to the client right away: a real
		// SSE client (EventSource) should see the connection open
		// immediately rather than waiting for the first event, which may
		// be arbitrarily far in the future.
		flusher.Flush()

		gz := gzip.NewWriter(w)
		defer gz.Close()

		sub, initial := h.subscribe(topics)
		defer h.unsubscribe(sub)

		write := func(topic string, data []byte) bool {
			if _, err := fmt.Fprintf(gz, "event: %s\ndata: %s\n\n", topic, data); err != nil {
				return false
			}
			if err := gz.Flush(); err != nil {
				return false
			}
			flusher.Flush()
			return true
		}

		// Send whatever snapshots already exist for the requested topics,
		// in the order the client asked for them. TopicTime is special: its
		// stored snapshot is up to one heartbeat interval stale, and clients
		// derive their clock offset from it, so replaying it would skew every
		// running timer by that staleness until the next heartbeat. Instead
		// each new subscriber gets a freshly stamped payload.
		for _, t := range topics {
			if t == TopicTime {
				if !write(t, timePayload()) {
					return
				}
				continue
			}
			if d, ok := initial[t]; ok {
				if !write(t, d) {
					return
				}
			}
		}

		ctx := r.Context()
		for {
			select {
			case <-ctx.Done():
				return
			case <-sub.kicked:
				return
			case ev := <-sub.ch:
				if !write(ev.topic, ev.data) {
					return
				}
			}
		}
	})
}

// timePayload returns a freshly stamped {"server_ms": <UnixMilli>} payload.
func timePayload() []byte {
	data, _ := json.Marshal(struct {
		ServerMS int64 `json:"server_ms"`
	}{ServerMS: time.Now().UnixMilli()})
	return data
}

// Run publishes a {"server_ms": <UnixMilli>} heartbeat to TopicTime every 25
// seconds until ctx is cancelled.
func (h *Hub) Run(ctx context.Context) {
	ticker := time.NewTicker(25 * time.Second)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			h.Publish(TopicTime, timePayload())
		}
	}
}
