# Piper — Phase 1 MVP 設計文件

**Date:** 2026-04-25
**Author:** ymu
**Status:** Design — pending implementation
**Target deployment:** H100 server, DGX Spark (Linux x86_64 / aarch64)

---

## 1. 專案概述

### 1.1 背景

目前團隊在 H100 與 DGX Spark 上運行多個 AI 服務（FastAPI 後端、vLLM、Ollama、translate server、subtitle pipeline 等），其中：

- 部分服務以 Docker container 形式部署，自帶 port 配置
- 部分服務直接以 systemd / nohup 跑在主機上
- 防火牆（UFW）規則是另一份手動維護的設定
- 目前依賴一份手動維護的 markdown 文件，並手動同步到一個 HTML 頁面供團隊查閱

這四份資訊（系統實際 listening port、Docker 配置、UFW 規則、手寫文件）彼此不同步，每次新建 container 時容易撞 port 或忘記開防火牆，且舊 container 刪除後 port 沒有及時回收。

### 1.2 目標（In Scope）

Piper 是一個單一 Go binary 工具，提供：

1. **即時 port 狀態查詢**：給定 port，回答「能不能用 / 被誰佔 / Docker container 狀態 / UFW 規則」
2. **Container 全 port 視圖**：查到某 port 屬於某 container 時，自動展開該 container 所有對外綁定 port 的狀態
3. **顯性預留登記**：手動登記「這個 port 留給某個尚未存在的 container」，避免被自動建議搶走
4. **隱性預留識別**：自動偵測 stopped container 的 port 配置（重啟即恢復）
5. **閒置 port 建議**：從指定範圍找出可立即配置的 port
6. **Web 共享面板**：替換現有手動維護的 HTML，提供團隊唯讀檢視
7. **歷史事件追溯**：誰在何時佔了某 port、何時釋放

### 1.3 非目標（Out of Scope）

以下不在 Phase 1 範圍：

- **跨主機**：每台機器跑獨立 piper instance，不互通
- **修改系統**：piper 不執行 `ufw allow`、不 stop/start container、不 kill process
- **使用者認證**：bind 0.0.0.0 + UFW 內網保護，無密碼/token
- **多使用者權限**：寫入操作限 localhost，全機器共用一份資料
- **Port range 分群**：不需要「30000-30099 留給 AI 服務」這類分類功能
- **跨平台 GUI**：純 CLI + Web，無 desktop app

---

## 2. 名稱與隱喻

### 2.1 名稱：piper

源自德國民間故事「魔笛手」（Pied Piper of Hamelin / Hamelin's Piper）。魔笛手吹奏笛聲就能讓所有老鼠跟著走，呼應本工具「召喚一聲，所有 port 資訊排好隊出來」的核心體驗。

選擇 `piper` 而非 `pied-piper` 的理由：

- 單字 5 字母，CLI 好打
- 避開 Silicon Valley 影集的具體聯想（影集 Pied Piper 是壓縮演算法，與 port 無關）
- 執行檔、設定檔、SQLite 檔命名統一順暢：`piper`、`piper.yaml`、`piper.db`

### 2.2 視覺/語言調性

- 預留登記稱為 "reservation"（不是 "lock" 或 "claim"，語感較中性）
- 隱性預留稱為 "implicit reservation"，顯性稱為 "explicit reservation"
- Web UI 標語建議：`"piper — call once, all ports come marching"`（可選，README/首頁裝飾用）

---

## 3. 核心使用情境（User Stories）

### US-1：新建 container 前查可用 port
> 「我要部署一個新的 vLLM 服務，需要兩個對外 port，從 8000-9999 找兩個沒人用的給我。」

對應指令：`piper suggest -n 2`

### US-2：排查特定 port 是否能用
> 「我想用 8080，但好像 8081 之前也被某個東西佔過，這幾個能用嗎？」

對應指令：`piper check 8080 8081`

### US-3：服務從外部連不上
> 「我的 ai-translate-server 開了 metrics port 9090，從別台機器 curl 不到，是不是 UFW 沒開？」

對應指令：`piper check 9090`（自動展開該 container 全 port 與 UFW 狀態）

### US-4：刪掉 container 後忘記 port 號是多少
> 「我上週刪了一個測試 container，那組 port 現在哪些是真的空的？」

對應流程：piper 掃描週期偵測到 container 已不存在 → history 表記錄 release 事件 → `piper list --free` 顯示為可用

### US-5：團隊成員想看當前 port 配置
> 「PM 想知道現在 H100 上有哪些服務在跑、各自佔哪些 port。」

對應流程：PM 瀏覽器打開 `http://h100:7878`，看到唯讀面板

