// timemon 性能試験ツール (開発用、tools/sensor-sim.py と同じ立ち位置)。
//
// gtm.tm34.dev (実体は192.168.100.81:8080、Cloudflare Tunnel経由で公開) の
// 性能試験用。標準ライブラリのみで完結し、外部依存を持たない。
//
// 使い方:
//
//	go run ./tools/loadtest read   [-url http://192.168.100.81:8080] [-workers 300]
//	go run ./tools/loadtest write  -driver-class-id 1 -drivetrain-class-id 1 [-workers 50]
//	go run ./tools/loadtest admin  -admin-cookie <tm_sessionの値> [-driver-id N -vehicle-id N] [-workers 100]
//	go run ./tools/loadtest sse    [-workers 200] [-hold 30]
//
// read: 認証不要の読み取り系API (ranking/queue/recent/archive) にワーカー数を
// 段階的に増やしながらGETを打つ。
// write: 各ワーカーが一意な参加者としてPOST /api/register→POST/DELETE
// /api/mypage/queue をループする。事前に driver_class_id/drivetrain_class_id
// を管理画面で確認し指定すること。イベントの RegistrationOpen=true かつ
// RegistrationMode!="staff" であること。
// admin: 管理者のtm_sessionクッキー値が必要 (起動ログのEmergency admin URL
// または /a/{token} でログインして取得)。driver_id/vehicle_idは省略時
// GET /api/drivers, /api/vehiclesの先頭を自動採用する (write を先に実行して
// 合成データを作っておくとよい)。
// sse: GET /api/stream (orphan以外は無認証) への同時接続を段階的に増やし、
// 一定時間 (-hold秒) 維持して接続確立レイテンシを見る。
package main

import (
	"fmt"
	"os"
)

func main() {
	if len(os.Args) < 2 {
		usage()
		os.Exit(1)
	}
	cmd, args := os.Args[1], os.Args[2:]
	switch cmd {
	case "read":
		runRead(args)
	case "write":
		runWrite(args)
	case "admin":
		runAdmin(args)
	case "sse":
		runSSE(args)
	default:
		fmt.Fprintf(os.Stderr, "不明なサブコマンド: %s\n\n", cmd)
		usage()
		os.Exit(1)
	}
}

func usage() {
	fmt.Fprintln(os.Stderr, "使い方: go run ./tools/loadtest <read|write|admin|sse> [オプション]")
	fmt.Fprintln(os.Stderr, "各サブコマンドの詳細は go run ./tools/loadtest <サブコマンド> -h")
}
