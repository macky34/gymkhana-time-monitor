package sse

import (
	"bufio"
	"compress/gzip"
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
	"time"
)

func newTestClient() *http.Client {
	// Disable the Transport's own transparent gzip handling: by default Go
	// auto-decompresses "Content-Encoding: gzip" responses and strips the
	// header, which would hide the exact thing these tests verify.
	return &http.Client{
		Timeout:   5 * time.Second,
		Transport: &http.Transport{DisableCompression: true},
	}
}

// sseReader wraps a gzip-compressed SSE HTTP response body and lets tests
// pull one (topic, data) event at a time as they arrive on the wire. The
// gzip reader is created lazily on the first call to next(): gzip.NewReader
// blocks until at least the gzip header bytes are available, which the
// server only writes once it has its own first event to send, so
// initializing eagerly would deadlock any test that constructs the reader
// before triggering a Publish.
type sseReader struct {
	body io.Reader
	br   *bufio.Reader
}

func newSSEReader(_ *testing.T, body io.Reader) *sseReader {
	return &sseReader{body: body}
}

func (r *sseReader) next() (topic string, data []byte, err error) {
	if r.br == nil {
		gz, err := gzip.NewReader(r.body)
		if err != nil {
			return "", nil, err
		}
		r.br = bufio.NewReader(gz)
	}
	var gotEvent bool
	for {
		line, err := r.br.ReadString('\n')
		if err != nil {
			return "", nil, err
		}
		line = strings.TrimRight(line, "\r\n")
		switch {
		case strings.HasPrefix(line, "event: "):
			topic = strings.TrimPrefix(line, "event: ")
			gotEvent = true
		case strings.HasPrefix(line, "data: "):
			data = []byte(strings.TrimPrefix(line, "data: "))
		case line == "" && gotEvent:
			return topic, data, nil
		}
	}
}

func TestHandler_InitialSnapshotAndLiveUpdates(t *testing.T) {
	h := NewHub()
	h.Publish(TopicRanking, []byte(`{"rows":[]}`))

	srv := httptest.NewServer(h.Handler(func(*http.Request) bool { return false }))
	t.Cleanup(srv.Close)

	resp, err := newTestClient().Get(srv.URL + "/?topics=ranking,queue")
	if err != nil {
		t.Fatalf("GET: %v", err)
	}
	t.Cleanup(func() { resp.Body.Close() })

	if ct := resp.Header.Get("Content-Type"); ct != "text/event-stream" {
		t.Fatalf("Content-Type = %q", ct)
	}
	if ce := resp.Header.Get("Content-Encoding"); ce != "gzip" {
		t.Fatalf("Content-Encoding = %q", ce)
	}

	sr := newSSEReader(t, resp.Body)

	topic, data, err := sr.next()
	if err != nil {
		t.Fatalf("reading initial event: %v", err)
	}
	if topic != TopicRanking || string(data) != `{"rows":[]}` {
		t.Fatalf("initial event = %s %s, want ranking {\"rows\":[]}", topic, data)
	}

	h.Publish(TopicQueue, []byte(`{"items":[]}`))

	topic, data, err = sr.next()
	if err != nil {
		t.Fatalf("reading live event: %v", err)
	}
	if topic != TopicQueue || string(data) != `{"items":[]}` {
		t.Fatalf("live event = %s %s, want queue {\"items\":[]}", topic, data)
	}
}

func TestHandler_ResponseIsGzippedSSE(t *testing.T) {
	h := NewHub()
	h.Publish(TopicSettings, []byte(`{"a":1}`))

	srv := httptest.NewServer(h.Handler(nil))
	t.Cleanup(srv.Close)

	resp, err := newTestClient().Get(srv.URL + "/?topics=settings")
	if err != nil {
		t.Fatalf("GET: %v", err)
	}
	t.Cleanup(func() { resp.Body.Close() })

	gz, err := gzip.NewReader(resp.Body)
	if err != nil {
		t.Fatalf("response body is not valid gzip: %v", err)
	}
	br := bufio.NewReader(gz)

	line, err := br.ReadString('\n')
	if err != nil {
		t.Fatalf("reading decompressed event line: %v", err)
	}
	if got := strings.TrimRight(line, "\r\n"); got != "event: settings" {
		t.Fatalf("first decompressed line = %q, want %q", got, "event: settings")
	}

	line, err = br.ReadString('\n')
	if err != nil {
		t.Fatalf("reading decompressed data line: %v", err)
	}
	if got := strings.TrimRight(line, "\r\n"); got != `data: {"a":1}` {
		t.Fatalf("second decompressed line = %q, want %q", got, `data: {"a":1}`)
	}
}