### US-6：登記預留給尚未建立的 container
> 「下週要部署一個新服務，先把 9100 留住別讓自動建議拿走。」

對應指令：`piper reserve 9100 --name vllm-llama --note "next week deployment"`

---

## 4. CLI 命令設計

CLI 使用 `cobra` 框架。所有子命令支援 `--output FILE` 與 `--format text|json`。

### 4.1 全域選項

```
piper [global flags] <command> [command flags]

Global flags:
  --data-dir PATH       資料目錄（預設 ~/.piper/，環境變數 PIPER_DATA_DIR 可覆寫）
  --format FORMAT       輸出格式：text（預設）| json
  --output FILE         tee 模式：螢幕照印同時寫入檔案
                        如果只給 --output 不接路徑，預設寫入 ./piper-query.txt
  --no-color            停用 ANSI 顏色輸出
  -h, --help
  -v, --version
```

### 4.2 子命令總覽

```
piper serve              # 啟動 web UI + 背景掃描
piper scan               # 立刻掃描一次，輸出表格
piper check <ports...>   # 查單一或多個 port 詳細狀態（核心命令）
piper list               # 列出 port 清單（依條件過濾）
piper suggest            # 建議閒置 port
piper reserve <port>     # 新增顯性預留
piper release <port>     # 取消顯性預留
piper history            # 查詢歷史事件
piper refresh            # 強制重新掃描
```

### 4.3 `piper serve`

啟動常駐 daemon：HTTP server + 背景掃描排程 + 過期清理排程。

```
piper serve [flags]

Flags:
  --port PORT           HTTP 監聽 port（預設 7878）
  --host HOST           HTTP 監聽位址（預設 0.0.0.0）
  --scan-interval DURATION  掃描週期（預設 5m）
```

啟動行為：

1. 連線/建立 SQLite，跑 migrations
2. **啟動清理**：刪除 history 表中超過 30 天的紀錄
3. 立刻執行一次掃描（不等待 5 分鐘）
4. 啟動 HTTP server bind 0.0.0.0:7878
5. 啟動兩個 goroutine：
   - 掃描排程（每 5 分鐘）
   - 過期清理排程（每天午夜清理 history > 30 天）
6. 寫入 stdout：`piper serving on http://0.0.0.0:7878 (data: ~/.piper/piper.db)`

### 4.4 `piper scan`

立刻執行一次完整掃描，輸出表格，不寫入持久化（除非 piper serve 同時在跑，那會走它的排程）。

實作上 `piper scan` 是 `piper list --used --reserved`（已佔用 + 預留）的便利別名。

### 4.5 `piper check <ports...>` ⭐核心命令

```
piper check <port1> [port2 port3 ... | port-range]

Examples:
  piper check 8080
  piper check 8080 8081 9000
  piper check 8080-8085
  piper check 8080 9000-9005 9100
```

#### 行為

對每個 port 跑「決策樹」（見 §6.1），輸出每個 port 的完整狀態。當 port 被某個 docker container 佔用時，**自動展開該 container 的所有其他對外綁定 port**，每個都附 UFW 狀態。

#### 去重邏輯

如果輸入的多個 port 屬於同一 container，第二次出現的 container 簡化顯示為 `(see port 8080)`。

#### 輸出範例（text format）

```
$ piper check 8080 8081 9000

Port 8080  ❌ IN USE (docker)
  ├─ Process: docker-proxy (pid 12453)
  ├─ Container: ai-translate-server
  │             image: translate:v2
  │             status: Up 3 days
  ├─ UFW: ALLOW (rule #5: 8080/tcp)
  └─ Other ports on this container:
      ├─ 8443  → UFW: ALLOW    ✅
      ├─ 9090  → UFW: (no rule, default deny)  ⚠️
      └─ 9091  → UFW: (no rule, default deny)  ⚠️

Port 8081  ⚠️  RESERVED (no listener)
  ├─ Source: implicit (stopped container)
  ├─ Container: subtitle-server (status: Exited 2 days ago)
  ├─ UFW: ALLOW (rule #7: 8081/tcp)
  └─ This port is technically free; restart container to reclaim.

Port 9000  ✅ AVAILABLE
  ├─ No listener, no reservation
  └─ UFW: (no rule)

Scanned at 2026-04-25 14:32:18
```

#### 輸出範例（json format）

