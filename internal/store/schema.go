package store

// schemaSQL is the event database schema, embedded verbatim from
// plan/DESIGN.md §2 ("データベース"). CREATE TABLE has been turned into
// CREATE TABLE IF NOT EXISTS so Open can apply it unconditionally on every
// startup; column definitions, defaults and comments are otherwise
// unchanged from the design document.
const schemaSQL = `
-- 単一行。イベント作成 = この行のINSERT (defaults.jsonの値をシード)
CREATE TABLE IF NOT EXISTS settings (
  id INTEGER PRIMARY KEY CHECK (id = 1),
  event_name TEXT NOT NULL,
  timing_mode TEXT NOT NULL DEFAULT 'sensor',      -- 'sensor' | 'manual' (イベント途中の切替可)
  pt_mode TEXT NOT NULL DEFAULT 'add',             -- 'add' | 'invalidate'
  pt_penalty_ms INTEGER NOT NULL DEFAULT 5000,
  heat_ranking INTEGER NOT NULL DEFAULT 0,         -- bool。※ヒート「数の上限」機能は意図的に非搭載 (自由走行主眼。運用で制御)
  registration_mode TEXT NOT NULL DEFAULT 'public',-- 'public' | 'staff'
  registration_open INTEGER NOT NULL DEFAULT 1,    -- bool (受付終了で0)
  queue_self_entry INTEGER NOT NULL DEFAULT 1,     -- bool
  max_course_time_sec INTEGER NOT NULL DEFAULT 180,
  sensor_lockout_ms INTEGER NOT NULL DEFAULT 800,
  coefficients TEXT NOT NULL,                      -- JSON: {"turbo_gasoline":1.7,"turbo_diesel":1.5,"rotary":1.7,"supercharger":1.7}
  displacement_classes TEXT NOT NULL               -- JSON: [{"label":"~660cc","max_cc":660},{"label":"~1600cc","max_cc":1600},{"label":"無制限","max_cc":null}]
);

-- クラスラベル (driver軸・drivetrain軸のみ。排気量クラスはsettingsから導出)
CREATE TABLE IF NOT EXISTS class_defs (
  id INTEGER PRIMARY KEY AUTOINCREMENT,
  axis TEXT NOT NULL,                              -- 'driver' | 'drivetrain'
  label TEXT NOT NULL,
  sort_order INTEGER NOT NULL
);

-- ドライバー = ユーザー (認証主体)
CREATE TABLE IF NOT EXISTS drivers (
  id INTEGER PRIMARY KEY AUTOINCREMENT,
  name TEXT NOT NULL,
  driver_class_id INTEGER NOT NULL REFERENCES class_defs(id),
  icon BLOB,                                       -- 128x128 JPEG。NULL可
  token TEXT NOT NULL UNIQUE,                      -- ログイントークン平文 (base64url)。システム発行の使い捨てランダム値であり
                                                   -- ユーザー由来の秘密ではないため平文保存 (セッションID保存と同等の扱い)
  role TEXT NOT NULL DEFAULT 'user',               -- 'user' | 'admin'
  main_vehicle_id INTEGER REFERENCES vehicles(id),
  is_deleted INTEGER NOT NULL DEFAULT 0
);

CREATE TABLE IF NOT EXISTS vehicles (
  id INTEGER PRIMARY KEY AUTOINCREMENT,
  number INTEGER NOT NULL,                         -- 号車番号 (表示「＃3 アルトワークス」)。重複はDB制約で禁止しない — 登録・編集UIで警告表示のみ (運用ミスだが登録を詰まらせない)
  name TEXT NOT NULL,
  engine_type TEXT NOT NULL,                       -- 'gasoline' | 'diesel' | 'rotary' | 'ev'
  displacement_cc INTEGER,                         -- EVはNULL
  forced_induction INTEGER NOT NULL DEFAULT 0,     -- bool (EVは常に0)
  drivetrain_class_id INTEGER NOT NULL REFERENCES class_defs(id),
  is_deleted INTEGER NOT NULL DEFAULT 0
);

-- 紐づけ (N:M、相乗り=マルチエントリー)
CREATE TABLE IF NOT EXISTS entries (
  driver_id INTEGER NOT NULL REFERENCES drivers(id),
  vehicle_id INTEGER NOT NULL REFERENCES vehicles(id),
  PRIMARY KEY (driver_id, vehicle_id)
);

-- 出走キュー + コース状態 (状態機械の実体)
CREATE TABLE IF NOT EXISTS queue (
  id INTEGER PRIMARY KEY AUTOINCREMENT,
  driver_id INTEGER NOT NULL REFERENCES drivers(id),
  vehicle_id INTEGER NOT NULL REFERENCES vehicles(id),
  position REAL,                                   -- waiting内の並び。挿入は間の実数。隣接差が1e-9未満になったらwaiting全体を1.0刻みでリナンバー (稀)
  status TEXT NOT NULL DEFAULT 'waiting',          -- 'waiting' | 'on_course' | 'done' | 'canceled'
  t_start_us INTEGER,                              -- スタート打刻(μs)。on_courseでNULL=READY(センサー待ち)
  pt_count INTEGER NOT NULL DEFAULT 0,             -- 走行中付与分。ログ生成時に引き継ぐ
  mc_flag INTEGER NOT NULL DEFAULT 0,              -- ミスコース予約
  created_by INTEGER REFERENCES drivers(id)        -- 自己投入の監査用
);

CREATE TABLE IF NOT EXISTS logs (
  id INTEGER PRIMARY KEY AUTOINCREMENT,
  driver_id INTEGER REFERENCES drivers(id),        -- NULL = 未割当ログ
  vehicle_id INTEGER REFERENCES vehicles(id),
  raw_ms INTEGER NOT NULL,                         -- 計測タイム(ms、切り捨て)
  pt_count INTEGER NOT NULL DEFAULT 0,
  is_mc INTEGER NOT NULL DEFAULT 0,
  timestamp_ms INTEGER NOT NULL,                   -- 記録時刻(UNIX ms)。並び順の正はこれ (log_idは同刻タイブレークのみ)
  source TEXT NOT NULL,                            -- 'sensor' | 'manual'
  edited_at INTEGER,                               -- 運営編集時刻(ms)。NULL=未編集
  is_deleted INTEGER NOT NULL DEFAULT 0
);

-- 生トリガー全件保存 (ペアリングと独立のセーフティネット)
CREATE TABLE IF NOT EXISTS sensor_events (
  id INTEGER PRIMARY KEY AUTOINCREMENT,
  sensor_id TEXT NOT NULL,                         -- 'start' | 'goal'
  boot_id INTEGER NOT NULL,
  seq INTEGER NOT NULL,
  timestamp_us INTEGER NOT NULL,                   -- ESP32打刻 (chrony時刻系)
  received_at INTEGER NOT NULL,
  UNIQUE (sensor_id, boot_id, seq)                 -- 3連送の重複排除
);

-- 運営操作の監査ログ
CREATE TABLE IF NOT EXISTS audit (
  id INTEGER PRIMARY KEY AUTOINCREMENT,
  at_ms INTEGER NOT NULL,
  driver_id INTEGER,                               -- 操作者
  action TEXT NOT NULL,                            -- 'log.edit' 'queue.launch' 'user.reissue' 等
  detail TEXT                                      -- JSON
);
`
