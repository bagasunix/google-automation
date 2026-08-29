# 🚀 Google Automation Engine (bagasunix.com)

Search automation engine enterprise-grade untuk menaikkan peringkat artikel website sendiri (`bagasunix.com`) di Google dan Bing melalui simulasi perilaku browsing manusia yang 100% realistis dan tidak terdeteksi (*undetected*).

---

## 📋 Cara Kerja Sistem

```
1. Proxy Pool Management:
   ├─ Scrape & load proxy dari Webshare API (multi-key rotation) / Residential Proxy / Custom File
   ├─ Health-check paralel (100 goroutines) → filter latency & bandwidth → pool aktif
   └─ Proxy health scoring: auto-quarantine 4 jam jika kena CAPTCHA, 2 jam jika network error.

2. Article & Keyword Ingestion:
   ├─ Scrape artikel target via sitemap.xml → simpan judul, meta description, topik ke SQLite
   ├─ Smart Priority Matrix: Artikel di Halaman 2 & 3 (posisi 11–30) otomatis dapat bobot pencarian 3x lebih tinggi
   └─ AI Semantic Query Expander: Generate variasi query alami (Groq Llama-3 / Heuristic Indo Slang).

3. Task Execution Loop (Per Task Fresh Browser Session & Dynamic Cooldown):
   ├─ Traffic Source Mixer: 70% Google, 10% Bing, 10% Direct Bookmark/Homepage, 10% Social Referral
   ├─ Pre-search #1: Query topik/keyword umum → casual browse SERP → baca cuplikan 20-60s
   ├─ Pre-search #2 (50% random chance): Query keyword kedua dari variasi AI
   ├─ Target Search: Ketik judul/meta/long-tail dengan simulasi ketukan jari manusia & koreksi typo (backspace)
   ├─ SERP Pagination: Telusuri hasil pencarian hingga Halaman 3 (posisi 1–30)
   ├─ Click Variation & Pogo-Sticking Engine:
   │  ├─ 50% Klik target langsung (setelah jeda membaca snippet 3-8s)
   │  ├─ 25% Scroll melewati target, baca hasil lain, scroll balik, lalu klik target
   │  └─ 25% Pogo-Sticking: Klik kompetitor #1/#2, skim 4–7s, tekan tombol Back (Bounce), lalu klik target kita
   ├─ Post-Click Reading Engagement:
   │  ├─ Initial scan: Diam 5–12s membaca layar pertama
   │  ├─ Scroll chunk: 200–500px, jeda lama pada H2/H3, code block, dan gambar
   │  ├─ Reading Heatmap: Seleksi/highlight teks penting, micro-pause 1–3s, scroll balik atas (re-read)
   │  └─ Total waktu membaca artikel: 60–300s (proporsional dengan panjang konten)
   ├─ Multi-Tab & Internal Navigation:
   │  ├─ Klik 1-2 artikel internal di domain sendiri
   │  ├─ 40% Buka artikel di Tab Baru (Ctrl+Click), baca 10–20s, tutup tab, kembali ke artikel utama
   │  └─ 60% Buka di tab yang sama, baca penuh, lalu navigasi kembali
   ├─ Exit Strategy: 70% Close browser, 30% Buka situs distraksi (Wikipedia/News) sebelum keluar
   └─ Multi-Tier CAPTCHA Solver: Jika Google memicu /sorry/ reCAPTCHA v2 → pecahkan via Groq Whisper / Google Web Speech.

4. Observability & Sync:
   ├─ Catat posisi SERP, dwell time, scroll depth, konsumsi bandwidth, dan status ke SQLite (WAL mode)
   ├─ Perbarui Live Web Dashboard (:8080) dengan visualisasi Chart.js & fitur Export CSV Report
   └─ Kirim ringkasan atau terima perintah via Telegram Bot (/status, /stats, /pause, /resume).
```

---

## 🏗️ Arsitektur Sistem

Arsitektur **Hybrid Go + Python** membagi beban kerja secara optimal: Go menangani kecepatan tinggi (proxy, dynamic scheduler, analytics, SQLite WAL), sedangkan Python menangani stealth browser automation (SeleniumBase UC, CDP injection, humanizer).