```json
{
  "scanned_at": "2026-04-25T14:32:18+08:00",
  "results": [
    {
      "port": 8080,
      "state": "used_docker",
      "occupant": {
        "type": "docker",
        "container_id": "a3f5...",
        "container_name": "ai-translate-server",
        "container_image": "translate:v2",
        "container_status": "running",
        "container_uptime": "3 days"
      },
      "ufw": {"action": "allow", "rule_num": 5},
      "container_other_ports": [
        {"port": 8443, "ufw": {"action": "allow"}},
        {"port": 9090, "ufw": null},
        {"port": 9091, "ufw": null}
      ],
      "reservation": null
    },
    {
      "port": 8081,
      "state": "reserved_implicit",
      "occupant": null,
      "ufw": {"action": "allow", "rule_num": 7},
      "container_other_ports": null,
      "reservation": {
        "source": "implicit",
        "container_name": "subtitle-server",
        "container_status": "exited"
      }
    },
    {
      "port": 9000,
      "state": "free",
      "occupant": null,
      "ufw": null,
      "container_other_ports": null,
      "reservation": null
    }
  ]
}
```

### 4.6 `piper list`

列出符合條件的 port。

```
piper list [flags]

Flags:
  --used                  只列被佔用的（含 process + docker）
  --free                  只列完全空閒的（無 listener、無預留、無 UFW 規則）
  --reserved              只列預留中的（顯性 + 隱性）
  --reserved-explicit     只列顯性預留
  --reserved-implicit     只列隱性預留
  --all                   列出所有有「狀態」的 port（預設行為）
  --container NAME        只列屬於指定 container 的 port
```

預設不列完全空閒的 port（避免列出 60000+ 筆）。`--free` 配合 `--from/--to` 限制範圍：

```
piper list --free --from 8000 --to 9999
```

### 4.7 `piper suggest`

```
piper suggest [flags]

Flags:
  -n COUNT          建議幾個（預設 1）
  --from PORT       搜尋起始（預設 8000）
  --to PORT         搜尋終止（預設 9999）
  --avoid-recent    避開 7 天內剛 release 的 port（Phase 2 功能，先預留 flag）
```

#### 演算法（Phase 1）

順序搜尋：從 `--from` 開始遞增，找出第一個 N 個符合條件的 port：

- 沒人在 listen
- 沒有顯性預留
- 沒有隱性預留（包含 stopped container）

不考量「最近剛 release」的智能規則，等使用一段時間後評估是否需要。

### 4.8 `piper reserve` / `piper release`

```
piper reserve <port> --name <service-name> [--note "..."]
piper release <port>
```

`reserve`：

- 寫入 SQLite reservations 表
- 如果該 port 當下有人在 listen，警告但仍允許登記（你可能在登記「將要替換目前佔用者」的計畫）
- 寫入 history 事件 `reserved`

`release`：

- 從 reservations 表刪除
- 寫入 history 事件 `unreserved`
- 不影響當下佔用情況

### 4.9 `piper history`

```
piper history [flags]

Flags:
  --port PORT           只看特定 port
  --days N              最近 N 天（預設 7）
  --event EVENT         只看特定事件類型：occupied | released | reserved | unreserved
  --limit N             最多顯示 N 筆（預設 50）
```

### 4.10 `piper refresh`

僅在 `piper serve` 也在跑時有意義：透過 HTTP 通知 serve 進程立刻執行一次掃描，不等待週期。如果 serve 沒在跑，`piper refresh` 等同 `piper scan`（自己掃一次但不寫入 DB）。

---

## 5. Web UI 設計

### 5.1 技術棧

- **後端**：Go 標準庫 `net/http`（Go 1.22+ 的 ServeMux 已支援 method routing）
- **前端**：HTMX + Pico.css，全部 `go:embed` 進 binary
- **無編譯流程**：純 HTML/CSS/JS 靜態資源

### 5.2 路由

| 路由 | 內容 |
|------|------|
| `/` | Overview 頁 |
| `/reservations` | 預留管理頁 |
| `/history` | 歷史事件頁 |
| `/static/*` | embedded 靜態資源（htmx.min.js、pico.min.css） |

### 5.3 Overview 頁

最常用的頁面。Layout 從上到下：

1. **頂部 Sticky 工具列**：
   - 「Check ports」輸入框 + Go 按鈕（接受 batch 與 range，例如 `8080 9000-9005`）
   - 「Suggest free」輸入框（n、from、to）+ Suggest 按鈕
   - 「Refresh now」按鈕（觸發 `POST /api/scan/trigger`）
   - 「Last scan: X min ago」狀態文字

2. **Port 狀態主表格**：列出所有「有狀態」的 port（被佔、預留、有 UFW 規則）。欄位：

   | Port | State | Occupant | Container Status | UFW | Action |
   |------|-------|----------|------------------|-----|--------|
   | 8080 | 🟢 USED | ai-translate-server | Up 3d | allow ✅ | [view] |
   | 8081 | 🔒 IMPL | subtitle-server | Exited | allow | [view] |
   | 9000 | 📌 EXPL | (planned-X) | — | none | [view] |

   **不列完全空閒 port**。看空閒 port 走 Suggest 功能。

