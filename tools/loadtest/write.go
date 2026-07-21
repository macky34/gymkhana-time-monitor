package main

import (
	"bytes"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"net/http"
	"net/http/cookiejar"
	"os"
	"time"
)

func runWrite(args []string) {
	fs := flag.NewFlagSet("write", flag.ExitOnError)
	baseURL := fs.String("url", "http://192.168.100.81:8080", "接続先ベースURL")
	maxWorkers := fs.Int("workers", 50, "最大同時ワーカー数 (書き込みは控えめに)")
	driverClassID := fs.Int64("driver-class-id", 0, "登録に使うdriver_class_id (必須)")
	drivetrainClassID := fs.Int64("drivetrain-class-id", 0, "登録に使うdrivetrain_class_id (必須)")
	fs.Parse(args)

	if *driverClassID == 0 || *drivetrainClassID == 0 {
		fmt.Fprintln(os.Stderr, "-driver-class-id と -drivetrain-class-id を指定してください (管理画面で確認)")
		os.Exit(1)
	}

	factory := func(id int) func() (time.Duration, bool) {
		jar, _ := cookiejar.New(nil)
		client := &http.Client{
			Timeout:   10 * time.Second,
			Jar:       jar,
			Transport: &fakeIPTransport{ip: fakeIPFor(id)},
		}
		registered := false
		iter := 0

		return func() (time.Duration, bool) {
			iter++
			start := time.Now()

			if !registered {
				body, _ := json.Marshal(map[string]any{
					"name":            fmt.Sprintf("loadtest-%d-%d", id, iter),
					"driver_class_id": *driverClassID,
					"vehicle": map[string]any{
						"number":              (id*1000 + iter) % 9999,
						"name":                fmt.Sprintf("loadtest-car-%d-%d", id, iter),
						"engine_type":         "NA",
						"drivetrain_class_id": *drivetrainClassID,
					},
				})
				resp, err := client.Post(*baseURL+"/api/register", "application/json", bytes.NewReader(body))
				if err != nil {
					return time.Since(start), false
				}
				io.Copy(io.Discard, resp.Body)
				resp.Body.Close()
				if resp.StatusCode != http.StatusOK {
					return time.Since(start), false
				}
				registered = true
			}

			qResp, err := client.Post(*baseURL+"/api/mypage/queue", "application/json", bytes.NewReader([]byte("{}")))
			if err != nil {
				return time.Since(start), false
			}
			io.Copy(io.Discard, qResp.Body)
			qResp.Body.Close()
			okQueue := qResp.StatusCode == http.StatusOK || qResp.StatusCode == http.StatusConflict

			req, _ := http.NewRequest(http.MethodDelete, *baseURL+"/api/mypage/queue", nil)
			cResp, err := client.Do(req)
			if err != nil {
				return time.Since(start), false
			}
			io.Copy(io.Discard, cResp.Body)
			cResp.Body.Close()
			okCancel := cResp.StatusCode == http.StatusOK || cResp.StatusCode == http.StatusNotFound
			elapsed := time.Since(start)

			// mypage系はIPベースのレート制限(10 req/10秒=持続1req/秒、
			// internal/web/ratelimit.go)がかかっており、1イテレーションで
			// 2リクエスト(add+cancel)消費する。runRamp側の150-500msの
			// 待ちだけでは持続可能レートを超えて429を量産しSQLite書き込み
			// 経路の実力が見えなくなるため、ここで追加の待ちを入れて
			// 1ワーカー=1疑似IPあたりの消費を1req/秒以下に抑える。計測対象は
			// 実際のリクエスト所要時間(elapsed)のみとし、このペース調整の
			// 待ち時間はレイテンシに含めない。
			time.Sleep(2500 * time.Millisecond)

			return elapsed, okQueue && okCancel
		}
	}

	stages := []stage{
		{target: min(5, *maxWorkers), duration: 30 * time.Second},
		{target: min(20, *maxWorkers), duration: time.Minute},
		{target: *maxWorkers, duration: time.Minute},
	}
	samples := runRamp(stages, factory)
	summarize("write-api", samples)
}