```
Go Orchestrator (Port Dinamis)          Python Worker (Port 50051)
├─ Proxy Manager (Multi-Key Webshare)   ├─ SeleniumBase UC (Undetected-ChromeDriver)
├─ Residential Proxy Hub (Smartproxy)   ├─ CDP Stealth (WebGL, Audio, WebRTC, Canvas)
├─ Article Queue & Priority Matrix      ├─ Search Flow (Google & Bing, SERP Hal 1-3)
├─ Dynamic Scheduler & Traffic Mixer    ├─ Pogo-Sticking Engine & Typo Humanizer
├─ GSC Opportunity Optimizer            ├─ Engagement Simulation (Multi-Tab & Heatmap)
├─ Telegram Bot Controller              ├─ AI Semantic Query Expander (Groq LLM)
├─ Live Web Dashboard (:8080)           ├─ Multi-Tier CAPTCHA Solver (Whisper/Web)
├─ SQLite Storage (WAL Mode, Pure Go)   ├─ Warm Profiles Manager (profile_0..9)
└─ gRPC Client ─────────────────────→   └─ gRPC Server (:50051)
```

---

## 📁 Struktur Direktori

```
google-automation/
├── cmd/
│   ├── main.go                       # Entrypoint Go Orchestrator
│   └── dashboard/main.go             # Live Web Dashboard Server (:8080)
├── config/
│   ├── config.yaml                   # Konfigurasi utama engine, scheduler & proxy
│   └── config.yaml.example           # Template konfigurasi bersih
├── internal/
│   ├── article/                      # Sitemap scraper, queue & keyword priority matrix
│   ├── bandwidth/                    # Bandwidth tracking & quota conservation
│   ├── config/                       # YAML loader & .env integration
│   ├── grpc/                         # gRPC client & protobuf generated files
│   ├── gsc/                          # Google Search Console opportunity optimizer
│   ├── notify/                       # Telegram notifier & interactive bot controller
│   ├── orchestrator/                 # Main loop task coordinator
│   ├── proxy/                        # Proxy pool, health scoring & residential hub
│   ├── scheduler/                    # Dynamic throttle, cooldowns & traffic mixer
│   └── storage/                      # SQLite queries, schema migrations & WAL mode
├── worker/                           # Python Worker
│   ├── main.py                       # gRPC server worker
│   ├── browser/                      # Stealth CDP, session, warm profiles, humanizer
│   ├── captcha/                      # Audio solver, STT fallback & token handler
│   ├── engagement/                   # Reading dwell, multi-tab click, exit simulation
│   ├── search/                       # Google/Bing SERP flows, AI query expander
│   ├── reporter.py                   # Result JSON formatter & screenshot capturer
│   └── requirements.txt              # Dependencies: seleniumbase, grpcio, openai, etc.
├── scripts/
│   ├── run.sh                        # Universal launcher (WSL/Local/VPS)
│   ├── stop.sh                       # Graceful stopper
│   ├── vps_setup.sh                  # Turnkey installation script for Ubuntu/Debian
│   ├── install_services.sh           # Systemd service installer
│   ├── watchdog.sh                   # Auto-heal watchdog cron script
│   └── systemd/                      # Unit file templates (.service)
├── data/                             # SQLite DB (`search_automation.db`) & profiles
└── .env                              # Kredensial rahasia (API Keys & Tokens)
```

---

## 🛠️ Setup & Instalasi

### 1. Prasyarat Lingkungan
- **Go**: Versi 1.22+ (terpasang di system atau `~/go-sdk/go/bin`)
- **Python**: Versi 3.10+ (dengan virtual environment)
- **Google Chrome**: Google Chrome Stable untuk headless Undetected-ChromeDriver
- **Sistem Operasi**: Linux VPS (Ubuntu/Debian) atau WSL2 Ubuntu

### 2. Setup Otomatis di VPS Baru (Turnkey)
Cukup jalankan script setup satu klik:
```bash
cd ~/Project/google-automation
bash scripts/vps_setup.sh
```

