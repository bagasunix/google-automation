# PLAN & ROADMAP — Search Automation bagasunix.com

Last updated: 2026-08-29

---

## Status Pipeline Saat Ini

| Komponen | Status | Catatan |
|---|---|---|
| Go Orchestrator | ✅ Running | Proxy rotation, sitemap scraper, gRPC client, SQLite WAL mode |
| Python Worker (gRPC :50051) | ✅ Running | SeleniumBase UC (Undetected-ChromeDriver), stealth session, dwell reading |
| Webshare Proxy (Multi-Key) | ✅ Active | Multi-key failover + bandwidth tracking (1GB limit) |
| Bing Search | ✅ Working | Normal parsing, browsing & click engagement |
| Google Search | ⚠️ Perlu optimasi | Datacenter IP sering kena /sorry/ — butuh CDP stealth injection & residential proxy |
| CAPTCHA Solver | ✅ Integrated | Audio reCAPTCHA v2 (Groq Whisper-large-v3-turbo) + token solver fallback |

---

## Roadmap Pengembangan Berurutan 📋

### 🚀 Fase 1: Quick Wins, Anti-Deteksi Kritis & Keamanan
- [x] **1.1. Injeksi Stealth Script via CDP**
  - Hubungkan `build_stealth_script()` di `worker/browser/stealth.py` ke `worker/browser/session.py`.
  - Menggunakan `execute_cdp_cmd("Page.addScriptToEvaluateOnNewDocument", ...)` dengan prototype property patch dan WebGL fallback mock untuk server tanpa GPU/X11.
- [x] **1.2. Sinkronisasi Cooldown Go vs Python**
  - Menghilangkan redundansi double delay di `worker/main.py` dan memusatkan kontrol cooldown di Go orchestrator.
- [x] **1.3. Keamanan Kredensial (.env Integration)**
  - Mengintegrasikan loader `.env` di Go (`internal/config/config.go`) dan Python (`worker/main.py`).
  - Menyediakan `.env.example` dan menambahkan aturan di `.gitignore`.

---

### 🎯 Fase 2: SERP Navigation & Realisme Pencarian
- [x] **2.1. SERP Pagination (Halaman 2, 3 & Infinite Scroll)**
  - Navigasi otomatis ke halaman 2 dan 3 jika target belum ditemukan di halaman 1 (Google: `&start=10`/`&start=20`, Bing: `&first=11`/`&first=21`).
  - Posisi ranking diakumulasikan secara riil (posisi 1–30).
- [x] **2.2. Long-Tail & Query Variation Generator**
  - Menambahkan metode pencarian long-tail: `cara [keyword]`, `[keyword] tutorial`, `[keyword] [brand]`.
  - Simulasi typo keyboard manusiawi dengan jeda per karakter dan koreksi backspace di `worker/browser/humanizer.py`.
- [x] **2.3. Mobile Browser Emulation**
  - Menambahkan User-Agent mobile (Android & iPhone), Viewports mobile, CDP touch emulation (`maxTouchPoints = 5`), dan device metrics override.

---

### 🛡️ Fase 3: Profile Persistence & Proxy Reliability
- [x] **3.1. Warm Browser Profiles (Cookie & History Retention)**
  - Pool direktori browser persistent (`data/profiles/profile_0` .. `profile_9`) untuk menyimpan cookies, session, dan cache browsing secara natural di `worker/browser/profiles.py` & `session.py`.
- [x] **3.2. Proxy Health Scoring & Auto-Quarantine**
  - Auto-quarantine proxy selama 4 jam jika memicu CAPTCHA, dan isolasi 2 jam jika mengalami 3x network error berturut-turut di `internal/proxy/pool.go`.

---

### 📊 Fase 4: Observabilitas & Interaktivitas
- [x] **4.1. Live Interactive Web Dashboard**
  - Mode live web server di `cmd/dashboard/main.go` (`./bin/dashboard --serve :8080`) dengan auto-refresh 30 detik dan endpoint JSON `/api/stats`.
- [x] **4.2. Interactive Telegram Bot Commands**
  - Telegram bot command listener aktif untuk `/status`, `/stats`, `/pause`, dan `/resume` di `internal/notify/telegram.go` & `internal/orchestrator/orchestrator.go`.

---

### 🔮 Fase 5: Advanced SEO Boost & VPS Resilience
- [x] **5.1. Pogo-Sticking Engine & Competitor Bounce Rate Manipulator**
  - Klik kompetitor di ranking 1–2, diam sebentar (4–8s), lalu klik Back (Bounce) kembali ke SERP.
  - Lanjutkan dengan mengklik target `bagasunix.com` dengan dwell panjang (60–180s) untuk mengirimkan sinyal kepuasan pengguna (*satisfied user signal*) terkuat ke Google RankBrain.
- [x] **5.2. Natural Traffic Source Mixer**
  - Diversifikasi sumber traffic di scheduler (70% Google/Bing Organic, 15% Direct/Bookmark, 10% Social Referral via Twitter/Reddit/LinkedIn).
  - Menjaga profil analytics website tetap natural dan bebas anomali.
- [x] **5.3. Multi-Tab Browsing & Realistic Reading Heatmap**
  - Membuka artikel terkait di tab baru (*Ctrl+Click*), berpindah antar tab secara bergantian, lalu menutup tab.
  - Simulasi seleksi/highlight teks penting dan jeda lebih lama saat scroll melewati gambar/tabel.
- [x] **5.4. Smart Keyword Priority Matrix**
  - Bobot prioritas dinamis: artikel yang terdeteksi di halaman 2 atau 3 (posisi 11–30) otomatis mendapat bobot frekuensi pencarian 3x lebih tinggi untuk didorong (*breakthrough*) ke halaman 1.
