package main

import (
	"context"
	"flag"
	"fmt"
	"log"
	"net/http"
	"os/signal"
	"strings"
	"syscall"

	"timemon/internal/snapshot"
	"timemon/internal/sse"
	"timemon/internal/store"
	"timemon/internal/web"
)

var version = "dev" // -ldflags "-X main.version=..."

func main() {
	addr := flag.String("addr", ":8080", "HTTP待受アドレス")
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

	// TODO(W3): timing.Listen (UDP :9999) の起動
	// TODO(W4): 1時間毎の VACUUM INTO ./snapshots/{ts}.sqlite3 goroutine

	httpSrv := &http.Server{Addr: *addr, Handler: srv.Routes()}
	go func() {
		<-ctx.Done()
		httpSrv.Shutdown(context.Background())
	}()

	fmt.Printf("timemon %s listening on %s (db=%s)\n", version, *addr, *dbPath)
	if err := httpSrv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
		log.Fatal(err)
	}
}