### 3. Setup Manual (Local / WSL2)
```bash
cd ~/Project/google-automation

# Setup Python Worker Virtual Environment
cd worker
python3 -m venv .venv
source .venv/bin/activate
pip install -r requirements.txt
cd ..

# Setup Go Dependencies & Build Binaries
export PATH="$HOME/go-sdk/go/bin:$PATH"
go mod tidy
go build -o bin/orchestrator cmd/main.go
go build -o bin/dashboard cmd/dashboard/main.go
```

---

## ⚙️ Konfigurasi

### 1. File `.env` (Kredensial Rahasia)
Salin template `.env.example` ke `.env` dan isi kunci API yang relevan:
```bash
cp .env.example .env
nano .env
```
Contoh isi `.env`:
```bash
# Proxy Webshare API Keys (Multi-Key)
WEBSHARE_API_KEY_0=your_primary_webshare_key
WEBSHARE_API_KEY_1=your_backup_webshare_key

# STT Audio CAPTCHA & AI Query Expander (Groq - Cepat & Gratis)
OPENAI_API_KEY=gsk_xxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxx

# Telegram Bot Notifier & Remote Control (Opsional)
TELEGRAM_BOT_TOKEN=123456789:ABC-DEF1234ghIkl-zyx57W2v1u123ew11
TELEGRAM_CHAT_ID=123456789
```

### 2. File `config/config.yaml`
```yaml
domains:
  - bagasunix.com

engine_ratio:
  google: 70                         # 70% Google Search
  bing: 10                           # 10% Bing Search
  direct: 10                         # 10% Direct Bookmark/Homepage
  social: 10                         # 10% Social Referral (Twitter/Reddit)

scheduler:
  max_search_per_proxy: 5            # Maksimal pencarian per proxy per hari
  new_article_boost: 5               # Artikel baru (<7 hari): kuota 5x pencarian
  regular_max: 3                     # Artikel biasa: kuota 3x pencarian
  captcha_pause_hours: 3             # Auto-pause per engine jika CAPTCHA spike
  min_cooldown_seconds: 30           # Jeda minimum antar tugas
  max_cooldown_seconds: 120          # Jeda maksimum antar tugas
  post_exit_cooldown_min: 30         # Jeda setelah keluar dari artikel
  post_exit_cooldown_max: 120
  active_hours_start: 7              # Jam aktif mulai (07:00 waktu lokal proxy)
  active_hours_end: 23               # Jam aktif berakhir (23:00 waktu lokal proxy)

proxy:
  provider: "webshare"               # "webshare" | "residential" | "custom_file"
  refresh_interval_hours: 3
  health_check_timeout: 8
  webshare_api_keys:
    - "YOUR_KEY_0"

captcha:
  enabled: true
  max_attempts: 3
  solver: "openai_api"               # "openai_api" (Groq) | "google_web" | "whisper"
  whisper_model: "base"              # Model lokal ringan (~70MB)
```

---

## 🚦 Cara Menjalankan

### A. Menjalankan Otomatis (CLI Mode)
```bash
./scripts/run.sh
```
Untuk menghentikan:
```bash
./scripts/stop.sh
```

### B. Menjalankan Live Web Control Panel (Dashboard v3)
```bash
./bin/dashboard --serve :8080
```
Buka di browser: `http://<IP_VPS_ANDA>:8080` untuk mengakses **Web Admin Suite**:
* **🤖 Bot Fleet Grid & Live Terminal**: Memantau worker paralel secara live (`Worker #1`..`#N`), mengatur jumlah browser yang aktif (*concurrency scaling*), dan membaca streaming log real-time dengan filter worker tanpa perlu buka SSH.
* **📊 Analytics & Trends**: Grafik garis interaktif **Chart.js** pergerakan ranking dan tombol unduh **📥 Export CSV Report**.
* **🌐 Articles & SERP**: Memantau posisi ranking setiap artikel dan memicu pencarian instan dengan tombol **⚡ Cari Sekarang**.
* **🛡️ Proxy Hub**: Memantau status alokasi proxy aktif, latensi, dan status karantina.
* **⚙️ Settings Editor**: Mengubah pengaturan `config.yaml` dan `.env` langsung dari web browser dan menerapkan perubahan secara *live hot-reload*.

