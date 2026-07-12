package snapshot

import "timemon/internal/sse"

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