3. **Detail 區（折疊）**：點 [view] 或 Check 按鈕後展開，顯示 §4.5 的詳細視圖（含 container 全 port）。

### 5.4 Reservations 頁

只列顯性預留：

| Port | Name | Note | Created | By | Action |
|------|------|------|---------|-----|--------|
| 9100 | vllm-llama | DGX 部署用 | 2026-04-22 | ymu | [del] |

頁面下方有「+ New reservation」表單（port、name、note）。

**寫入按鈕（New / del）的可見性規則**：
- 若 request 來自 `127.0.0.1`：按鈕顯示且可點
- 否則：按鈕完全不渲染（連 disabled state 都不出現，避免擾民）

判斷方式：後端 handler 在 render template 時根據 `r.RemoteAddr` 注入 `IsLocalhost` 變數。

### 5.5 History 頁

時間軸式呈現（最新在上）：

```
2026-04-25 12:03  port 9100  occupied  vllm-llama
2026-04-25 11:47  port 9100  released
2026-04-24 23:01  port 8080  occupied  ai-translate-server
```

過濾選項：
- Port 過濾（輸入框）
- 事件類型 checkbox（occupied / released / reserved / unreserved）
- 時間範圍（最近 1 天 / 7 天 / 30 天）

### 5.6 互動模式

- **預設靜態**：頁面載入時讀取最新快照，顯示「Last scan: X min ago」
- **手動 refresh**：按 Refresh now 觸發後端立刻掃描，掃完返回新資料
- **HTMX 局部更新**：點按鈕只更新對應區塊，不整頁 reload
- **不做 WebSocket / 自動 polling**：5 分鐘掃描週期下沒有意義

### 5.7 視覺風格

- Pico.css 預設 light theme（亦支援 OS prefers-color-scheme dark）
- Port state 使用色塊 + emoji：
  - 🟢 USED — 綠色（運作中）
  - 🟠 USED but UFW issue — 橘色（佔用但 UFW 異常）
  - 🔒 IMPLICIT RESERVED — 灰色
  - 📌 EXPLICIT RESERVED — 藍色
  - ✅ AVAILABLE — 不顯示在表格（除非 `--free` 查詢）

---

## 6. JSON API Contract

### 6.1 路由表

| Method | Path | 說明 | Localhost only |
|--------|------|------|---------------|
| GET | `/api/scan/latest` | 最新掃描快照（全部 port） | No |
| GET | `/api/port/:port` | 單一 port 詳細狀態 | No |
| POST | `/api/check` | Batch check | No |
| GET | `/api/suggest` | 建議閒置 port | No |
| GET | `/api/reservations` | 列出顯性預留 | No |
| POST | `/api/reservations` | 新增預留 | **Yes** |
| DELETE | `/api/reservations/:port` | 刪除預留 | **Yes** |
| GET | `/api/history` | 查歷史事件 | No |
| POST | `/api/scan/trigger` | 手動觸發掃描 | **Yes** |
| GET | `/api/health` | 健康檢查 | No |

「Localhost only」欄位 = Yes 的路由由 middleware 檢查 `r.RemoteAddr`，非 127.0.0.1 一律回 403。

### 6.2 通用 Response 結構

成功：

```json
{
  "ok": true,
  "data": { ... }
}
```

失敗：

```json
{
  "ok": false,
  "error": {
    "code": "PORT_NOT_FOUND",
    "message": "Port 99999 is out of valid range"
  }
}
```

### 6.3 主要 endpoint 詳述

#### `GET /api/port/:port`

Response data：見 §4.5 json 範例的單一物件結構。

#### `POST /api/check`

Request：

```json
{"ports": [8080, 8081, "9000-9005"]}
```

Response data：

```json
{
  "scanned_at": "2026-04-25T14:32:18+08:00",
  "results": [ /* 同 /api/port/:port 的結構，陣列 */ ]
}
```

#### `GET /api/suggest`

Query params：`n`, `from`, `to`

Response：

```json
{
  "ok": true,
  "data": {
    "suggested": [8005, 8006, 8011],
    "search_range": {"from": 8000, "to": 9999},
    "scanned_at": "..."
  }
}
```

#### `POST /api/reservations`

Request：

```json
{"port": 9100, "name": "vllm-llama", "note": "DGX 部署用"}
```

Response：