### C. Menjalankan 24/7 via Systemd di VPS
```bash
sudo bash scripts/install_services.sh

# Start services
sudo systemctl start google-automation
sudo systemctl start google-dashboard

# Monitoring log live
sudo journalctl -u google-automation -f
```

---

## 🛡️ Rincian Fitur Anti-Deteksi

### 1. Browser Fingerprint
- **CDP Stealth Injection**: Injeksi script JavaScript pada `Page.addScriptToEvaluateOnNewDocument` untuk mem-patch prototype properti `navigator.webdriver = false`, `navigator.plugins`, `navigator.languages`, dan `window.chrome`.
- **Canvas Noise**: Injeksi mikro-noise deterministik pada `toDataURL` dan `getImageData` agar hash canvas berganti natural.
- **WebGL Mocking**: Mocking ekstensi `WEBGL_debug_renderer_info` dan fallback mock untuk lingkungan server headless tanpa GPU/X11.
- **WebRTC IP Shield**: Mencegah kebocoran IP asli VPS melalui WebRTC STUN/TURN dengan policy `disable_non_proxied_udp` dan SDP candidate sanitization.
- **AudioContext Spoofing**: Injeksi mikro-noise pada `AudioBuffer` & `AnalyserNode` untuk melindungi dari composite fingerprinting.
- **Warm Profiles**: Penyimpanan cookies, cache, dan riwayat browsing di direktori `data/profiles/profile_0`..`9`.
- **Mobile Emulation**: Dukungan rotasi User-Agent smartphone (Android & iPhone), mobile viewports, dan CDP touch emulation (`maxTouchPoints = 5`).

### 2. Search Behavior
- **Pre-Search & Browsing Santai**: Melakukan pencarian topik umum sebelum menuju artikel target untuk membangun riwayat sesi alami.
- **Typo Humanizer**: Pengetikan 80–200ms per karakter dengan simulasi salah ketik (*typo*) sesekali dan koreksi tombol *backspace*.
- **SERP Snippet Reading**: Dwell 5–15 detik membaca deskripsi snippet sebelum mengklik link.
- **Pogo-Sticking Engine**: Klik kompetitor rank 1–2 sebentar (4–7s), tekan tombol *Back (Bounce)*, lalu klik target `bagasunix.com`.
- **Bezier Mouse Movements**: Pergerakan kursor melengkung alami dan scroll ke viewport elemen sebelum mengklik.
- **Consent Banner Dismissal**: Klik manusiawi pada popup cookie/consent Google & Bing.

### 3. Post-Click Engagement
- **Initial Scan**: Jeda 5–12 detik saat mendarat di halaman artikel.
- **Smooth Chunk Scrolling**: Scroll bertahap 200–500px, bukan lompat instan.
- **Element Pausing**: Jeda lebih lama saat membaca Heading H2/H3 (3–7s), blok kode (4–10s), dan gambar/diagram (3–6s).
- **Reading Heatmap**: Simulasi seleksi/highlight teks dan scroll balik ke atas (*re-reading*).
- **Multi-Tab Browsing**: Membuka internal link di tab baru (*Ctrl+Click*), membaca 10–20s, menutup tab, dan kembali ke artikel utama.
- **Exit Variety**: 70% Menutup browser langsung, 30% berselancar ke situs distraksi sebelum sesi berakhir.

### 4. Proxy Reliability & Dynamic Throttling
- **Multi-Key Failover**: Prioritas key Webshare #0, otomatis beralih ke key #1 jika kuota habis.
- **Auto-Quarantine**: Karantina otomatis 4 jam jika proxy memicu CAPTCHA, dan 2 jam jika mengalami 3x network error berturut-turut.
- **Time-of-Day Awareness**: Pencarian hanya berjalan pada jam aktif pengguna (07:00–23:00) sesuai zona waktu lokal IP proxy.
- **Per-Engine Auto Fallback**: Jika Google memicu jeda CAPTCHA, pencarian otomatis dialihkan ke Bing/Direct/Social tanpa menghentikan bot.

