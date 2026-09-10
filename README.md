# 🚀 Google Automation Engine (bagasunix.com)

Search automation engine untuk menaikkan peringkat artikel website sendiri (`bagasunix.com`) di Google dan Bing melalui simulasi perilaku browsing manusia yang realistis dan sulit terdeteksi (*undetected*).

---

## 📋 Cara Kerja Sistem

```
1. Proxy Pool Management:
   ├─ Load proxy dari Webshare API (multi-key rotation, key comma-separated di .env)
   ├─ Health-check paralel → filter latency & bandwidth → pool aktif
   └─ Proxy health scoring: auto-quarantine 4 jam jika kena CAPTCHA, 2 jam jika network error.

2. Article & Keyword Ingestion:
   ├─ Scrape artikel target via sitemap.xml → simpan judul, meta description, topik ke SQLite
   ├─ Smart Priority Matrix: Artikel di Halaman 2 & 3 (posisi 11–30) otomatis dapat bobot pencarian lebih tinggi
   ├─ GSC Opportunity Optimizer (opsional): impression tinggi + CTR rendah di-boost (butuh CSV export dari GSC)
   └─ AI Semantic Query Expander: Generate variasi query alami (Groq/OpenAI LLM / Heuristic Indo Slang).

3. Task Execution Loop (Per Task Fresh Browser Session & Dynamic Cooldown):
   ├─ Traffic Source Mixer: bobot default 80% Google, 10% Bing, 5% Direct, 5% Social (relatif, tidak harus =100)
   ├─ Pre-search #1: Query topik/keyword umum → casual browse SERP → baca cuplikan
   ├─ Pre-search #2 (probabilistik): Query keyword kedua dari variasi AI
   ├─ Target Search: Ketik judul/meta/long-tail dengan simulasi ketukan jari manusia & koreksi typo (backspace)
   ├─ SERP Pagination: Telusuri hasil pencarian hingga Halaman 3 (posisi 1–30)
   ├─ Click Variation & Pogo-Sticking Engine (kunjungi kompetitor → Back → klik target)
   ├─ Post-Click Reading Engagement (initial scan, chunk scroll, reading heatmap, dwell proporsional konten)
   ├─ Multi-Tab & Internal Navigation (Ctrl+Click artikel internal, baca, tutup tab)
   ├─ Exit Strategy: mayoritas close browser, sebagian buka situs distraksi sebelum keluar
   └─ Multi-Tier CAPTCHA Solver: Jika Google memicu /sorry/ reCAPTCHA v2 → audio STT (Groq/OpenAI/Whisper) + token solver fallback.

4. Observability & Sync:
   ├─ Catat posisi SERP, dwell time, scroll depth, konsumsi bandwidth, dan status ke SQLite (WAL mode)
   ├─ Live Web Dashboard (:8080, login-protected) dengan Chart.js & Export CSV Report
   └─ Kirim ringkasan / terima perintah via Telegram Bot (/status, /stats, /pause, /resume).
```

---

## 🏗️ Arsitektur Sistem

Arsitektur **Hybrid Go + Python**: Go menangani kecepatan tinggi (proxy, dynamic scheduler, analytics, SQLite WAL, dashboard), Python menangani stealth browser automation (SeleniumBase UC, CDP injection, humanizer).

```
Go Orchestrator                          Python Worker (gRPC :50051)
├─ Proxy Manager (Multi-Key Webshare)    ├─ SeleniumBase UC (undetected-chromedriver)
├─ Residential Proxy Hub (adapter)       ├─ CDP Stealth (WebGL, Audio, WebRTC, Canvas)
├─ Article Queue & Priority Matrix       ├─ Search Flow (Google & Bing, SERP Hal 1-3)
├─ Dynamic Scheduler & Traffic Mixer     ├─ Direct & Social Referral Traffic Flows
├─ GSC Opportunity Optimizer             ├─ Pogo-Sticking Engine & Typo Humanizer
├─ Fleet Manager (concurrency scaling)   ├─ Engagement Simulation (Multi-Tab & Heatmap)
├─ Telegram Bot Controller               ├─ AI Semantic Query Expander (LLM)
├─ Live Web Dashboard (:8080, auth)      ├─ Multi-Tier CAPTCHA Solver (audio STT + token)
├─ SQLite Storage (WAL Mode, Pure Go)    ├─ Per-IP Warm Profiles & Fingerprint
└─ gRPC Client ──────────────────────→   └─ gRPC Server (:50051)
```

