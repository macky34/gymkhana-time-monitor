package timing

import (
	"context"
	"encoding/json"
	"net"
	"testing"
	"time"
)

// TestHandleHeartbeatSendsConfigReply verifies that a hb packet gets a
// "config" reply piggybacked back on the same UDP socket the sensor sent it
// from, carrying the current lockout value (issue #18: live lockout reload
// without a device reboot).
func TestHandleHeartbeatSendsConfigReply(t *testing.T) {
	probe, err := net.ListenPacket("udp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("reserve udp port: %v", err)
	}
	addr := probe.LocalAddr().String()
	probe.Close()

	ctx, cancel := context.WithCancel(context.Background())
	stopped := make(chan struct{})
	go func() {
		if err := Listen(ctx, addr, Deps{
			SensorLockoutMS: func() int { return 1234 },
		}); err != nil {
			t.Logf("Listen: %v", err)
		}
		close(stopped)
	}()
	t.Cleanup(func() {
		cancel()
		select {
		case <-stopped:
		case <-time.After(2 * time.Second):
			t.Error("Listen did not return after context cancel")
		}
	})

	var conn net.Conn
	deadline := time.Now().Add(time.Second)
	for time.Now().Before(deadline) {
		conn, err = net.Dial("udp", addr)
		if err == nil {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	if conn == nil {
		t.Fatalf("dial udp %s: %v", addr, err)
	}
	t.Cleanup(func() { conn.Close() })

	if _, err := conn.Write([]byte(hbJSON("start", 42, 1, 0.0))); err != nil {
		t.Fatalf("udp write: %v", err)
	}

	if err := conn.SetReadDeadline(time.Now().Add(2 * time.Second)); err != nil {
		t.Fatalf("SetReadDeadline: %v", err)
	}
	buf := make([]byte, 256)
	n, err := conn.Read(buf)
	if err != nil {
		t.Fatalf("waiting for config reply: %v", err)
	}

	var reply struct {
		Type      string `json:"type"`
		LockoutMS int    `json:"lockout_ms"`
	}
	if err := json.Unmarshal(buf[:n], &reply); err != nil {
		t.Fatalf("unmarshal config reply %q: %v", buf[:n], err)
	}
	if reply.Type != "config" {
		t.Errorf("reply.Type = %q, want %q", reply.Type, "config")
	}
	if reply.LockoutMS != 1234 {
		t.Errorf("reply.LockoutMS = %d, want %d", reply.LockoutMS, 1234)
	}
}

// TestHandleHeartbeatNoConfigReplyWhenNilSensorLockoutMS verifies that no
// reply is sent when Deps.SensorLockoutMS is left nil (the default),
// preserving today's one-directional-only behavior for callers that don't
// wire it up.
func TestHandleHeartbeatNoConfigReplyWhenNilSensorLockoutMS(t *testing.T) {
	probe, err := net.ListenPacket("udp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("reserve udp port: %v", err)
	}
	addr := probe.LocalAddr().String()
	probe.Close()

	ctx, cancel := context.WithCancel(context.Background())
	stopped := make(chan struct{})
	go func() {
		if err := Listen(ctx, addr, Deps{}); err != nil {
			t.Logf("Listen: %v", err)
		}
		close(stopped)
	}()
	t.Cleanup(func() {
		cancel()
		select {
		case <-stopped:
		case <-time.After(2 * time.Second):
			t.Error("Listen did not return after context cancel")
		}
	})

	var conn net.Conn
	deadline := time.Now().Add(time.Second)
	for time.Now().Before(deadline) {
		conn, err = net.Dial("udp", addr)
		if err == nil {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	if conn == nil {
		t.Fatalf("dial udp %s: %v", addr, err)
	}
	t.Cleanup(func() { conn.Close() })

	if _, err := conn.Write([]byte(hbJSON("start", 42, 1, 0.0))); err != nil {
		t.Fatalf("udp write: %v", err)
	}

	if err := conn.SetReadDeadline(time.Now().Add(300 * time.Millisecond)); err != nil {
		t.Fatalf("SetReadDeadline: %v", err)
	}
	buf := make([]byte, 256)
	if _, err := conn.Read(buf); err == nil {
		t.Fatalf("expected no reply when SensorLockoutMS is nil, but got one")
	}
}
