package main

import (
	"context"
	"flag"
	"fmt"
	"log"
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

func main() {
	addr := flag.String("addr", ":8080", "HTTP待受アドレス")
	udpAddr := flag.String("udp", ":9999", "センサーUDP待受アドレス")
	dbPath := flag.String("db", "./event.sqlite3", "イベントDBファイル (無ければ自動作成)")
	baseURL := flag.String("base-url", "", "外部から見えるベースURL (Setup URL/QR用。省略時 http://localhost:<port>)")
	flag.Parse()

	if *baseURL == "" {
		p := *addr
		if strings.HasPrefix(p, ":") {
			p = "localhost" + p
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

	fmt.Printf("timemon %s listening on %s, udp %s (db=%s)\n", version, *addr, *udpAddr, *dbPath)
	if err := httpSrv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
		log.Fatal(err)
	}
}

// vacuumLoop writes a consistent DB snapshot to ./snapshots/{unix}.sqlite3
// once an hour (DESIGN.md §2). Backups beyond that are an external concern
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