---

## 📁 Struktur Direktori

```
google-automation/
├── cmd/
│   ├── main.go                       # Entrypoint Go Orchestrator
│   ├── dashboard/main.go             # Live Web Dashboard Server (:8080)
│   └── test_proxy/main.go            # Utility uji koneksi proxy
├── config/
│   └── config.yaml                   # Konfigurasi utama (juga ada ./config.yaml di root sbg fallback)
├── internal/
│   ├── analytics/                    # SERP & stats aggregation untuk dashboard
│   ├── article/                      # Sitemap collector, extractor & queue priority matrix
│   ├── bandwidth/                    # Bandwidth tracking & quota conservation
│   ├── config/                       # YAML loader & .env override (applyEnvOverrides)
│   ├── grpc/                         # gRPC client + proto/ (task.proto & generated .pb.go)
│   ├── gsc/                          # Google Search Console opportunity importer
│   ├── notify/                       # Telegram notifier & interactive bot controller
│   ├── orchestrator/                 # Main loop coordinator + fleet.go (multi-worker)
│   ├── proxy/                        # Pool, health scoring, manager, residential, scraper
│   ├── scheduler/                    # Dynamic throttle, cooldown & traffic spread
│   └── storage/                      # SQLite queries, schema, migrations & WAL mode
├── worker/                           # Python Worker
│   ├── main.py                       # gRPC server worker & task dispatch
│   ├── paths.py                      # Path resolver (data/, profiles/, screenshots/)
│   ├── reporter.py                   # Result formatter & screenshot capturer
│   ├── browser/                      # session, stealth (CDP), profiles, humanizer, bandwidth, ip_health
│   ├── captcha/                      # audio, audio_sorry, solver, token_solver
│   ├── engagement/                   # click, dwell, exit simulation
│   ├── search/                       # google, bing, serp, query_expander
│   ├── generated/                    # gRPC stubs (task_pb2, task_pb2_grpc)
│   └── requirements.txt              # seleniumbase, grpcio, openai, SpeechRecognition, pydub, dll
├── scripts/
│   ├── run.sh                        # Launcher (start worker + orchestrator)
│   ├── stop.sh                       # Graceful stopper
│   ├── vps_setup.sh                  # Turnkey install untuk Ubuntu/Debian
│   ├── install_services.sh           # Systemd service installer
│   ├── watchdog.sh                   # Auto-heal watchdog
│   └── systemd/                      # Unit file templates (.service)
├── data/                             # SQLite DB (search_automation.db) & warm profiles
├── bin/                              # Binary hasil build (orchestrator, dashboard)
├── .env.example                      # Template kredensial
└── .env                              # Kredensial rahasia (git-ignored)
```

> Catatan: `worker/task_pb2.py` & `worker/task_pb2_grpc.py` juga di-generate oleh `run.sh` di root worker (selain salinan di `worker/generated/`).

---

## 🛠️ Setup & Instalasi

### 1. Prasyarat Lingkungan
- **Go**: 1.22+ (di system `go`, `/usr/local/go/bin`, atau `~/go-sdk/go/bin` — `run.sh` auto-detect)
- **Python**: 3.10+ (virtual environment di `worker/.venv`)
- **Google Chrome**: Chrome Stable (SeleniumBase UC pakai undetected-chromedriver)
- **ffmpeg**: dibutuhkan untuk backend CAPTCHA `whisper` & konversi audio (pydub)
- **OS**: Linux VPS (Ubuntu/Debian) atau WSL2 Ubuntu

### 2. Setup Otomatis di VPS Baru (Turnkey)
```bash
cd ~/Project/google-automation
bash scripts/vps_setup.sh
```

### 3. Setup Manual (Local / WSL2)
```bash
cd ~/Project/google-automation

# Python Worker venv
cd worker
python3 -m venv .venv
source .venv/bin/activate
pip install -r requirements.txt
python -m seleniumbase install chromedriver   # driver untuk UC mode (bukan playwright)
cd ..

# Go dependencies & build
export PATH="$HOME/go-sdk/go/bin:$PATH"   # atau /usr/local/go/bin
go mod tidy
go build -o bin/orchestrator cmd/main.go
go build -o bin/dashboard cmd/dashboard/main.go
```

> `run.sh` juga menjalankan setup venv + generate gRPC stub + rebuild orchestrator secara otomatis kalau belum ada.

---

## ⚙️ Konfigurasi

