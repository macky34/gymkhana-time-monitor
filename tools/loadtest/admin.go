package main

import (
	"bytes"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"net/http"
	"os"
	"time"
)

func runAdmin(args []string) {
	fs := flag.NewFlagSet("admin", flag.ExitOnError)
	baseURL := fs.String("url", "http://192.168.100.81:8080", "接続先ベースURL")
	maxWorkers := fs.Int("workers", 100, "最大同時ワーカー数")
	adminCookie := fs.String("admin-cookie", "", "管理者のtm_sessionクッキー値 (必須)")
	driverID := fs.Int64("driver-id", 0, "省略時はGET /api/driversの先頭を使う")
	vehicleID := fs.Int64("vehicle-id", 0, "省略時はGET /api/vehiclesの先頭を使う")
	fs.Parse(args)

	if *adminCookie == "" {
		fmt.Fprintln(os.Stderr, "-admin-cookie を指定してください (管理者ログインURLで発行されたtm_sessionの値)")
		os.Exit(1)
	}

	client := &http.Client{Timeout: 10 * time.Second}
	cookieHeader := "tm_session=" + *adminCookie

	dID, vID := *driverID, *vehicleID
	if dID == 0 {
		dID = fetchFirstID(client, *baseURL+"/api/drivers", "drivers")
	}
	if vID == 0 {
		vID = fetchFirstID(client, *baseURL+"/api/vehicles", "vehicles")
	}
	if dID == 0 || vID == 0 {
		fmt.Fprintln(os.Stderr, "driver_id/vehicle_idが取得できませんでした。write サブコマンドを先に実行して合成データを作るか、-driver-id/-vehicle-id を指定してください")
		os.Exit(1)
	}
	fmt.Printf("使用する driver_id=%d vehicle_id=%d\n", dID, vID)

	post := func(path string, body []byte) (int, error) {
		req, err := http.NewRequest(http.MethodPost, *baseURL+path, bytes.NewReader(body))
		if err != nil {
			return 0, err
		}
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("Cookie", cookieHeader)
		resp, err := client.Do(req)
		if err != nil {
			return 0, err
		}
		defer resp.Body.Close()
		io.Copy(io.Discard, resp.Body)
		return resp.StatusCode, nil
	}

	// admin系はレート制限が無いため read/write より高いワーカー数で攻めてよい。
	// 複数ワーカーが同じ待機列/コース状態を奪い合うため409(Conflict)は
	// 想定内のレースとして成功扱いにする。
	okStatus := func(status int, err error) bool {
		return err == nil && (status == http.StatusOK || status == http.StatusConflict)
	}

	factory := func(id int) func() (time.Duration, bool) {
		return func() (time.Duration, bool) {
			start := time.Now()
			ok := true

			enqueueBody, _ := json.Marshal(map[string]any{"driver_id": dID, "vehicle_id": vID})
			if status, err := post("/api/admin/queue", enqueueBody); !okStatus(status, err) {
				ok = false
			}
			if status, err := post("/api/admin/course", nil); !okStatus(status, err) {
				ok = false
			}
			time.Sleep(100 * time.Millisecond)
			if status, err := post("/api/admin/course/start", nil); !okStatus(status, err) {
				ok = false
			}
			time.Sleep(200 * time.Millisecond)
			if status, err := post("/api/admin/course/finish", nil); !okStatus(status, err) {
				ok = false
			}

			return time.Since(start), ok
		}
	}

	stages := []stage{
		{target: min(10, *maxWorkers), duration: 30 * time.Second},
		{target: min(50, *maxWorkers), duration: time.Minute},
		{target: *maxWorkers, duration: time.Minute},
	}
	samples := runRamp(stages, factory)
	summarize("admin-api", samples)
}

func fetchFirstID(client *http.Client, url, key string) int64 {
	resp, err := client.Get(url)
	if err != nil {
		return 0
	}
	defer resp.Body.Close()
	var out map[string][]struct {
		ID int64 `json:"id"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		return 0
	}
	items := out[key]
	if len(items) == 0 {
		return 0
	}
	return items[0].ID
}
