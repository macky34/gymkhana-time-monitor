---
name: new-admin-api
description: 管理API (POST/PUT/DELETE /api/admin/...) を新設・変更するときのチェックリスト。ミドルウェアの合成順序 (withCSRFGuard→withAdmin)、audit記録、store層の排他、テスト、Wiki同期までの定型手順を強制する。管理ルートの追加・変更時は必ず参照すること。
---

# 管理APIの追加・変更手順

管理ルートは定型パターンの積み重ねで、順序ミスがそのままセキュリティホールになる。以下を上から順に満たすこと。

## 1. store層

- 書き込み系メソッドは必ず `s.writeMu.Lock()/defer Unlock()` を取る(`internal/store/` の既存メソッドに合わせる)。
- エラーは `fmt.Errorf("store: <動作>: %w", err)` 形式でラップ。
- check-then-act(存在確認→更新 等)が必要なら、可能な限り1つのSQL文またはトランザクションにまとめる。ハンドラ側での分離チェックはレースになる(例: CountAdmins→SetRole)。

## 2. ハンドラ (internal/web/admin_<領域>.go)

- シグネチャは `func (s *Server) handleAdminXxx(w http.ResponseWriter, r *http.Request, admin store.Driver)`。
- body解析は `decodeReqJSON[T]`、パスIDは `requirePathID`、エラー応答は `writeErr` / `writeJSONError`、成功は `writeJSON`(いずれも `httputil.go`)。
- 変更成功後は `s.audit(&admin.ID, "admin.<領域>.<動作>", map[string]any{...})` を必ず呼ぶ。
- 公開スナップショットに影響する変更なら `publishDirectory()` / `s.Snap.Publish...` を忘れない。

## 3. ルート登録 (server.go の Routes())

- **合成順序は外側から `withCSRFGuard(withAdmin(handler))`**。読み取り(GET)はCSRF不要で `withAdmin(handler)` のみ。
- `withAdmin` は緊急管理者(合成Driver, ID=0)を403で拒否する(default-deny)。**緊急管理者にも許可してよいのはユーザー管理系のみ**で、その場合に限り `withUserAdmin` を使う。迷ったら `withAdmin`。
- 新ルートにレート制限が必要か検討(公開・認証系のみ対象。管理APIは通常不要)。

## 4. テスト

- `internal/web/` の既存テスト(`newTestServer` ヘルパー)に合わせて追加。最低限: 正常系、非adminでの403、(該当すれば)緊急管理者での挙動。

## 5. 検証と同期

```sh
go build ./... && go vet ./... && go test ./... && gofmt -l .
```

- `/wiki-sync` を実行し、`API.md` のルート一覧(認証列: 公開 / U / A / A* / RL / CSRF)と関連ページを更新する。
