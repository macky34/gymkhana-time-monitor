package snapshot

import (
	"encoding/json"
	"sync/atomic"

	"timemon/internal/sse"
)

// PublishRanking regenerates the ranking snapshot and pushes it to h.
func (b *Builder) PublishRanking(h *sse.Hub) error {
	data, err := b.Ranking()
	if err != nil {
		return err
	}
	h.Publish(sse.TopicRanking, data)
	return nil
}

// PublishQueue regenerates the queue snapshot and pushes it to h.
func (b *Builder) PublishQueue(h *sse.Hub) error {
	data, err := b.Queue()
	if err != nil {
		return err
	}
	h.Publish(sse.TopicQueue, data)
	return nil
}

// PublishOnCourse regenerates the on_course snapshot and pushes it to h.
func (b *Builder) PublishOnCourse(h *sse.Hub) error {
	data, err := b.OnCourse()
	if err != nil {
		return err
	}
	h.Publish(sse.TopicOnCourse, data)
	return nil
}

// PublishSettings regenerates the settings snapshot and pushes it to h.
func (b *Builder) PublishSettings(h *sse.Hub) error {
	data, err := b.Settings()
	if err != nil {
		return err
	}
	h.Publish(sse.TopicSettings, data)
	return nil
}

// PublishDirectory increments the "directory" revision counter and pushes
// the new value to h. Unlike the other Publish* methods, this snapshot does
// not describe any store content: {"rev": N} is a bare change counter that
// lets subscribers (the admin page) notice that a driver/vehicle/link
// mutation happened somewhere and refetch the actual lists via the existing
// REST APIs. The counter resets to 0 on process restart, which is fine —
// subscribers only care about the value changing, never its absolute value.
func (b *Builder) PublishDirectory(h *sse.Hub) error {
	rev := atomic.AddInt64(&b.dirRev, 1)
	data, err := json.Marshal(struct {
		Rev int64 `json:"rev"`
	}{Rev: rev})
	if err != nil {
		return err
	}
	h.Publish(sse.TopicDirectory, data)
	return nil
}

// PublishAll regenerates and publishes ranking, queue, on_course, and
// settings, in that order.
func (b *Builder) PublishAll(h *sse.Hub) error {
	if err := b.PublishRanking(h); err != nil {
		return err
	}
	if err := b.PublishQueue(h); err != nil {
		return err
	}
	if err := b.PublishOnCourse(h); err != nil {
		return err
	}
	if err := b.PublishSettings(h); err != nil {
		return err
	}
	return nil
}
