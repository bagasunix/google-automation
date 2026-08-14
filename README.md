# Google Automation

Search automation engine untuk nge-push ranking web sendiri di Google/Bing dengan simulasi perilaku manusia.

## Cara Kerja

```
1. Scrape proxy dari Webshare API (multi-key rotation) → health check → pool aktif
2. Scrape artikel web sendiri (sitemap.xml) → ambil judul + meta desc
3. Setiap loop (ganti proxy setiap kali, browser fresh/clean):
   ├─ Pilih engine (Google 70% / Bing 30%) — kalau Google kena CAPTCHA, otomatis Bing
   ├─ Pre-search #1: query "BagasUnix" → casual browse SERP → klik result lain → baca 20-60s → back
   ├─ Pre-search #2 (50% skip): query keyword random dari artikel
   ├─ Target search: ketik judul/meta artikel ke engine yang dipilih
   ├─ Cari domain sendiri di SERP (page 1 + page 2) → klik dengan variasi behavior
   ├─ Baca artikel: scroll chunk, pause di H2/H3, pause di code/image, total 60-300s
   ├─ Klik 1-2 internal link → baca full (simulate_reading) → balik
   ├─ Exit: close/back (varied)
   └─ Cooldown sebelum next task
4. Update analytics: SERP position, dwell time, success rate
```

## Arsitektur

Hybrid Go + Python. Go handle speed (proxy, scheduling, analytics). Python handle anti-detection (playwright stealth, humanized browser).

```
Go Orchestrator (port: dynamic)     Python Worker (localhost:50051)
├─ Proxy Manager (multi-key)         ├─ Stealth Browser (canvas/WebGL/tz spoof)
├─ Article Collector (sitemap.xml)   ├─ Humanized Search (typing, SERP browse)
├─ Scheduler (per-engine pause)      ├─ Post-Click Engagement (reading sim)
├─ Analytics + SERP tracking         ├─ Internal click (1-2 articles, full read)
├─ SQLite storage (WAL mode)         ├─ CAPTCHA detector
└─ gRPC client ──────────────────→ gRPC server (port 50051)
```

## Struktur Project

```
google-automation/
├── cmd/main.go                          Go entry point
├── config/config.yaml                   Config (domain, engine ratio, settings)
├── internal/
│   ├── config/config.go                 YAML config loader
│   ├── proxy/                           Proxy scrape + health + rotation
│   ├── article/                         Sitemap scraper + title/meta extractor
│   ├── scheduler/                       Dynamic throttle + per-engine pause + cooldown
│   ├── analytics/                       Stats + SERP position tracking
│   ├── storage/                         SQLite (pure Go, no CGO, WAL+busy_timeout)
│   ├── grpc/                            gRPC client + proto
│   └── orchestrator/                    Main loop coordinator
├── worker/                              Python worker
│   ├── main.py                          gRPC server
│   ├── browser/                         Stealth + session + humanizer
│   ├── search/                          Google + Bing + SERP parser
│   ├── engagement/                      Dwell + click + exit simulation
│   ├── reporter.py                      JSON result + screenshot
│   └── requirements.txt                 playwright, grpcio, bs4, lxml
├── scripts/
│   ├── run.sh                           Start Python + Go
│   └── stop.sh                          Stop all
└── data/                                SQLite DB (auto-created)
```

## Setup

### Prasyarat

- Go 1.25+ (di ~/go-sdk/go/bin)
- Python 3.12+ (di system)
- WSL2 Ubuntu

### Install

```bash
cd ~/Project/google-automation

# Setup Python worker (first time only)
cd worker
python3 -m venv .venv
source .venv/bin/activate
pip install -r requirements.txt
playwright install chromium
cd ..

# Setup Go (auto-download deps)
export PATH="$HOME/go-sdk/go/bin:$PATH"
go mod tidy
```

### Konfigurasi

Edit `config/config.yaml`:

```yaml
domains:
  - bagasunix.com                    # domain web lo

engine_ratio:
  google: 70                         # 70% search di Google
  bing: 30                           # 30% search di Bing
  # Kalau Google kena CAPTCHA → otomatis fallback ke Bing
  # Kalau semua proxy Google kena CAPTCHA → Google di-pause, Bing tetap jalan

scheduler:
  max_search_per_proxy: 5            # max 5 search per proxy per hari
  new_article_boost: 5               # artikel baru: 5x search minggu 1
  regular_max: 3                     # artikel lama: max 3x search
  captcha_pause_hours: 3             # auto pause per-engine kalau CAPTCHA naik
  min_cooldown_seconds: 30           # jeda antar task
  max_cooldown_seconds: 120
  post_exit_cooldown_min: 30         # jeda setelah exit artikel
  post_exit_cooldown_max: 120
  active_hours_start: 7              # jam aktif (7am waktu proxy)
  active_hours_end: 23               # (11pm waktu proxy)

proxy:
  refresh_interval_hours: 3
  health_check_timeout: 8
  # Multi-key rotation: key #0 dipakai dulu sampai habis/CAPTCHA, baru pindah key #1
  webshare_api_keys:
    - "YOUR_API_KEY_0"               # key #0 (primary)
    - "YOUR_API_KEY_1"               # key #1 (uncomment kalau punya)

grpc:
  port: 50051
  worker_timeout: 600                # timeout per task (detik) — cukup untuk full reading
```

## Menjalankan

```bash
# Start (Python worker + Go orchestrator)
./scripts/run.sh

# Stop
./scripts/stop.sh
```

Atau manual:

```bash
# Terminal 1: Python worker
cd worker && source .venv/bin/activate && python main.py

# Terminal 2: Go orchestrator
export PATH="$HOME/go-sdk/go/bin:$PATH"
go run cmd/main.go
```