### 5. Algoritma Deep SEO & Algorithmic Boosters
- **Google Autocomplete Hijacker**: Mengetik kata kunci bertahap, menunggu dropdown saran pencarian Google (*Google Suggest*), lalu mengaitkannya dengan brand `bagasunix` untuk menanam nama web di autocomplete publik.
- **People Also Ask (PAA) Explorer**: Membuka dan membaca accordion tanya-jawab di Google SERP sebelum mengklik web target guna memperkuat sinyal riset intent mendalam (*deep research intent*).
- **UX Engagement & Social Share Simulator**: Simulasi interaksi pengguna tingkat lanjut (*hover* tombol share Twitter/FB/WA, fokus form komentar, dan klik navigasi daftar isi).

---

## 🗄️ Database Schema (SQLite WAL Mode)

Database SQLite murni Go disimpan di `search_automation.db` dengan tabel:
- **`proxies`**: `ip`, `port`, `country`, `timezone`, `active`, `latency_ms`, `used_count`, `blacklisted`, `blacklist_reason`.
- **`articles`**: `domain`, `url`, `title`, `meta_desc`, `topic`, `searched_count`, `serp_position`, `last_searched_at`.
- **`tasks`**: `article_id`, `proxy_id`, `engine`, `status`, `result_json`, `error`, `created_at`, `completed_at`.
- **`daily_stats`**: `date`, `total_search`, `success`, `fail`, `captcha`, `avg_dwell_seconds`, `avg_serp_position`.

---

## 📸 Screenshots Otomatis

Python worker otomatis menyimpan tangkapan layar (screenshot) saat terjadi kondisi penting/error:
- CAPTCHA terdeteksi di Google/Bing (`captcha_target_*.png`)
- Domain target tidak ditemukan di SERP (`target_not_found_*.png`)
- Kesalahan navigasi landing page (`wrong_landing_*.png`)
- Exception / error tak terduga (`exception_*.png`)

File tersimpan di direktori: `screenshots/{task_id}_{error_type}_{timestamp}.png`.

---

## 🔌 gRPC Protocol Definition

Komunikasi antar proses Go Orchestrator dan Python Worker menggunakan gRPC pada port `50051`:

```protobuf
syntax = "proto3";
package searchautomation;

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
    int32 serp_position = 5;            // 0 = not found, 1-30 = found position
    int32 dwell_time_seconds = 6;
    int32 scroll_depth_percent = 7;
    int32 internal_clicks = 8;
    bool captcha_hit = 9;
    string error = 10;
    int32 bandwidth_used_kb = 11;
}
```

---

## 📱 Perintah Telegram Bot

Jika bot token Telegram diisi pada `.env`, bot dapat dikendalikan dari jarak jauh:
* `/status` — Memeriksa status orchestrator, worker, pool proxy yang tersedia, dan sisa waktu cooldown.
* `/stats` — Menampilkan ringkasan analitik hari ini (Total pencarian, Sukses, CAPTCHA, Rata-rata Dwell).
* `/pause` — Menghentikan sementara jadwal pencarian bot.
* `/resume` — Melanjutkan kembali jadwal pencarian bot.

---

## 📊 Format Ekspor Laporan (CSV)

Endpoint `/api/export/csv` pada Web Dashboard menyediakan file laporan analitik harian:
```csv
Date,TotalSearches,Success,Fail,CAPTCHA,SuccessRatePercent,AvgDwellSeconds,AvgSerpPosition
2026-08-29,24,23,1,0,95.83,112.40,3.50
```

---

## 💡 Tips Penggunaan di VPS
1. **Groq API Key**: Masukkan kunci API Groq gratis ke `.env` (`OPENAI_API_KEY=gsk_...`) untuk transkripsi audio CAPTCHA secepat kilat (<1 detik) dan variasi query AI alami.
2. **Kualitas Proxy**: Untuk Google Search bervolume tinggi, gunakan Residential Proxy melalui pengaturan `provider: "residential"` di `config.yaml`.
3. **Auto-Start**: Gunakan `scripts/install_services.sh` agar engine berjalan otomatis dan stabil 24/7 di VPS Anda.