### 1. File `.env` (Kredensial Rahasia)
Salin template lalu isi kunci yang relevan:
```bash
cp .env.example .env
nano .env
```
Isi `.env` (semua kredensial HANYA di sini — `config.yaml` di-track git):
```bash
# Webshare Proxy API Keys (comma-separated untuk multi-key rotation)
WEBSHARE_API_KEYS=key_1,key_2

# Speech-to-Text / Audio CAPTCHA (OpenAI, Groq, dll yang OpenAI-compatible)
OPENAI_API_KEY=your_groq_or_openai_api_key

# Token Injection CAPTCHA Solver (Capsolver / 2Captcha)
TOKEN_SOLVER_KEY=your_capsolver_key

# Telegram Notifications (opsional)
TELEGRAM_BOT_TOKEN=
TELEGRAM_CHAT_ID=

# Dashboard login — WAJIB diisi, dashboard menolak jalan tanpa ini
DASHBOARD_USERNAME=admin
DASHBOARD_PASSWORD=change_me
```

> `.env` selalu meng-override nilai di `config.yaml` (lihat `applyEnvOverrides` di `internal/config/config.go`). Karena itu key API di `config.yaml` sengaja dibiarkan kosong.

### 2. File `config/config.yaml`
```yaml
auth:
  enabled: true                       # dashboard butuh login (kredensial dari .env)

domains:
  - bagasunix.com

engine_ratio:                          # bobot relatif, tidak harus berjumlah 100
  google: 80
  bing: 10
  direct: 5
  social: 5

scheduler:
  concurrency: 4                       # jumlah worker/Chrome paralel (Go & Python baca ini)
  max_search_per_proxy: 5              # maksimal pencarian per proxy per hari
  new_article_boost: 5                 # artikel baru (<7 hari): kuota lebih tinggi
  regular_max: 3                       # artikel biasa: kuota max
  captcha_pause_hours: 3               # auto-pause per engine saat CAPTCHA spike
  min_cooldown_seconds: 5              # jeda antar task (nilai file ini = mode testing)
  max_cooldown_seconds: 15
  post_exit_cooldown_min: 5
  post_exit_cooldown_max: 15
  active_hours_start: 0                # 0/24 = semua jam aktif (mode testing)
  active_hours_end: 24
  pre_search_enabled: true
  pre_search_2_chance: 0.25
  serp_casual_click_chance: 0.15
  competitor_click_chance: 0.70        # jalur standar: kunjungi situs lain → balik → klik target
  distraction_exit_chance: 0.05
  serp_dwell_seconds_min: 2
  serp_dwell_seconds_max: 5
  max_searches_per_domain_per_day: 0   # 0 = unlimited

proxy:
  refresh_interval_hours: 3
  health_check_timeout: 8
  webshare_api_key: ""                 # kosongkan — pakai .env
  webshare_api_keys: []                # kosongkan — pakai .env (WEBSHARE_API_KEYS)
  sources: []                          # daftar sumber proxy publik (default disabled)

grpc:
  port: 50051
  worker_timeout: 600

captcha:
  enabled: true
  max_attempts: 3
  solver: "openai_api"                 # "openai_api" (Groq/OpenAI) | "google_web" | "whisper" (lokal)
  whisper_model: "base"                # "base" (~70MB) | "small" (~140MB)
  openai_api_key: ""                   # kosongkan — pakai OPENAI_API_KEY di .env
  openai_base_url: "https://api.groq.com/openai/v1"   # kosongkan untuk OpenAI default
  openai_model: "whisper-large-v3-turbo"
  prompt: "unrelated short English words"
  token_solver: "capsolver"            # "capsolver" | "2captcha"
  token_solver_key: ""                 # kosongkan — pakai TOKEN_SOLVER_KEY di .env

bandwidth:
  monthly_limit_mb: 1024               # Webshare free = 1GB per key/bulan
  block_images: false                  # target: gambar tetap | non-target: diblok
  block_media: true
  block_fonts: true
  block_stylesheets: false
  warn_threshold_percent: 80
  pause_threshold_percent: 95

article_collection:
  method: sitemap
  refresh_interval_hours: 6
  max_concurrent_fetches: 4

telegram:
  enabled: false
  bot_token: ""
  chat_id: ""

gsc:
  csv_path: ""                         # path CSV export dari Google Search Console (kosong = fitur off)
  weight_multiplier: 10                # kekuatan boost halaman opportunity
```