## Fitur Anti-Deteksi

### Browser Fingerprint
- Canvas fingerprint dispoof
- WebGL renderer dirandomize
- Timezone + language sesuai proxy geo
- WebRTC dimatikan (anti IP leak)
- User-Agent rotation per session: 18 agents (Chrome/Edge/Firefox/Safari, Windows/Mac/Linux)
- Cookie jar + localStorage fresh per session (clear_cookies() setiap ganti proxy)
- Viewport randomized (1280×720 sampai 1920×1080)

### Search Behavior
- Pre-search dengan query topik (bukan judul langsung)
- Typing 80-200ms per karakter (random delay)
- SERP casual browsing: scroll, klik result lain, baca 20-60s, back
- SERP dwell 5-15s sebelum klik (baca snippet)
- Click variation:
  - 50% klik target langsung (delay 3-8s)
  - 30% scroll lewat target, baca lain, balik, klik
  - 20% klik competitor dulu, baca 10-20s, back, klik target
- Mouse movement: bezier curve (bukan linear), scroll ke viewport dulu sebelum klik
- Consent banner dismiss: humanized click (bukan `.click()` langsung)

### Post-Click Engagement
- Initial pause 5-15s (scan halaman)
- Scroll chunk 200-500px (bukan jump)
- Pause di H2/H3 heading (3-8s, baca section)
- Pause di code block / image (5-12s, pelajari)
- Kadang scroll balik atas (re-read)
- Micro-pause 1-3s mid-paragraf (berpikir)
- Total baca 60-300s (sesuai panjang artikel)
- **Internal clicks: selalu klik 1-2 artikel lain di site, baca full (simulate_reading), balik**
- Exit varied: close tab / back to SERP
- Cooldown 30-120s sebelum next task

### Proxy Management
- **Multi-key Webshare rotation**: key #0 dipakai dulu, pindah ke key #1 kalau semua proxy key #0 blacklisted
- Pool sorted by API key index — key #0 selalu di depan
- max_search_per_proxy: 5 (1 proxy bisa reused 5x per hari)
- Health check paralel (Go concurrency, 100 goroutines)
- Auto-replace dead proxy setiap 3 jam
- Blacklist permanent kalau trigger CAPTCHA
- Time-of-day awareness: cuma search 7am-11pm waktu proxy lokal

### Engine Fallback
- **Per-engine CAPTCHA pause**: Google kena CAPTCHA → Google di-pause 3 jam, Bing tetap jalan
- **Auto-fallback**: `PickEngineAvailable()` skip engine yang sedang di-pause
- **Per-engine rate check**: `CheckCaptchaRate()` hitung CAPTCHA rate per engine, bukan global
- Kalau semua engine di-pause → orchestrator skip cycle, tunggu pause expire

### Search Randomization
Gak selalu search pakai judul. Random:
- (a) Exact article title
- (b) Meta description (full/partial)
- (c) Title + partial meta desc
- (d) Keyword dari title
- (e) Keyword dari meta desc

## Database

SQLite (file-based, no server). Auto-created di `search_automation.db`. WAL mode + busy_timeout 5s untuk concurrency.

```
proxies        ip, port, country, timezone, active, latency, used_count, blacklisted
articles       url, title, meta_desc, topic, searched_count, serp_position
tasks          article_id, proxy_id, engine, status, result_json
daily_stats    date, total_search, success, fail, captcha, avg_dwell
```

## Screenshots

Python worker simpen screenshot otomatis pas error:
- CAPTCHA ketahuan Google
- Artikel gak ketemu di SERP
- Proxy timeout / page load error
- Click target gagal

Lokasi: `screenshots/{task_id}_{timestamp}.png`

## gRPC Protocol

Go ↔ Python communicate via gRPC on localhost:50051.

```protobuf
service WorkerService {
    rpc ExecuteTask (TaskRequest) returns (TaskResponse);
}

TaskRequest:  task_id, article_title, article_url, domain,
              proxy_ip, proxy_port, engine, pre_search_queries[],
              proxy_username, proxy_password
TaskResponse: task_id, success, engine, serp_position,
              dwell_time, scroll_depth, internal_clicks,
              captcha_hit, error
```

## Tech Stack

| Component | Technology |
|---|---|
| Orchestrator | Go 1.25 |
| Browser automation | Python + Playwright |
| Anti-detection | Custom stealth scripts (canvas/WebGL/tz/UA) |
| IPC | gRPC (protobuf) |
| Database | SQLite (modernc.org/sqlite, pure Go, WAL) |
| Config | YAML |
| Proxy | Webshare API (multi-key rotation) |

## Dependencies

### Go
- google.golang.org/grpc
- google.golang.org/protobuf
- modernc.org/sqlite (pure Go, no CGO)
- gopkg.in/yaml.v3

### Python
- playwright >= 1.40.0
- grpcio >= 1.60.0
- grpcio-tools >= 1.60.0
- beautifulsoup4 >= 4.12.0
- lxml >= 5.1.0

## Catatan

- Webshare **datacenter** proxy akan selalu kena CAPTCHA di Google. Butuh **residential** proxy biar Google bisa jalan.
- Bing berjalan normal dengan datacenter proxy — gunakan Bing dulu sambil nunggu residential proxy.
- Google engine otomatis di-pause kalau kena CAPTCHA, Bing tetap jalan tanpa perlu ganti config.
- Artikel baru otomatis dapat boost (5x search di minggu pertama).
- CAPTCHA rate > 10% per engine → engine tersebut di-pause 3 jam (engine lain tidak terpengaruh).
- Tambah API key Webshare ke `webshare_api_keys` list untuk lebih banyak proxy.