- [x] **5.5. VPS Systemd Service Suite & Auto-Heal Watchdog**
  - File unit systemd (`google-automation.service` & `google-dashboard.service`) untuk auto-start on boot di VPS.
  - Health watchdog script (`scripts/watchdog.sh`) dan script installer satu klik (`scripts/install_services.sh`).

---

### 🧠 Fase 6: Next-Level Intelligence & Deep Anti-Detection
- [x] **6.1. AI Semantic Query Expander (Groq LLM)**
  - Integrasi generator query cerdas berbasis LLM (Groq Llama-3/Whisper) untuk membuat puluhan variasi query pencarian alami (pertanyaan, bahasa gaul/singkatan Indonesia, perbandingan).
- [x] **6.2. WebRTC & AudioContext Deep Anti-Leak Protection**
  - Mencegah kebocoran IP asli VPS melalui WebRTC STUN/TURN, serta menyamarkan fingerprint AudioBuffer/Oscillator dan font Linux.
- [x] **6.3. Google Search Console (GSC) Opportunity Optimizer**
  - Modul analisis CTR & Impression: otomatis memprioritaskan keyword ber-impression tinggi dengan CTR rendah untuk akselerasi ranking.
- [x] **6.4. Live Dashboard v2 (Interactive Line Chart & CSV Export)**
  - Grafik visual interaktif histori pergerakan ranking per artikel, tombol trigger pencarian manual instan, dan export data ke CSV/Excel.
- [x] **6.5. Multi-Provider Residential Proxy Hub**
  - Adaptor fleksibel untuk beralih atau menggabungkan proxy Webshare (Datacenter) dengan penyedia Residential Proxy premium (Smartproxy, IPRoyal, BrightData).

---

### 👑 Fase 7: Enterprise Multi-Worker Fleet, Web Admin Suite & Deep SEO
- [x] **7.1. Multi-Worker Concurrency Engine (Go Fleet Manager)**
  - Mesin concurrency worker pool (`internal/orchestrator/fleet.go` & `orchestrator.go`) untuk menjalankan *N browser instance paralel* dengan isolasi proxy per worker dan dynamic concurrency scaling (`1..10 workers`).
- [x] **7.2. Live Bot Fleet Grid UI (Multi-Worker Status Matrix)**
  - Tampilan visual kartu grid untuk setiap worker yang sedang aktif (`Worker #1`..`#N`), menampilkan bendera negara proxy, judul artikel target, tahapan aksi saat ini, dan progress bar membaca (*dwell time & scroll depth*).
- [x] **7.3. Multi-Stream Live Web Terminal & Worker Log Filter**
  - Terminal web streaming real-time di browser (`:8080/logs`) dengan dropdown filter worker (`[All Workers]`, `[Worker #1]`, `[Worker #2]`) dan label prefix warna per worker tanpa perlu buka SSH.
- [x] **7.4. Interactive Web Settings Editor & Hot-Reload (Web GUI)**
  - Form UI di Web Dashboard untuk mengubah konfigurasi `config.yaml` dan `.env` (rasio traffic, cooldown, target domain, API keys) dan menerapkan perubahan secara instan (*live hot-reload*) tanpa restart manual.
- [x] **7.5. Article & SERP Rank Manager UI with One-Click Actions**
  - Daftar artikel lengkap dengan posisi SERP, tombol **⚡ "Cari Sekarang"** untuk memicu pencarian instan per artikel, dan tombol **🔄 "Sync Sitemap Now"**.
- [x] **7.6. Proxy Hub & Health Monitoring Panel**
  - Tampilan alokasi proxy real-time (*In-Use by Worker X / Idle / Quarantined*), pengukur latensi, dan tombol **"Test All Proxies"**.
- [x] **7.7. Google Autocomplete & Suggestion Hijacker (Brand Association)**
  - Mengetik kata kunci huruf demi huruf di Google, menunggu dropdown Google Suggest muncul (`ul[role="listbox"]`), lalu mengetikkan brand `bagasunix` untuk menanamkan nama web kita di Google Autocomplete publik.
- [x] **7.8. "People Also Ask" (PAA) Accordion Explorer**
  - Mengklik untuk membuka 1–2 pertanyaan di accordion "People Also Ask" (PAA) Google SERP, membaca 3–5 detik, menutupnya kembali, lalu melanjutkan klik ke target untuk mengirim sinyal *deep intent searcher*.
- [x] **7.9. Multi-Domain Campaign Manager (Skala Banyak Website)**
  - Mendukung pengelolaan banyak domain website sekaligus (misal `bagasunix.com`, `domain-2.com`, dll) dalam 1 orchestrator dengan isolasi sitemap, kuota harian, dan filter di Web Dashboard.
- [x] **7.10. UX Engagement & Social Share Simulator**
  - Simulasi interaksi pengguna tingkat lanjut: *hover* tombol share medsos (Twitter, Facebook, WA), interaksi form komentar (*focus input*), dan klik daftar isi (*Table of Contents*).

---

## Cara Menjalankan & Deployment VPS

### 1. Menjalankan Otomatis (Local / WSL / VPS)
```bash
./scripts/run.sh
```

### 2. Menjalankan Live Dashboard Server
```bash
./bin/dashboard --serve :8080
# Buka di browser: http://<IP_VPS_ANDA>:8080
```

### 3. Setup Turnkey di VPS Baru (Ubuntu / Debian)
```bash
bash scripts/vps_setup.sh
```
