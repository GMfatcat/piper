# piper

> *吹一聲笛，所有 port 排好隊出來*

單一 Go binary，用來管理 Linux 主機上 Docker container 與 host-level 服務混雜時的 port 配置。Piper 將三個事實來源 — `ss -tlnp` 監聽、`docker ps -a` port 綁定、`ufw status numbered` 規則 — 融合成一致的視圖，持久化到 SQLite，並透過 CLI 和內嵌 Web UI 提供查詢。

[English README](./README.md) · [Phase 1 設計文件](./2026-04-25-phase1-mvp-design.md) · [驗收狀態](./ACCEPTANCE.md)

---

## 為什麼需要 piper

典型實驗室／伺服器環境中，「這個 port 現在誰在用？」這個問題的四個答案彼此不同步：

- kernel 的 listener 表
- Docker 的 port 綁定（running 與 stopped container 都算）
- UFW 規則
- 某人手動維護的 wiki 頁面

新建 container 時 port 撞號；刪除 container 後 port 技術上空著但沒人敢重用。piper 讓你問一次就拿到真相。

## 狀態

**Phase 1 MVP — 實作完成，待真機 smoke 驗證。**

設計文件的 10 條驗收條件中 9 條已在本機驗證（cross-compile、embedded asset、Linux binary CLI e2e smoke）。剩下：用真實 `ss`/`docker`/`ufw` 輸出驗證 parser 覆蓋率、以及 LAN 瀏覽器對 `piper serve` 的 smoke。

## 安裝 / 編譯

需要 Go 1.26+。SQLite 走純 Go 實作（`modernc.org/sqlite`），不需 cgo，cross-compile 一行就過。

```bash
git clone <repo>
cd piper
make build           # 產出 dist/piper (linux amd64) + dist/piper-arm64
make build-host      # 當前 OS/arch（開發機方便用）
make test            # 完整測試
make test-race       # 帶 race detector
```

Make 目標：

| 目標 | 產出 |
|------|------|
| `make build-amd64` | `dist/piper` — Linux x86_64，靜態連結，約 7 MB |
| `make build-arm64` | `dist/piper-arm64` — Linux aarch64（DGX Spark / Jetson） |
| `make build-host` | `dist/piper[.exe]` — 當前 OS/arch，開發便利 |
| `make test` / `test-race` / `lint` / `fmt` / `vet` | 顧名思義 |

## Sudoers 設定（UFW 必要步驟）

`ufw status` 需要 root 權限。piper 以一般使用者身份運行，因此需要 sudoers
白名單授予「免密碼」執行該指令的權限。沒設定的話，`piper serve` 啟動時會
印警告，Web UI 也會顯示 "UFW data unavailable" banner；`ss` 與 `docker`
資料仍可正常使用。

在目標主機**執行一次**（將 `ymu` 換成你的使用者名稱）：

```bash
sudo tee /etc/sudoers.d/piper-ufw <<EOF
ymu ALL=(root) NOPASSWD: /usr/sbin/ufw status, /usr/sbin/ufw status numbered
EOF
sudo chmod 0440 /etc/sudoers.d/piper-ufw
```

白名單是**唯讀的** — piper 仍然無法新增或刪除 UFW 規則。非 Debian 系統請
把 `/usr/sbin/ufw` 換成 `which ufw` 給出的實際路徑。

## 快速上手

```bash
# 把 binary 丟到目標主機
scp dist/piper user@h100:~/

# 設定免密碼 `sudo ufw status`（見上方「Sudoers 設定」）
# … 接著 …

# 開放 web port（一次性）
ssh user@h100 'sudo ufw allow 7878/tcp'

# 啟動 daemon（前景跑；systemd 部署看 design §10.4）
./piper serve --scan-interval 5m

# 在主機上的任何 shell：
piper check 8080
piper check 8080 9000-9005
piper suggest -n 3 --from 8000 --to 9999
piper reserve 9100 --name vllm-llama --note "下週部署"
piper history --port 9100 --days 7

# 從 LAN 上任何瀏覽器：
http://h100:7878/
```

## CLI 概覽