```json
{
  "ok": true,
  "data": {
    "port": 9100,
    "name": "vllm-llama",
    "note": "DGX 部署用",
    "created_at": "2026-04-25T14:35:00+08:00",
    "created_by": "ymu"
  }
}
```

`created_by` 從進程 `os/user` 取得。

---

## 7. 資料模型

### 7.1 SQLite Schema

```sql
-- 顯性預留（持久化）
CREATE TABLE reservations (
    port         INTEGER PRIMARY KEY,
    name         TEXT NOT NULL,
    note         TEXT,
    created_at   TIMESTAMP DEFAULT CURRENT_TIMESTAMP,
    created_by   TEXT
);

-- 最新掃描快照（每次掃描清空後重寫）
CREATE TABLE scan_snapshots (
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
CREATE TABLE scan_meta (
    id            INTEGER PRIMARY KEY CHECK (id = 1),
    last_scan_at  TIMESTAMP NOT NULL
);

-- 歷史事件（保留 30 天）
CREATE TABLE history (
    id           INTEGER PRIMARY KEY AUTOINCREMENT,
    port         INTEGER NOT NULL,
    event        TEXT NOT NULL,           -- 'occupied' | 'released' | 'reserved' | 'unreserved'
    occupant     TEXT,                    -- container name 或 process name
    timestamp    TIMESTAMP DEFAULT CURRENT_TIMESTAMP
);

CREATE INDEX idx_history_port ON history(port);
CREATE INDEX idx_history_time ON history(timestamp);
```

### 7.2 掃描資料流

每次掃描（手動觸發 or 排程）：

```
┌─────────────────────────────────────────────────────────┐
│ 1. 平行收集三來源（goroutine + sync.WaitGroup）            │
├─────────────────────────────────────────────────────────┤
│   ss -tlnp -H               → []SSEntry                  │
│   docker ps -a --format json → []DockerEntry            │
│   ufw status numbered       → []UFWRule                 │
└──────────┬──────────────────────────────────────────────┘
           │
           ▼
┌─────────────────────────────────────────────────────────┐
│ 2. 融合（service/checker.go）                            │
├─────────────────────────────────────────────────────────┤
│   建立 port → state 的 map                              │
│                                                         │
│   for entry in dockerEntries:                          │
│     for hostPort in entry.Ports:                       │
│       if entry.Status == 'running':                    │
│         state[hostPort] = used_docker                  │
│       else:                                            │
│         state[hostPort] = reserved_implicit            │
│                                                         │
│   for entry in ssEntries:                              │
│     if state[port] not set:                            │
│       state[port] = used_process                       │
│     # 若 ss 看到 docker-proxy，docker 那邊已蓋上資訊    │
│                                                         │
│   for rule in ufwRules:                                │
│     state[rule.port].ufw = rule                        │
└──────────┬──────────────────────────────────────────────┘
           │
           ▼
┌─────────────────────────────────────────────────────────┐
│ 3. Diff（service/differ.go）                            │
├─────────────────────────────────────────────────────────┤
│   讀取 scan_snapshots（上一次）                          │
│   比對：                                                │
│     - 上次 used / 這次 free → history.released         │
│     - 上次 free / 這次 used → history.occupied         │
└──────────┬──────────────────────────────────────────────┘
           │
           ▼
┌─────────────────────────────────────────────────────────┐
│ 4. 寫入（store/snapshot.go）                            │
├─────────────────────────────────────────────────────────┤
│   BEGIN TRANSACTION                                     │
│     DELETE FROM scan_snapshots                         │
│     INSERT INTO scan_snapshots ...                     │
│     UPDATE scan_meta SET last_scan_at = NOW            │
│     INSERT INTO history (...) (Diff 產生的事件)         │
│   COMMIT                                                │
└─────────────────────────────────────────────────────────┘
```

### 7.3 lsof 備援策略

`lsof` **不在週期掃描中執行**，僅在以下情境啟用：

- `piper check <port>` 指定的 port 在 ss 看不到、docker 也看不到，但使用者覺得「可能有東西在用」
- Web UI 的 detail 區提供「deep check」按鈕（將來功能，Phase 1 不做）

執行命令：`lsof -i :PORT -P -n`

### 7.4 Docker port 對應

`ss -tlnp` 看到 docker-proxy 進程時，piper 不需要追 pid → cgroup → container。
直接用 `docker ps -a --format '{{json .}}'` 取得每個 container 的 `Ports` 欄位（含 host port），用 host port 反查 container 即可。

`docker inspect <container>` 提供更完整的 `NetworkSettings.Ports`，用於展開「該 container 全部對外綁定 port」。

只解析有 `HostPort` 綁定的條目（即 `docker run -p` 配置的），不解析純 EXPOSE。

