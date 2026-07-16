package main

import (
	"context"
	"flag"
	"fmt"
	"log"
	"net"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"timemon/internal/snapshot"
	"timemon/internal/sse"
	"timemon/internal/store"
	"timemon/internal/timing"
	"timemon/internal/web"
)

var version = "dev" // -ldflags "-X main.version=..."

// normalizeAddr turns a bare port number (e.g. "8080") into a listen address
// (":8080"). Any other value (already containing a host and/or colon) is
// returned unchanged.
func normalizeAddr(s string) string {
	if s == "" {
		return s
	}
	for _, r := range s {
		if r < '0' || r > '9' {
			return s
		}
	}
	return ":" + s
}

// normalizeDBPath appends the default ".sqlite3" extension when the given
// path has no extension at all (e.g. "event" -> "event.sqlite3").
func normalizeDBPath(s string) string {
	if filepath.Ext(s) == "" {
		return s + ".sqlite3"
	}
	return s
}

// lanIP returns a best-effort LAN IPv4 address for this host, for use as the
// default base-URL hostname. It never sends any actual network traffic: the
// UDP "connection" below only resolves routing to pick a local address.
func lanIP() string {
	if conn, err := net.Dial("udp", "8.8.8.8:80"); err == nil {
		defer conn.Close()
		if a, ok := conn.LocalAddr().(*net.UDPAddr); ok && a.IP != nil {
			return a.IP.String()
		}
	}
	if addrs, err := net.InterfaceAddrs(); err == nil {
		for _, a := range addrs {
			ipNet, ok := a.(*net.IPNet)
			if !ok || ipNet.IP.IsLoopback() {
				continue
			}
			if ip4 := ipNet.IP.To4(); ip4 != nil {
				return ip4.String()
			}
		}
	}
	return "localhost"
}

func main() {
	addr := flag.String("addr", ":8080", "HTTP待受アドレス (\"8080\" のようにポート番号のみでも可)")
	udpAddr := flag.String("udp", ":9999", "センサーUDP待受アドレス (ポート番号のみでも可)")
	dbPath := flag.String("db", "./event.sqlite3", "イベントDBファイル (拡張子 .sqlite3 は省略可。無ければ自動作成)")
	baseURL := flag.String("base-url", "", "外部から見えるベースURL (Setup URL/QR用。省略時はLAN IPから自動生成)")
	flag.Parse()

	*addr = normalizeAddr(*addr)
	*udpAddr = normalizeAddr(*udpAddr)
	*dbPath = normalizeDBPath(*dbPath)

	if *baseURL == "" {
		p := *addr
		host := ""
		if idx := strings.Index(p, ":"); idx > 0 {
			host = p[:idx]
		} else {
			host = lanIP()
		}
		if strings.HasPrefix(p, ":") {
			p = host + p
		}
		*baseURL = "http://" + p
	}

	st, err := store.Open(*dbPath)
	if err != nil {
		log.Fatalf("store: %v", err)
	}
	defer st.Close()

	hub := sse.NewHub()
	snap := snapshot.New(st)

	srv, err := web.NewServer(st, hub, snap, *baseURL)
	if err != nil {
		log.Fatalf("web: %v", err)
	}

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()
	go hub.Run(ctx)
	if err := snap.PublishAll(hub); err != nil {
		log.Printf("initial snapshot: %v", err)
	}

	// Sensor ingest (UDP): pairs start/goal triggers with cars on course and
	// feeds orphan/sensor_status back to the SSE hub via the web server.
	go func() {
		err := timing.Listen(ctx, *udpAddr, timing.Deps{
			Store:          st,
			Course:         srv.SensorController(),
			OnOrphan:       srv.OnOrphan,
			OnSensorStatus: srv.OnSensorStatus,
			ActiveEventID: func() (int64, bool) {
				ev, ok, err := st.GetActiveEvent()
				if err != nil || !ok {
					return 0, false
				}
				return ev.ID, true
			},
		})
		if err != nil {
			log.Printf("timing: %v", err)
		}
	}()

	// Hourly snapshot backup: VACUUM INTO ./snapshots/{unix}.sqlite3.
	go vacuumLoop(ctx, st, *dbPath)

	httpSrv := &http.Server{Addr: *addr, Handler: srv.Routes()}
	go func() {
		<-ctx.Done()
		httpSrv.Shutdown(context.Background())
	}()

	fmt.Printf("timemon %s listening on %s, udp %s (db=%s, base-url=%s)\n", version, *addr, *udpAddr, *dbPath, *baseURL)
	if err := httpSrv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
		log.Fatal(err)
	}
}

// vacuumLoop writes a consistent DB snapshot to ./snapshots/{unix}.sqlite3
// once an hour (Architecture wiki: バックアップ). Backups beyond that are an external concern
// (a Nextcloud client syncs the directory).
func vacuumLoop(ctx context.Context, st *store.Store, dbPath string) {
	dir := filepath.Join(filepath.Dir(dbPath), "snapshots")
	ticker := time.NewTicker(time.Hour)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			if err := os.MkdirAll(dir, 0o755); err != nil {
				log.Printf("vacuum: mkdir %s: %v", dir, err)
				continue
			}
			out := filepath.Join(dir, fmt.Sprintf("%d.sqlite3", time.Now().Unix()))
			if err := st.VacuumInto(out); err != nil {
				log.Printf("vacuum: %v", err)
			}
		}
	}
}
