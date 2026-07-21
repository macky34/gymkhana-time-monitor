package main

import (
	"fmt"
	"net/http"
)

// fakeIPTransport はリクエストごとにCF-Connecting-IPヘッダを付与する。
// レート制限(internal/web/ratelimit.go)はこのヘッダを優先してキーにするため、
// 192.168.100.81:8080への直接試験ではワーカーごとに異なる疑似IPを送ることで
// 正当な負荷試験として複数送信元を模擬する。Cloudflare Tunnel経由では
// Cloudflare自身が上書きするため通用しない。
type fakeIPTransport struct {
	ip string
}

func (t *fakeIPTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	req.Header.Set("CF-Connecting-IP", t.ip)
	return http.DefaultTransport.RoundTrip(req)
}

func fakeIPFor(id int) string {
	return fmt.Sprintf("10.%d.%d.1", (id>>8)&255, id&255)
}
