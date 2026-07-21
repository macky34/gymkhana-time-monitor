package main

import (
	"flag"
	"io"
	"math/rand"
	"net/http"
	"time"
)

var readEndpoints = []string{
	"/api/ranking",
	"/api/queue",
	"/api/recent",
	"/api/archive/events",
}

func runRead(args []string) {
	fs := flag.NewFlagSet("read", flag.ExitOnError)
	baseURL := fs.String("url", "http://192.168.100.81:8080", "接続先ベースURL")
	maxWorkers := fs.Int("workers", 300, "最大同時ワーカー数")
	fs.Parse(args)

	client := &http.Client{Timeout: 10 * time.Second}

	factory := func(id int) func() (time.Duration, bool) {
		return func() (time.Duration, bool) {
			path := readEndpoints[rand.Intn(len(readEndpoints))]
			start := time.Now()
			resp, err := client.Get(*baseURL + path)
			if err != nil {
				return time.Since(start), false
			}
			defer resp.Body.Close()
			io.Copy(io.Discard, resp.Body)
			return time.Since(start), resp.StatusCode == http.StatusOK
		}
	}

	stages := []stage{
		{target: min(10, *maxWorkers), duration: 30 * time.Second},
		{target: min(50, *maxWorkers), duration: time.Minute},
		{target: min(100, *maxWorkers), duration: time.Minute},
		{target: *maxWorkers, duration: time.Minute},
	}
	samples := runRamp(stages, factory)
	summarize("read-api", samples)
}
