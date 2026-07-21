package main

import (
	"bufio"
	"context"
	"flag"
	"fmt"
	"net/http"
	"strings"
	"time"
)

func runSSE(args []string) {
	fs := flag.NewFlagSet("sse", flag.ExitOnError)
	baseURL := fs.String("url", "http://192.168.100.81:8080", "接続先ベースURL")
	maxWorkers := fs.Int("workers", 200, "最大同時接続数")
	holdSeconds := fs.Int("hold", 30, "1接続を維持する秒数")
	topics := fs.String("topics", "ranking,on_course,queue,time", "購読するSSEトピック (カンマ区切り)")
	fs.Parse(args)

	url := fmt.Sprintf("%s/api/stream?topics=%s", strings.TrimRight(*baseURL, "/"), *topics)
	hold := time.Duration(*holdSeconds) * time.Second

	factory := func(id int) func() (time.Duration, bool) {
		return func() (time.Duration, bool) {
			return openSSEConnection(url, hold)
		}
	}

	stages := []stage{
		{target: min(50, *maxWorkers), duration: 30 * time.Second},
		{target: *maxWorkers, duration: time.Minute},
		{target: *maxWorkers, duration: hold},
	}
	samples := runRamp(stages, factory)
	summarize("sse-stream (接続確立レイテンシ)", samples)
}

// openSSEConnection は1本のSSE接続を張り、hold時間だけ維持してから閉じる。
// 接続確立レイテンシ(初回レスポンス受信までの時間)と成功可否を返す。
//
// timemonのSSEは自発的に切断しないため、接続維持時間の唯一の締め切りは
// このcontextのタイムアウトである。bufio.Scanner.Scan()はイベント到着まで
// ブロックするため、ループ側でtime.Now()を見ても締め切りを守れない
// (イベント間隔が空くとオーバーシュートする)。contextの締め切りが来ると
// 内部の読み取りが中断されscanner.Scan()がfalseを返すため、これで確実に
// hold通りに終了する。
func openSSEConnection(url string, hold time.Duration) (time.Duration, bool) {
	ctx, cancel := context.WithTimeout(context.Background(), hold)
	defer cancel()

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return 0, false
	}
	req.Header.Set("Accept", "text/event-stream")

	start := time.Now()
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return time.Since(start), false
	}
	defer resp.Body.Close()
	connectLatency := time.Since(start)
	if resp.StatusCode != http.StatusOK {
		return connectLatency, false
	}

	scanner := bufio.NewScanner(resp.Body)
	scanner.Buffer(make([]byte, 64*1024), 1024*1024)
	for scanner.Scan() {
		// 受信データは読み捨てて接続を維持するだけでよい。イベント件数は
		// このツールでは計測せず、接続確立レイテンシと成功率に絞る。
	}
	return connectLatency, true
}