```
piper [全域 flags] <子指令> [子指令 flags]

子指令：
  check <ports...>     查詢一或多個 port（單一、清單、或 range 都接受）
  list                 依狀態過濾 port（--used / --free / --reserved / ...）
  scan                 等同於 `list --used --reserved`
  suggest              在指定 range 內建議閒置 port
  reserve <port>       新增顯性預留（--name、--note）
  release <port>       取消顯性預留
  history              查詢事件日誌
  serve                啟動 HTTP daemon + 排程掃描
  refresh              透過 daemon 立即觸發掃描
                       （daemon 未運行時降級為本地一次性掃描）

全域 flags：
  --data-dir PATH      資料目錄（預設 ~/.piper，可用 PIPER_DATA_DIR 覆寫）
  --format text|json   輸出格式（預設 text）
  --output FILE        tee 模式：輸出同時寫入 FILE
  --no-color           關閉 ANSI 顏色
```

所有子指令同時支援人類友善的 text（含顏色與樹狀繪製）與機器可讀的 JSON（包在 `{"ok":true,"data":...}` / `{"ok":false,"error":{...}}` envelope）。

### 範例：`piper check`

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
  └─ This port is technically free; restart container to reclaim.

Port 9000  ✅ AVAILABLE
  └─ No listener, no reservation

Scanned at 2026-04-25 14:32:18
```

## Web UI

`piper serve` 預設綁 `0.0.0.0:7878`。三個頁面：

- `/` — Overview：stateful port 表格、「Check ports」/「Suggest free」表單、「Refresh now」按鈕、「Last scan: X min ago」狀態。
- `/reservations` — 顯性預留列表 + 新增／刪除表單（write 按鈕只對來自 `127.0.0.1` 的 request 渲染）。
- `/history` — `occupied` / `released` / `reserved` / `unreserved` 事件時間軸。

採用 Pico.css + HTMX，無 build 流程，所有 asset 透過 `go:embed` 內嵌。離線可運作。

## 安全模型

LAN 唯讀、寫入限本機 — 周邊靠 UFW（或等價防火牆）：

| 來源 | 讀 | 寫 |
|------|----|----|
| 127.0.0.1 | ✅ | ✅ |
| LAN（UFW 放行） | ✅ | **❌**（HTTP 403） |
| 外網 | ❌（UFW 預設 deny） | ❌ |

寫入路由（`POST /api/reservations`、`DELETE /api/reservations/{port}`、`POST /api/scan/trigger`）由 `requireLocalhost` middleware 在 UFW 之上再次把關。Web UI 寫入按鈕在非 localhost request 時也不會渲染。

piper 全程不執行 `sudo`、不改動 UFW 規則、不啟停 container — 對系統是唯讀的。

## 架構

嚴格分層的依賴圖：

```
scanner ─┐
         ├─→ service ─→ cli
store   ─┘            ─→ server
```

- `internal/scanner/` — 純系統讀取（ss / docker / ufw）+ 薄薄的 orchestrator。Parser 是 `[]byte` 輸入的純函式，所以單元測試用 captured fixture 即可，不需要 live 系統。
- `internal/store/` — SQLite via `modernc.org/sqlite`。reservations、snapshots、history 的 CRUD，加上 `go:embed` migrations。
- `internal/service/` — 唯一的業務邏輯層。決策樹（`Checker`）、建議演算法（`Suggester`）、snapshot diff（`DiffSnapshots`）、scheduler。
- `internal/cli/` — cobra 子指令實作。
- `internal/server/` — `net/http` handler + HTMX/Pico template（`go:embed`）。

CLI 與 HTTP server 共用同一個 `service` 層，所以行為與 entry point 無關。完整架構說明見 [`CLAUDE.md`](./CLAUDE.md)。

## 資料佈局

```
~/.piper/
└── piper.db          # SQLite — reservations、scan_snapshots、scan_meta、history
```

`piper.db` mode `0600`，父目錄 `0700`。History 表中超過 30 天的紀錄，每天 `Asia/Taipei` 午夜自動清理。

## Phase 1 範圍與非目標

範圍內：即時查詢、container 全 port 展開、顯性 + 隱性預留、建議閒置 port、Web 唯讀面板、history。

範圍外（將來 phase 處理）：跨主機聯邦、系統變更（不執行 `ufw allow` / `docker rm`）、UFW + localhost 之外的認證、port range 分群、desktop GUI。

## 授權

待定。