func TestHandler_OrphanExcludedForNonAdmin(t *testing.T) {
	h := NewHub()
	srv := httptest.NewServer(h.Handler(func(*http.Request) bool { return false }))
	t.Cleanup(srv.Close)

	resp, err := newTestClient().Get(srv.URL + "/?topics=orphan,settings")
	if err != nil {
		t.Fatalf("GET: %v", err)
	}
	t.Cleanup(func() { resp.Body.Close() })
	sr := newSSEReader(t, resp.Body)

	h.Publish(TopicOrphan, []byte(`{"secret":true}`))
	h.Publish(TopicSettings, []byte(`{"ok":true}`))

	topic, data, err := sr.next()
	if err != nil {
		t.Fatalf("reading event: %v", err)
	}
	if topic != TopicSettings || string(data) != `{"ok":true}` {
		t.Fatalf("expected settings event to arrive (orphan must be skipped), got %s %s", topic, data)
	}
}

func TestHandler_OrphanIncludedForAdmin(t *testing.T) {
	h := NewHub()
	srv := httptest.NewServer(h.Handler(func(*http.Request) bool { return true }))
	t.Cleanup(srv.Close)

	resp, err := newTestClient().Get(srv.URL + "/?topics=orphan")
	if err != nil {
		t.Fatalf("GET: %v", err)
	}
	t.Cleanup(func() { resp.Body.Close() })
	sr := newSSEReader(t, resp.Body)

	h.Publish(TopicOrphan, []byte(`{"secret":true}`))

	topic, data, err := sr.next()
	if err != nil {
		t.Fatalf("reading event: %v", err)
	}
	if topic != TopicOrphan || string(data) != `{"secret":true}` {
		t.Fatalf("expected orphan event, got %s %s", topic, data)
	}
}

func TestHandler_MissingTopicsIs400(t *testing.T) {
	h := NewHub()
	srv := httptest.NewServer(h.Handler(nil))
	t.Cleanup(srv.Close)

	resp, err := newTestClient().Get(srv.URL + "/")
	if err != nil {
		t.Fatalf("GET: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400", resp.StatusCode)
	}
}

func TestHandler_UnknownTopicIgnoredSilently(t *testing.T) {
	h := NewHub()
	h.Publish(TopicSettings, []byte(`{"ok":true}`))
	srv := httptest.NewServer(h.Handler(nil))
	t.Cleanup(srv.Close)

	resp, err := newTestClient().Get(srv.URL + "/?topics=bogus,settings")
	if err != nil {
		t.Fatalf("GET: %v", err)
	}
	t.Cleanup(func() { resp.Body.Close() })
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200", resp.StatusCode)
	}
	sr := newSSEReader(t, resp.Body)
	topic, data, err := sr.next()
	if err != nil {
		t.Fatalf("reading event: %v", err)
	}
	if topic != TopicSettings || string(data) != `{"ok":true}` {
		t.Fatalf("expected settings event, got %s %s", topic, data)
	}
}

func TestHandler_SlowSubscriberDoesNotBlockPublish(t *testing.T) {
	h := NewHub()
	srv := httptest.NewServer(h.Handler(nil))
	t.Cleanup(srv.Close)

	// Connect but never read the body: once more than subscriberBufferSize
	// events are published, this subscriber's channel overflows.
	resp, err := newTestClient().Get(srv.URL + "/?topics=ranking")
	if err != nil {
		t.Fatalf("GET: %v", err)
	}
	t.Cleanup(func() { resp.Body.Close() })

	// A normal, actively-reading subscriber on the same topic must keep
	// receiving events even after the slow one overflows.
	resp2, err := newTestClient().Get(srv.URL + "/?topics=ranking")
	if err != nil {
		t.Fatalf("GET: %v", err)
	}
	t.Cleanup(func() { resp2.Body.Close() })
	sr2 := newSSEReader(t, resp2.Body)
	drainDone := make(chan struct{})
	go func() {
		defer close(drainDone)
		for i := 0; i < 64; i++ {
			if _, _, err := sr2.next(); err != nil {
				return
			}
		}
	}()

	publishDone := make(chan struct{})
	go func() {
		defer close(publishDone)
		for i := 0; i < 64; i++ {
			h.Publish(TopicRanking, []byte(`{"n":`+strconv.Itoa(i)+`}`))
		}
	}()

	select {
	case <-publishDone:
	case <-time.After(3 * time.Second):
		t.Fatal("Publish blocked because of a slow subscriber")
	}
	select {
	case <-drainDone:
	case <-time.After(3 * time.Second):
		t.Fatal("active subscriber did not receive events while another was slow")
	}
}

func TestRun_StopsOnContextCancel(t *testing.T) {
	h := NewHub()
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)

	done := make(chan struct{})
	go func() {
		defer close(done)
		h.Run(ctx)
	}()

	cancel()

	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("Run did not return after context cancellation")
	}
}