### 7.5 UFW 解析

執行：`ufw status numbered`

Output 範例：

```
Status: active

     To                         Action      From
     --                         ------      ----
[ 1] 22/tcp                     ALLOW IN    Anywhere
[ 2] 8080/tcp                   ALLOW IN    Anywhere
[ 3] 7878/tcp                   ALLOW IN    Anywhere
```

**Phase 1 只解析簡單格式**：`PORT/PROTO  ACTION IN  Anywhere`

複雜規則（指定 from IP / IP range / 特定 interface）標示為 `ufw_action = 'complex'`，並在 web UI 提示「使用 `ufw status` 查看完整規則」。

### 7.6 過期清理

`piper serve` 在以下時機執行：

- **啟動時**：執行一次清理
- **每天午夜（Asia/Taipei 時區）**：cron-like 排程

清理 SQL：

```sql
DELETE FROM history WHERE timestamp < datetime('now', '-30 days');
```

---

## 8. 套件分層與目錄結構

```
piper/
├── cmd/piper/
│   └── main.go                     # cobra root command + 子命令掛載
│
├── internal/
│   ├── scanner/                    # 純讀系統（無 DB / HTTP 依賴）
│   │   ├── scanner.go              # Scan() 入口 + 平行統籌
│   │   ├── ss.go                   # parseSS([]byte) []SSEntry
│   │   ├── lsof.go                 # parseLsof(...) (備援)
│   │   ├── docker.go               # listContainers() / inspectContainer()
│   │   ├── ufw.go                  # parseUFWStatus([]byte) []UFWRule
│   │   └── types.go                # SSEntry, DockerEntry, UFWRule
│   │
│   ├── store/                      # SQLite 操作層
│   │   ├── store.go                # Store struct, Open(), Close()
│   │   ├── migrate.go              # embed migrations，自動套用
│   │   ├── reservation.go          # Reservation CRUD
│   │   ├── snapshot.go             # SaveSnapshot, GetSnapshot
│   │   └── history.go              # AppendEvent, QueryEvents, Cleanup
│   │
│   ├── service/                    # 業務邏輯（讀 scanner + store，產生答案）
│   │   ├── checker.go              # CheckPort() 主決策樹
│   │   ├── suggester.go            # SuggestFreePorts()
│   │   ├── differ.go               # DiffSnapshots() 產生 history 事件
│   │   ├── scheduler.go            # 掃描排程 + 清理排程
│   │   └── types.go                # PortStatus, CheckResult
│   │
│   ├── server/                     # Web/API 層
│   │   ├── server.go               # http.Server 啟動
│   │   ├── routes.go               # ServeMux 路由註冊
│   │   ├── handlers_api.go         # /api/* handlers
│   │   ├── handlers_web.go         # /, /reservations, /history handlers
│   │   ├── middleware.go           # localhostOnly, rateLimit, logging
│   │   ├── templates.go            # html/template 載入 + render helper
│   │   └── web/                    # embedded 靜態資源
│   │       ├── templates/
│   │       │   ├── layout.html
│   │       │   ├── overview.html
│   │       │   ├── reservations.html
│   │       │   └── history.html
│   │       └── static/
│   │           ├── pico.min.css
│   │           ├── htmx.min.js
│   │           └── piper.css       # 自訂樣式
│   │
│   ├── cli/                        # CLI 子命令實作
│   │   ├── check.go
│   │   ├── list.go
│   │   ├── suggest.go
│   │   ├── reserve.go
│   │   ├── history.go
│   │   ├── scan.go
│   │   ├── serve.go
│   │   ├── refresh.go
│   │   └── output.go               # text/json formatter + tee
│   │
│   └── version/
│       └── version.go              # ldflags 注入版本號
│
├── migrations/
│   └── 001_init.sql                # 初始 schema
│
├── go.mod
├── go.sum
├── Makefile                        # build, build-arm64, test, lint
├── README.md                       # 英文版
├── README.zh-TW.md                 # 中文版
└── docs/
    └── superpowers/
        └── specs/
            └── 2026-04-25-phase1-mvp-design.md  # 本文件
```

### 分層原則

- `scanner` → 不 import `store`、`service`、`server`
- `store` → 不 import `scanner`、`service`、`server`
- `service` → import `scanner` + `store`
- `server` 與 `cli` → import `service`，互不 import 對方
- `service` 是唯一的業務邏輯層，CLI 與 Web 共用

---

## 9. 技術選型

