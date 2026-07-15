package main

import "testing"

func TestNormalizeAddr(t *testing.T) {
	cases := []struct {
		in   string
		want string
	}{
		{"8080", ":8080"},
		{"9999", ":9999"},
		{"0", ":0"},
		{":8080", ":8080"},
		{"localhost:8080", "localhost:8080"},
		{"192.168.1.10:8080", "192.168.1.10:8080"},
		{"", ""},
	}
	for _, c := range cases {
		if got := normalizeAddr(c.in); got != c.want {
			t.Errorf("normalizeAddr(%q) = %q, want %q", c.in, got, c.want)
		}
	}
}

func TestNormalizeDBPath(t *testing.T) {
	cases := []struct {
		in   string
		want string
	}{
		{"event", "event.sqlite3"},
		{"./event", "./event.sqlite3"},
		{"event.sqlite3", "event.sqlite3"},
		{"./event.sqlite3", "./event.sqlite3"},
		{"/data/event.db", "/data/event.db"},
		{"event.db", "event.db"},
	}
	for _, c := range cases {
		if got := normalizeDBPath(c.in); got != c.want {
			t.Errorf("normalizeDBPath(%q) = %q, want %q", c.in, got, c.want)
		}
	}
}
