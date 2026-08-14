# PLAN — Search Automation bagasunix.com

Last updated: 2026-08-14

---

## Status Pipeline

| Komponen | Status |
|---|---|
| Go Orchestrator | ✅ Running |
| Python Worker (gRPC :50051) | ✅ Running |
| Webshare Proxy (key#0, 10 proxy) | ✅ Active |
| Bing Search | ✅ Working |
| Google Search | ⚠️ Datacenter IP selalu CAPTCHA — butuh residential proxy |
| SERP Hit (bagasunix.com muncul) | ❌ Belum — artikel belum ranking |

---

## Selesai ✅

### Core Engine
- [x] Go + Python hybrid (gRPC IPC)
- [x] Webshare proxy API integration
- [x] Schema UNIQUE INDEX `proxies(ip,port)` untuk ON CONFLICT upsert
- [x] Parallel article extraction (20 goroutines, ~4s untuk 143 artikel)
- [x] AcquireUsableProxy infinite loop fix
- [x] `active_hours_start: 0` valid (tidak di-override ke 7)
- [x] ResetCycle() auto-reset saat pool habis
- [x] MaxSearchesToday = proxy_count × max_search_per_proxy

### Engine Fallback & CAPTCHA Handling
- [x] Per-engine CAPTCHA pause (`TriggerEnginePause`, `PickEngineAvailable`)
- [x] Google kena CAPTCHA → hanya Google di-pause, Bing tetap jalan
- [x] `CheckCaptchaRate()` per-engine (bukan global)
- [x] `enginePausedUntil map[string]time.Time` di scheduler

### Multi-key Webshare Rotation
- [x] Config `webshare_api_keys` list (multi-key)
- [x] `APIKeyIndex` di `Proxy` dan `PooledProxy` struct
- [x] Pool sorted by APIKeyIndex — key#0 selalu duluan
- [x] `NewScraperWithWebshareKeys`, `scrapeWebshareWithKey`
- [x] Backward compat: `webshare_api_key` (single) masih bisa dipakai

### Humanizer & Anti-Detection
- [x] UA pool 18 agents (Chrome/Edge/Firefox/Safari, Win/Mac/Linux)
- [x] `clear_cookies()` per session (fresh state tiap ganti proxy)
- [x] `human_click_element`: `scroll_into_view_if_needed` sebelum klik (fix Y negatif)
- [x] Consent banner dismiss pakai `human_click_element` (bukan `.click()`)
- [x] `_browse_serp_casually`: klik result → baca 20-60s → go_back
- [x] Internal clicks: 1-2 artikel, baca full `simulate_reading`
- [x] `import random` di google.py dan bing.py

### Bing Fixes
- [x] Bing consent banner dismiss: homepage + post-SERP
- [x] `parse_bing_serp`: `wait_for_selector("li.b_algo")` sebelum parse
- [x] `networkidle` → `domcontentloaded` (timeout fix)
- [x] Bing page 2: navigate via URL `first=11` (fix 0 results dari redirect URL)

### Database & Infra
- [x] SQLite WAL mode + `busy_timeout(5000)` (fix SQLITE_BUSY)
- [x] `TodayTaskCountByEngine` + `TodayCaptchaCountByEngine` queries
- [x] gRPC `worker_timeout: 600` (fix DeadlineExceeded pada long task)
- [x] Debug screenshot on 0 Bing results + URL + title log

---

## Pending / Next Steps 🔧

### Priority Tinggi

- [ ] **Residential Proxy** — datacenter IP Webshare selalu kena Google sorry page.
  Solusi: upgrade ke Webshare residential, atau coba provider lain (Oxylabs, Bright Data, Smartproxy).
  Setelah itu Google 70% bisa aktif lagi.

- [ ] **Fix stealth.py RuntimeWarning** — `set_extra_http_headers` dan `add_init_script`
  perlu `await`, tapi dipanggil dari sync context. Perlu refactor `apply_stealth()` jadi async.

- [ ] **Install playwright-stealth** — saat ini fallback ke custom scripts.
  Warning: `playwright-stealth not installed` muncul setiap task.

### Priority Sedang

- [ ] **VPS Deployment** (43.156.122.137, user: bukalab) — SSH port 22 refused.
  Perlu fix via Tencent Cloud VNC console. Setelah jalan, pindahkan worker ke VPS.

- [ ] **SERP Position Tracking** — semua artikel masih `N/A` karena belum ranking.
  Normal untuk situs baru. Monitor setelah 2-4 minggu.

- [ ] **Multiple domain support** — config sudah support list domains,
  tapi belum ditest dengan lebih dari 1 domain.

- [ ] **Graceful daily reset** — saat ini `ResetCycle()` di-call manual saat pool habis.
  Perlu reset otomatis setiap tengah malam.

### Priority Rendah

- [ ] **Analytics dashboard** — export stats ke HTML/CSV untuk monitoring mingguan.
- [ ] **Telegram notif** — kirim summary harian (tasks, success rate, SERP positions) via bot.
- [ ] **Docker Compose** — sudah ada DOCKER.md, belum ditest di production.
- [ ] **Rate limiter per domain** — kalau multi-domain, limit search per domain per hari.

---

## Config Aktif (2026-08-14)

```yaml
engine_ratio: google: 70 / bing: 30
max_search_per_proxy: 5
captcha_pause_hours: 3
worker_timeout: 600
webshare_api_keys: [key#0]   # 10 proxy datacenter US
active_hours: 0-24 (testing)
cooldown: 5-15s (testing)
```

---

## Catatan Teknis

- Webshare datacenter proxy → Bing OK, Google CAPTCHA 100%. Butuh residential untuk Google.
- Setiap task bisa 2-5 menit (dwell reading + internal clicks). Normal dengan timeout 600s.
- `max_search_per_proxy: 5` → 10 proxy × 5 = 50 tasks/hari maksimal.
- Cooldown testing (5-15s) perlu dinaikkan ke 30-120s saat production.
- `active_hours_start: 0 / end: 24` untuk testing, naikkan ke 7-23 saat production.