| 元件 | 選擇 | 理由 |
|------|------|------|
| 語言 | Go 1.23+ | 單 binary 分發、跨架構編譯、團隊熟悉 |
| CLI 框架 | `spf13/cobra` | 業界標準，子命令 + flag 處理成熟 |
| HTTP server | 標準庫 `net/http` | Go 1.22+ ServeMux 已支援 method routing，10 個路由不需要框架 |
| SQLite 驅動 | `modernc.org/sqlite` | 純 Go，免 cgo，跨架構編譯一行搞定（H100 x86_64 + Spark aarch64） |
| 前端互動 | HTMX | 宣告式互動，無編譯流程，配合 `go:embed` 完美 |
| CSS | Pico.css | classless，幾乎不用學，10KB |
| 靜態資源 | `go:embed` | 單 binary 分發，離線部署友善 |
| 資料目錄 | `~/.piper/`（可 `PIPER_DATA_DIR` 覆寫） | 使用者私有，免 sudo |
| 設定檔 | 不需要 | Phase 1 所有設定走 CLI flag + 環境變數 |
| 日誌 | `log/slog`（標準庫） | 結構化 log，無第三方依賴 |

### 不採用的選項與理由

- **`mattn/go-sqlite3`**：cgo 依賴，跨架構編譯麻煩
- **`gin` / `chi` / `echo`**：路由量太少，標準庫足夠
- **WebSocket**：5 分鐘掃描週期下沒有 push 必要
- **JWT / session 認證**：bind 0.0.0.0 + UFW + 寫入限 localhost 已夠安全
- **Tailwind / Vue / React**：殺雞用牛刀

---

## 10. 部署與運維

### 10.1 Build

```makefile
# Makefile

.PHONY: build build-amd64 build-arm64 test lint clean

VERSION := $(shell git describe --tags --always --dirty 2>/dev/null || echo "dev")
LDFLAGS := -ldflags "-s -w -X github.com/ymu/piper/internal/version.Version=$(VERSION)"

build: build-amd64 build-arm64

build-amd64:
	GOOS=linux GOARCH=amd64 CGO_ENABLED=0 go build $(LDFLAGS) -o dist/piper ./cmd/piper

build-arm64:
	GOOS=linux GOARCH=arm64 CGO_ENABLED=0 go build $(LDFLAGS) -o dist/piper-arm64 ./cmd/piper

test:
	go test -race -cover ./...

lint:
	go vet ./...
	golangci-lint run

clean:
	rm -rf dist/
```

### 10.2 部署流程

H100（x86_64）：

```bash
# 在 WSL 開發機編譯
make build-amd64

# 傳到 H100
scp dist/piper ymu@h100:/home/ymu/

# H100 上開 UFW（一次性）
ssh ymu@h100 'sudo ufw allow 7878/tcp'

# 啟動（手動跑階段）
ssh ymu@h100 'nohup /home/ymu/piper serve > ~/.piper/piper.log 2>&1 &'
```

Spark（aarch64）：同上，改用 `dist/piper-arm64`。

### 10.3 資料目錄結構

```
~/.piper/
├── piper.db          # SQLite 主檔
└── piper.log         # nohup 模式的輸出（自行 redirect）
```

啟動時自動建立 `~/.piper/`（mkdir 0700），DB 檔 chmod 0600。

### 10.4 systemd unit（Phase 2 才採用）

附範例在 README，但 Phase 1 用 nohup 即可：

```ini
# /etc/systemd/system/piper.service (Phase 2)
[Unit]
Description=Piper port management
After=network.target docker.service

[Service]
Type=simple
User=ymu
ExecStart=/home/ymu/piper serve
Restart=on-failure
RestartSec=5

[Install]
WantedBy=multi-user.target
```

### 10.5 升級流程

1. 在開發機 `make build`
2. `scp` 新 binary 覆蓋舊的（先 kill 舊進程）
3. `nohup ./piper serve &`

SQLite migrations 自動套用（程式啟動時檢查 schema version）。

---

## 11. 安全模型

### 11.1 信任邊界

| 來源 | 可讀 | 可寫 |
|------|------|------|
| 同主機本機（127.0.0.1） | ✅ | ✅ |
| 同網段其他主機（透過 UFW 允許） | ✅ | ❌ |
| 外網 | ❌ | ❌（UFW 預設 deny + 工廠網路隔離） |

### 11.2 寫入路由保護

`server/middleware.go` 提供 `requireLocalhost` middleware：

```go
func requireLocalhost(next http.Handler) http.Handler {
    return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
        host, _, err := net.SplitHostPort(r.RemoteAddr)
        if err != nil || (host != "127.0.0.1" && host != "::1") {
            http.Error(w, "forbidden: write operations require localhost", http.StatusForbidden)
            return
        }
        next.ServeHTTP(w, r)
    })
}
```

