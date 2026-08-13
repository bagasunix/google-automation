# Google Automation

Search automation engine untuk nge-push ranking web sendiri di Google/Bing dengan simulasi perilaku manusia.

## Cara Kerja

```
1. Scrape free proxy → health check → pool aktif
2. Scrape artikel web sendiri (sitemap.xml) → ambil judul + meta desc
3. Setiap loop (ganti proxy setiap kali):
   ├─ Pilih artikel random + random search method (judul/meta desc/keyword)
   ├─ Pre-search: query topik related (bukan judul artikel)
   ├─ Target search: ketik judul/meta artikel ke Google/Bing
   ├─ Cari domain sendiri di SERP → klik (dengan delay baca snippet)
   ├─ Baca artikel: scroll chunk, pause di H2/H3, pause di code block
   ├─ 50% chance klik internal link → baca 20-40s → balik
   ├─ Exit: close/back (varied)
   └─ Cooldown 30-120s sebelum next task
4. Update analytics: SERP position, dwell time, success rate
```

## Arsitektur

Hybrid Go + Python. Go handle speed (proxy, scheduling, analytics). Python handle anti-detection (playwright stealth, humanized browser).

```
Go Orchestrator (port: dynamic)     Python Worker (localhost:50051)
├─ Proxy Manager                     ├─ Stealth Browser (canvas/WebGL/tz spoof)
├─ Article Collector (sitemap.xml)   ├─ Humanized Search (typing, SERP browse)
├─ Scheduler (dynamic throttle)      ├─ Post-Click Engagement (reading sim)
├─ Analytics + SERP tracking         ├─ CAPTCHA detector
├─ SQLite storage                    └─ Screenshot on error
└─ gRPC client ──────────────────→ gRPC server
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
│   ├── scheduler/                       Dynamic throttle + time-of-day + cooldown
│   ├── analytics/                       Stats + SERP position tracking
│   ├── storage/                         SQLite (pure Go, no CGO)
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

scheduler:
  max_search_per_proxy: 1            # 1 proxy = 1 search (STRICT)
  new_article_boost: 5               # artikel baru: 5x search minggu 1
  regular_max: 3                     # artikel lama: max 3x search
  captcha_pause_hours: 3             # auto pause kalau CAPTCHA naik
  min_cooldown_seconds: 30           # jeda antar task
  max_cooldown_seconds: 120
  post_exit_cooldown_min: 30         # jeda setelah exit artikel
  post_exit_cooldown_max: 120
  active_hours_start: 7              # jam aktif (7am waktu proxy)
  active_hours_end: 23               # (11pm waktu proxy)

proxy:
  refresh_interval_hours: 3          # scrape proxy baru tiap 3 jam
  health_check_timeout: 5            # timeout per proxy (detik)

grpc:
  port: 50051
  worker_timeout: 300                # timeout per task (detik)
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
- User-Agent + viewport rotation per session
- Cookie jar fresh per session

### Search Behavior
- Pre-search dengan query topik (bukan judul langsung)
- Typing 80-200ms per karakter (random delay)
- SERP dwell 5-15s sebelum klik (baca snippet)
- Click variation:
  - 50% klik target langsung (delay 3-8s)
  - 30% scroll lewat target, baca lain, balik, klik
  - 20% klik competitor dulu, baca 10-20s, back, klik target
- Mouse movement: bezier curve (bukan linear)

### Post-Click Engagement
- Initial pause 5-15s (scan halaman)
- Scroll chunk 200-500px (bukan jump)
- Pause di H2/H3 heading (3-8s, baca section)
- Pause di code block / image (5-12s, pelajari)
- Kadang scroll balik atas (re-read)
- Micro-pause 1-3s mid-paragraf (berpikir)
- Total baca 60-300s (sesuai panjang artikel)
- 50% chance klik internal link (baca 20-40s, balik)
- Exit varied: close tab / back to Google
- Cooldown 30-120s sebelum next task

### Proxy Management
- 1 proxy = 1 search per cycle (STRICT rotation)
- Free proxy dari 4+ sources
- Health check paralel (Go concurrency)
- Auto-replace dead proxy
- Blacklist permanent kalau trigger CAPTCHA
- Time-of-day awareness: cuma search 7am-11pm waktu proxy lokal

### Search Randomization
Gak selalu search pakai judul. Random:
- (a) Exact article title
- (b) Meta description (full/partial)
- (c) Title + partial meta desc
- (d) Keyword dari title
- (e) Keyword dari meta desc

## Database

SQLite (file-based, no server). Auto-created di `data/search_automation.db`.

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
              proxy_ip, proxy_port, engine, pre_search_queries[]
TaskResponse: task_id, success, engine, serp_position,
              dwell_time, scroll_depth, internal_clicks,
              captcha_hit, error
```

## Tech Stack

| Component | Technology |
|---|---|
| Orchestrator | Go 1.25 |
| Browser automation | Python + Playwright |
| Anti-detection | playwright-stealth |
| IPC | gRPC (protobuf) |
| Database | SQLite (modernc.org/sqlite, pure Go) |
| Config | YAML |
| Proxy | Free proxy (scraped from 4+ sources) |

## Dependencies

### Go
- google.golang.org/grpc
- google.golang.org/protobuf
- modernc.org/sqlite (pure Go, no CGO)
- gopkg.in/yaml.v3

### Python
- playwright >= 1.40.0
- playwright-stealth >= 1.0.6
- grpcio >= 1.60.0
- grpcio-tools >= 1.60.0
- beautifulsoup4 >= 4.12.0
- lxml >= 5.1.0

## Catatan

- Free proxy success rate ~10-20%. Sistem didesign untuk handle failure tinggi.
- Google lebih ketat dari Bing. Engine ratio default 70/30.
- Artikel baru otomatis dapat boost (5x search di minggu pertama).
- Kalau CAPTCHA rate > 10% di satu hari, scheduler auto pause 2-4 jam.
