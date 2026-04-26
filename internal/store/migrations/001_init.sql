-- Migration tracking table (must be first so subsequent migrations can self-register)
CREATE TABLE IF NOT EXISTS schema_migrations (
    version    INTEGER PRIMARY KEY,
    applied_at TIMESTAMP DEFAULT CURRENT_TIMESTAMP
);

-- 顯性預留（持久化）
CREATE TABLE IF NOT EXISTS reservations (
    port         INTEGER PRIMARY KEY,
    name         TEXT NOT NULL,
    note         TEXT,
    created_at   TIMESTAMP DEFAULT CURRENT_TIMESTAMP,
    created_by   TEXT
);

-- 最新掃描快照（每次掃描清空後重寫）
CREATE TABLE IF NOT EXISTS scan_snapshots (
    port             INTEGER PRIMARY KEY,
    state            TEXT NOT NULL,        -- 'used_process' | 'used_docker' | 'reserved_implicit' | 'free'
    pid              INTEGER,
    process_name     TEXT,
    cmdline          TEXT,
    container_id     TEXT,
    container_name   TEXT,
    container_image  TEXT,
    container_status TEXT,                  -- 'running' | 'exited' | 'created' | 'paused' | 'dead'
    ufw_action       TEXT,                  -- 'allow' | 'deny' | 'limit' | 'complex' | NULL
    ufw_rule_num     INTEGER
);

-- 掃描中繼資訊（只有一筆）
CREATE TABLE IF NOT EXISTS scan_meta (
    id            INTEGER PRIMARY KEY CHECK (id = 1),
    last_scan_at  TIMESTAMP NOT NULL
);

-- 歷史事件（保留 30 天）
CREATE TABLE IF NOT EXISTS history (
    id           INTEGER PRIMARY KEY AUTOINCREMENT,
    port         INTEGER NOT NULL,
    event        TEXT NOT NULL,           -- 'occupied' | 'released' | 'reserved' | 'unreserved'
    occupant     TEXT,                    -- container name 或 process name
    timestamp    TIMESTAMP DEFAULT CURRENT_TIMESTAMP
);

CREATE INDEX IF NOT EXISTS idx_history_port ON history(port);
CREATE INDEX IF NOT EXISTS idx_history_time ON history(timestamp);