套用到 §6.1 標記為「Localhost only」的路由。

### 11.3 Rate limit

簡單的 per-IP token bucket，預設 10 req/sec/IP，避免有人 polling 太兇拖慢機器。Phase 1 採記憶體實作（重啟即清），不寫 DB。

### 11.4 SQLite 權限

- `~/.piper/` 目錄 chmod 0700（僅擁有者）
- `piper.db` chmod 0600
- 多使用者環境下，每個使用者各自有自己的 piper instance（`~/.piper/`），互不干擾

### 11.5 不執行 sudo

Piper 全程不需要 sudo：

- `ss`、`docker ps` 一般使用者可執行（前提：使用者在 `docker` group）
- `ufw status` 一般使用者可讀（透過 sudoers 或 group 權限，依環境而定）

如果 `ufw status` 需要 sudo，piper 啟動時偵測權限不足，提示使用者：

```
warning: ufw status requires sudo. Add ymu to a group with read access, or run:
  sudo setcap cap_dac_read_search+ep /home/ymu/piper
ufw rule scanning will be disabled until resolved.
```

---

## 12. 開發階段規劃

### Phase 1（本文件範圍，MVP）

- ✅ Scanner：ss + docker + ufw 三來源融合
- ✅ Store：SQLite reservations / snapshots / history
- ✅ Service：CheckPort + SuggestFreePorts + DiffSnapshots
- ✅ CLI：scan / check / list / suggest / reserve / release / history / serve / refresh
- ✅ Web：Overview / Reservations / History 三頁
- ✅ API：§6.1 列表
- ✅ go:embed 單 binary 分發
- ✅ 跨架構編譯（amd64 + arm64）

### Phase 2（將來考慮，非本文件承諾）

- Container exited 狀態自動偵測為「孤兒預留」並標記
- `--avoid-recent` 智能建議（避開最近 7 天 release 的 port）
- `lsof` deep check 按鈕
- systemd service 整合
- Prometheus exporter（`/metrics`）
- 跨主機聯邦（多台機器資料聚合）
- 寫入 token 認證（如需要遠端管理）

### Phase 3（更遠的願景，非承諾）

- ufw 規則建議與 dry-run（不執行，僅產生命令給使用者複製）
- container 配置變更建議（例如「你的 9090 沒開 UFW，是不是忘了？」）
- 與 Gitea / GitLab 整合：自動讀取 docker-compose.yml 做預期 vs 實際的 diff

---

## 13. 開放問題

開發過程中可能浮現的細節，先記錄：

1. **IPv6 支援**：`ss -tlnp` 預設同時列 IPv4 / IPv6，融合時要小心 `[::]:8080` 與 `0.0.0.0:8080` 視為同一 port 還是分開。Phase 1 建議視為同一 port（顯示 IPv4 為主）。

2. **同一 port 多進程 listen**（罕見但合法，例如 SO_REUSEPORT）：Phase 1 取第一筆顯示，加註 `(+N more)`。

3. **Docker user-defined networks**：如果 container 不用 host port mapping 而是內部網路通訊（`network_mode: bridge` 但無 `-p`），piper 不會看到。這合預期 — 我們關心的是「對 host 暴露的 port」。

4. **UFW 不啟用時的處理**：`ufw status` 輸出 `Status: inactive` 時，所有 port 的 ufw_action 標 NULL，並在 web UI 頂部 banner 提示。

5. **時區**：所有 timestamp 以 UTC 儲存，顯示時轉 Asia/Taipei。

---

## 14. 驗收標準（Acceptance Criteria）

Phase 1 完成需通過：

- [ ] `piper check 8080` 對 listening port 與 stopped container 均能正確識別狀態
- [ ] `piper check 8080` 自動展開所屬 container 的全 port + UFW 狀態
- [ ] `piper suggest -n 3 --from 8000 --to 9999` 回傳 3 個確實未被 listen / reserve 的 port
- [ ] `piper reserve` / `piper release` 正確讀寫 SQLite，CLI 與 Web 雙向同步
- [ ] `piper serve` 在 0.0.0.0:7878 啟動，瀏覽器從同網段其他機器可瀏覽
- [ ] 從非 localhost 嘗試 POST `/api/reservations` 回傳 403
- [ ] 容器被刪除後 5 分鐘內，history 表記錄 `released` 事件
- [ ] 啟動 1 個月後，history 表自動清除超過 30 天的紀錄
- [ ] 同一份原始碼能編出 amd64 與 arm64 兩個 binary
- [ ] Binary 透過 `go:embed` 內建 web 靜態資源，離線環境可運作

---

**文件結束**