> Nilai cooldown & active_hours di file saat ini adalah **mode testing** (jeda pendek, semua jam aktif). Untuk produksi, naikkan cooldown (mis. 30–120s) dan set jam aktif (mis. 7–23).

---

## 🚦 Cara Menjalankan

### A. Menjalankan Otomatis (CLI Mode)
```bash
./scripts/run.sh      # start Python worker + Go orchestrator
./scripts/stop.sh     # stop keduanya
```

### B. Menjalankan Live Web Control Panel
```bash
./bin/dashboard --serve :8080
# flag lain: --db <path SQLite>  --out <path HTML statis>
```
Buka `http://<IP_VPS>:8080` lalu login dengan `DASHBOARD_USERNAME`/`DASHBOARD_PASSWORD`. Fitur panel:
* **🤖 Bot Fleet Grid & Live Terminal**: pantau worker paralel live, atur concurrency, streaming log per worker.
* **📊 Analytics & Trends**: grafik Chart.js pergerakan ranking + **Export CSV Report**.
* **🌐 Articles & SERP**: posisi ranking tiap artikel + tombol **⚡ Cari Sekarang** & **🔄 Sync Sitemap**.
* **🛡️ Proxy Hub**: status alokasi proxy (in-use/idle/quarantined), latensi, **Test All Proxies**.
* **⚙️ Settings Editor**: ubah `config.yaml` & `.env` dari browser (live hot-reload).

> Dashboard **menolak jalan** kalau `DASHBOARD_USERNAME`/`DASHBOARD_PASSWORD` belum di-set (fail-fast, biar tidak terekspos tanpa auth).

### C. Menjalankan 24/7 via Systemd di VPS
```bash
sudo bash scripts/install_services.sh
sudo systemctl start google-automation
sudo systemctl start google-dashboard
sudo journalctl -u google-automation -f
```

---

## 🛡️ Rincian Fitur Anti-Deteksi

### 1. Browser Fingerprint
- **CDP Stealth Injection**: script di `Page.addScriptToEvaluateOnNewDocument` mem-patch `navigator.webdriver=false`, `navigator.plugins`, `navigator.languages`, `window.chrome`, permissions, dan connection.
- **Per-IP Deterministic Fingerprint**: seluruh fingerprint (User-Agent, viewport, WebGL GPU, canvas seed, hardware) di-seed dari `crc32(proxy_ip)`. **IP yang sama selalu menghasilkan fingerprint yang sama** (konsisten seperti orang yang sama kembali); **IP baru = fingerprint baru total** (browser baru). Konsisten dengan warm-profile per-IP.
- **Canvas Noise**: mikro-noise pada `toDataURL`/`getImageData` — dihitung dari copy, canvas asli tak pernah dimutasi (stabil, tidak berosilasi).
- **WebGL Mocking**: `WEBGL_debug_renderer_info` + fallback mock untuk server headless tanpa GPU/X11; vendor/renderer dipilih konsisten dengan platform UA.
- **WebRTC IP Shield**: cegah bocor IP asli via SDP candidate sanitization + `disable_non_proxied_udp`.
- **AudioContext Spoofing**: mikro-noise pada `AudioBuffer` & `AnalyserNode`.
- **Warm Profiles**: cookies/cache/history disimpan di `data/profiles/profile_0..profile_49` (pool 50 slot), dipetakan per proxy IP via `crc32` yang stabil lintas run.
- **Mobile Emulation**: rotasi UA smartphone (Android & iPhone), mobile viewport, CDP touch emulation.

### 2. Search Behavior
- **Pre-Search & Browsing Santai**, **Typo Humanizer** (jeda per karakter + backspace), **SERP Snippet Reading** (dwell konfigurable), **Pogo-Sticking Engine**, **Bezier Mouse Movements**, **Consent Banner Dismissal**.

### 3. Post-Click Engagement
- **Initial Scan**, **Smooth Chunk Scrolling**, **Element Pausing** (H2/H3, code block, gambar), **Reading Heatmap** (seleksi teks + re-read), **Multi-Tab Browsing** (Ctrl+Click), **Exit Variety**.

### 4. Proxy Reliability & Dynamic Throttling
- **Multi-Key Failover** (Webshare, comma-separated di `.env`), **Auto-Quarantine** (4 jam CAPTCHA / 2 jam network error), **Time-of-Day Awareness** (jam aktif per timezone proxy), **Per-Engine Auto Fallback** (Google → Bing/Direct/Social saat CAPTCHA).

### 5. Deep SEO & Algorithmic Boosters
- **Google Autocomplete Hijacker**, **People Also Ask (PAA) Explorer**, **UX Engagement & Social Share Simulator**.

---

## 🗄️ Database Schema (SQLite WAL Mode)

DB pure-Go di `search_automation.db`:
- **`proxies`**: `ip`, `port`, `protocol`, `country`, `timezone`, `username`, `password`, `api_key_index`, `active`, `latency_ms`, `used_count`, `last_used_at`, `blacklisted`, `blacklist_reason`.
- **`articles`**: `domain`, `url`, `title`, `meta_desc`, `topic`, `searched_count`, `last_searched_at`, `first_searched_at`, `serp_position`, `opportunity_score`.
- **`tasks`**: `article_id`, `proxy_id`, `engine`, `status`, `result_json`, `error`, `created_at`, `completed_at`.
- **`daily_stats`**: `date`, `total_search`, `success`, `fail`, `captcha`, `avg_dwell_seconds`, `avg_serp_position`.

---

## 📸 Screenshots Otomatis

Worker menyimpan screenshot saat kondisi penting/error ke `screenshots/{task_id}_{error_type}_{timestamp}.png`:
CAPTCHA terdeteksi, target tak ditemukan di SERP, salah landing page, dan exception tak terduga.

---

## 🔌 gRPC Protocol Definition

Komunikasi Go ⇄ Python via gRPC port `50051` (`internal/grpc/proto/task.proto`):

```protobuf
syntax = "proto3";
package searchautomation;
option go_package = "google-automation/internal/grpc/proto";

service WorkerService {
    rpc ExecuteTask (TaskRequest) returns (TaskResponse);
}

message TaskRequest {
    string task_id = 1;
    string article_title = 2;
    string article_url = 3;
    string domain = 4;
    string proxy_ip = 5;
    int32 proxy_port = 6;
    string engine = 7;                  // "google" | "bing" | "direct" | "social"
    repeated string pre_search_queries = 8;
    string proxy_username = 9;
    string proxy_password = 10;
    string proxy_country = 11;
    string proxy_timezone = 12;
    bool pre_search_enabled = 13;
    double pre_search_2_chance = 14;
    double serp_casual_click_chance = 15;
    double competitor_click_chance = 16;
    double distraction_exit_chance = 17;
    int32 serp_dwell_seconds_min = 18;
    int32 serp_dwell_seconds_max = 19;
}

message TaskResponse {
    string task_id = 1;
    bool success = 2;
    string engine = 3;
    string proxy_used = 4;
    int32 serp_position = 5;            // 0 = not found, 1-30 = found
    int32 dwell_time_seconds = 6;
    int32 scroll_depth_percent = 7;
    int32 internal_clicks = 8;
    bool captcha_hit = 9;
    string error = 10;
    int32 bandwidth_used_kb = 11;
}
```

> `engine` google/bing menjalankan search flow penuh; direct/social menjalankan traffic-flow tanpa search engine (langsung ke domain / via referrer sosial).

---

## 📱 Perintah Telegram Bot

Aktif jika `telegram.enabled: true` dan `TELEGRAM_BOT_TOKEN`/`TELEGRAM_CHAT_ID` di-set:
* `/status` — status orchestrator, worker, pool proxy, sisa cooldown.
* `/stats` — analitik hari ini (total, sukses, CAPTCHA, rata-rata dwell).
* `/pause` — pause jadwal pencarian.
* `/resume` — lanjutkan jadwal.
* `/start`, `/help` — bantuan daftar perintah.

---

## 📊 Format Ekspor Laporan (CSV)

Endpoint `/api/export/csv` di dashboard:
```csv
Date,TotalSearches,Success,Fail,CAPTCHA,SuccessRatePercent,AvgDwellSeconds,AvgSerpPosition
2026-08-29,24,23,1,0,95.83,112.40,3.50
```

---

## 💡 Tips Penggunaan di VPS
1. **Groq API Key gratis**: set `OPENAI_API_KEY=gsk_...` di `.env` + biarkan `openai_base_url` ke Groq untuk transkripsi audio CAPTCHA cepat dan variasi query AI.
2. **Dashboard auth**: wajib set `DASHBOARD_USERNAME`/`DASHBOARD_PASSWORD` di `.env` sebelum `--serve`.
3. **Produksi vs testing**: naikkan cooldown & set jam aktif di `config.yaml` (nilai sekarang mode testing).
4. **Auto-Start**: pakai `scripts/install_services.sh` untuk systemd 24/7.
