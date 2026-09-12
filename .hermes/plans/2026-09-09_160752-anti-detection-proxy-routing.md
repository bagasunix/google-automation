# Anti-Deteksi & Proxy Routing — Progress Plan

> **Tujuan dokumen:** catatan status agar kalau sesi terputus, langsung tahu sampai mana.
> Bukan plan eksekusi dari nol — ini campuran "sudah selesai" + "sisa yang belum".
>
> **Terakhir diverifikasi: 2026-09-11.** Fase 1–5 & 7 SELESAI dan sudah ter-commit di `6fd6335`.
> Sisa: **push** + **uji live jalur google** (terblokir: `.env` belum punya kredensial residential).

**Goal keseluruhan:** hilangkan Google `/sorry/` "DOS Captcha" pada bagasunix.com search-automation, dengan (a) fingerprint konsisten per proxy IP, (b) routing engine sadar jenis proxy, (c) residential proxy jalan berdampingan dengan Webshare datacenter.

**Repo:** `~/Project/google-automation` (Hybrid Go orchestrator + Python worker, gRPC :50051)
**Baseline commit saat kerja:** `54a9115`
**Status commit (update 2026-09-11):** semua Fase 1–5 & 7 SUDAH di-commit sebagai **`6fd6335`**
(`feat: v2 multi-engine anti-detection + per-proxy routing`) — satu commit besar, bukan 5 commit
granular seperti saran lama di bawah. Working tree BERSIH.
**Belum di-push:** `origin/v2` masih di `54a9115`. Branch `v2` dan `single-engine` dua-duanya
menunjuk ke `6fd6335` (identik, `git diff v2 single-engine` kosong).

---

## Status Ringkas

| Fase | Status | Commit? |
|---|---|---|
| 1. Fingerprint deterministik per-IP | ✅ SELESAI, terverifikasi | ✅ `6fd6335` |
| 2. Reset warm profile lama | ✅ SELESAI | (aksi runtime, bukan kode) |
| 3. Routing engine sadar datacenter | ✅ SELESAI, unit test PASS + terbukti live | ✅ `6fd6335` |
| 4. Residential proxy berdampingan | ✅ SELESAI, unit test PASS | ✅ `6fd6335` |
| 5. Fingerprint per-EXIT residential | ✅ SELESAI, terverifikasi | ✅ `6fd6335` |
| 7. Sumber proxy gratis (sources) + fix geonode | ✅ SELESAI, terverifikasi live 2026-09-11 | ✅ `6fd6335` |
| 6. Commit + jalankan uji end-to-end live | 🟡 SEBAGIAN — commit ✅, push ❌, E2E dihentikan di tengah | — |

**Update 2026-09-11:** klaim lama "belum di-commit" sudah TIDAK berlaku. `git status` bersih;
semua kerja ada di `6fd6335`. Yang benar-benar tersisa: **push** + **uji live sampai tuntas**.

---

## Fase 1 — Fingerprint Deterministik per Proxy IP ✅ SELESAI

**Masalah:** tiap sesi fingerprint di-random ulang; warm-profile cookie per-IP tapi fingerprint berubah → sinyal "orang lama, device berubah". Juga sempat: iPhone + GPU NVIDIA + tz UTC (mismatch fatal).

**Solusi:** seed seluruh fingerprint dari `crc32(proxy_ip)` — IP sama → fingerprint sama; IP baru → fingerprint baru total.

**File diubah:**
- `worker/browser/stealth.py`
  - `_pick_ua_for_locale(locale, rng=None)` — terima RNG opsional (badan pakai `_r`, float pakai `roll`)
  - `StealthProfile.for_proxy(..., proxy_ip="")` — kalau ada IP: `rng = random.Random(zlib.crc32(proxy_ip.encode()))`, else `random.Random()`. Semua `random.*` di badan → `rng.*`
  - import `zlib`; docstring modul baris ~15 diperbarui
- `worker/browser/session.py` — `for_proxy(..., proxy_ip=self.proxy_ip)`

**Verifikasi (sudah dijalankan, PASS):** IP `31.58.9.4` 3x identik (Windows+NVIDIA+seed162+Europe/Berlin); IP beda → iPhone+AppleGPU+seed44; tanpa IP → tetap random.

---

## Fase 2 — Reset Warm Profile Lama ✅ SELESAI

Profil `data/profiles/profile_0..49` lama terbentuk saat fingerprint masih random → cookie & fingerprint tidak sinkron. Sudah `reset_all_profiles()` (12 profil → 0). PID stale `.worker.pid`/`.orchestrator.pid` sudah dihapus. Worker saat itu mati (aman).

---

## Fase 3 — Routing Engine Sadar Datacenter ✅ SELESAI

**Masalah (akar "DOS Captcha"):** semua proxy Webshare = datacenter; engine dipilih acak tanpa lihat jenis proxy → proxy datacenter dikirim ke Google → blok "automated queries" (`_is_doscaptcha` di `worker/captcha/audio_sorry.py`).

**Solusi:** deteksi datacenter di health check (numpang request geo ip-api.com yang sudah ada — nol request tambahan), lalu scheduler route: datacenter → hanya direct/social; residential/unknown → ratio penuh incl. google/bing.

**File diubah:**
- `internal/proxy/health.go`
  - `HealthResult.IsDatacenter bool`
  - `GeoIPResponse` +`Hosting`,`Proxy`; `ipwhoisResponse` +`Connection{Org,ISP}`
  - `queryIPAPI`/`queryIPWhois` return tambahan `datacenter bool` (ip-api pakai flag `hosting||proxy`; ipwho.is pakai keyword org via `datacenterOrgKeywords`)
  - `DetectGeoIP(ip) (country, tz string, datacenter bool)` — SIGNATURE BERUBAH
  - method baru `(*Checker).resolveGeo(result, p)` (dipakai Fase 4 juga)
- `internal/proxy/pool.go` — `PooledProxy.IsDatacenter bool`
- `internal/proxy/manager.go` — teruskan `r.IsDatacenter` ke PooledProxy
- `internal/scheduler/dynamic.go` — `PickEngineForProxy(px)` + `pickEngine(datacenterOnly bool)`; `PickEngineAvailable()` jadi wrapper `pickEngine(false)`
- `internal/orchestrator/orchestrator.go` — ganti `PickEngineAvailable()` → `PickEngineForProxy(px)`
- `cmd/dashboard/main.go:~1318` — call site `DetectGeoIP` disesuaikan (3 nilai, `_` untuk datacenter)

**Verifikasi (unit test sementara, PASS lalu dihapus):** datacenter 2000x roll → nol google/bing; residential → dapat google.

---

## Fase 4 — Residential Proxy Berdampingan dengan Webshare ✅ SELESAI

**4 sub-masalah yang dibenahi:**
1. Butuh key `provider` yang tak ada → dihapus ketergantungannya; residential aktif otomatis jika `RESIDENTIAL_HOST`+`RESIDENTIAL_USER` terisi.
2. Kredensial residential tak bisa via `.env` → ditambah override.
3. Provider either/or → sekarang Webshare + residential dalam SATU pool.
4. Deteksi datacenter salah sasaran di gateway residential → gateway di-skip geo & dipaksa `IsDatacenter=false`.

**File diubah:**
- `internal/proxy/scraper.go` — `Proxy` +`Timezone string` +`IsResidential bool`
- `internal/proxy/residential.go` — `residentialCountryTZ` map; `GenerateResidentialProxies` set `IsResidential=true`, `Timezone` dari country, `Country` uppercase
- `internal/proxy/health.go` — `resolveGeo`: kalau `p.IsResidential` → pakai preset Country/Timezone, `IsDatacenter=false`, SKIP `DetectGeoIP`
- `internal/proxy/pool.go` — `PooledProxy.IsResidential bool`
- `internal/proxy/manager.go` — `hasWebshare()` helper; `refresh()` load Webshare + residential BERSAMAAN (custom_file masih menggantikan); teruskan `IsResidential`
- `internal/config/config.go` — import `strconv`; `applyEnvOverrides` baca `RESIDENTIAL_HOST/PORT/USER/PASSWORD/COUNTRY`
- `internal/scheduler/dynamic.go` — `PickEngineForProxy`: `datacenterOnly := px.IsDatacenter && !px.IsResidential`
- `.env.example` — blok `RESIDENTIAL_*`
- `config/config.yaml` — blok `residential_*` (kosong; kredensial via .env)

**Verifikasi (unit test sementara, PASS lalu dihapus):** residential yang gateway-nya ke-flag datacenter TETAP dapat google; datacenter murni tetap diblok; `GenerateResidentialProxies` set flag+tz+country benar & session-id berputar.

**Catatan:** field `cfg.Proxy.Provider` kini no-op (cuma muncul di teks HTML dashboard). Biarkan sebagai legacy; tidak dihapus.

---

## Fase 5 — Fingerprint per-EXIT Residential ✅ SELESAI

**Masalah:** 20 endpoint residential share gateway IP yang sama → `crc32(IP)` untuk warm-profile/fingerprint identik semua. Yang membedakan identitas cuma session-id di username (exit IP beda). Idealnya fingerprint & slot cookie di-seed dari identitas unik per-exit, bukan gateway IP.

**Solusi:** "identity key". Residential dikenali dari `session-` di `proxy_username` → seed dari username; datacenter (tanpa session token) tetap seed dari `proxy_ip`.

**File diubah:**
- `worker/browser/stealth.py` — `for_proxy(..., seed_key="")`; precedence `seed_key` > `proxy_ip` > unseeded (`key = seed_key or proxy_ip`)
- `worker/browser/profiles.py` — `get_profile_dir(..., identity_key="")`; slot pakai `key = identity_key or proxy_ip`
- `worker/browser/session.py` — `is_gateway_residential = "session-" in proxy_username`; hitung `identity_key`, teruskan ke `for_proxy(seed_key=...)` dan `get_profile_dir(identity_key=...)`

**Verifikasi (dijalankan via venv, PASS):**
- 1 gateway IP + 3 session beda → 3 fingerprint unik + 3 slot warm-profile unik
- session sama 2× → fingerprint & slot stabil
- regresi datacenter: IP sama stabil, IP beda → fingerprint beda

---

---

## Fase 7 — Sumber Proxy Gratis (sources) + Fix Geonode ✅ SELESAI

**Konteks:** user mau tambah sumber proxy publik GRATIS (ip:port, tanpa auth, tanpa daftar akun) — gaya proxyscrape free list. TIDAK pakai GitHub raw list (user bilang sering basi/tidak valid).

**Klarifikasi penting (sudah dijelaskan ke user):** proxy gratis publik = SELALU `ip:port` polos, TIDAK ada auth. Auth (`user:pass`) = penanda proxy berbayar/akun. "Gratis + auth + tanpa akun" secara desain mustahil. Semua sumber di bawah = datacenter/publik → per routing Fase 3, otomatis cuma dipakai direct/social, BUKAN google/bing.

**File diubah:**
- `config/config.yaml` — `sources:` diisi 4 endpoint API (hapus `sources: []` duplikat lama):
  - `api.proxyscrape.com/v4/free-proxy-list/get?...` (~284)
  - `proxylist.geonode.com/api/proxy-list?...` (~500)
  - `hproxy.com/api/proxy-list?...` (~2700)
  - `freeproxydb.com/api/proxy/subscribe?...` (~50)
- `internal/proxy/scraper.go` — **FIX BUG**: `jsonPortRe` dulu `"port"\s*:\s*(\d+)` cuma match port angka. Geonode kirim port STRING (`"port":"8118"`) → geonode selalu 0 proxy. Diperbaiki jadi `"port"\s*:\s*"?(\d+)"?` (quote opsional). Setelah fix: geonode 0 → 500.

**Verifikasi live (dijalankan, PASS):** keempat sumber total ~3567 proxy mentah/refresh (sebelum health-check). `go build ./...` + `go vet ./internal/proxy/` OK.

**Verifikasi ulang 2026-09-11 (run penuh via `scripts/run.sh`) — angka aktual:**

| Source | Proxy mentah |
|---|---|
| hproxy.com | 3575 |
| proxylist.geonode.com | 500 |
| api.proxyscrape.com v4 | 240 |
| freeproxydb.com | 50 |
| webshare.io key#0 | 10 |
| webshare.io key#1 | 10 |
| **total unik** | **4166** |
| **lolos health-check** | **702 (16,8%)** |

- `sources: 6 ok / 0 failed` — nol sumber gagal.
- Fix `jsonPortRe` TERBUKTI: geonode menghasilkan 500 (bukan 0 seperti bug lama).
- Ternyata ada **2** Webshare API key aktif di `.env`, bukan 1 seperti asumsi plan lama.

**Temuan baru yang belum ada di plan manapun:**
1. **Health-check 4166 proxy makan 14 menit 25 detik** (16:48:05 → 17:02:30) dengan
   `concurrency=100` + `health_check_timeout=8`. Ini bottleneck startup nyata: orchestrator
   tidak mengirim satu task pun selama 14 menit pertama.
2. **303 proxy kena `ip-api lookup failed → fallback provider`** + 13 kasus
   `providers disagree`. ip-api.com rate-limit di 45 req/menit sementara kita menghantam
   ribuan lookup — jadi mayoritas kegagalan geo ini kemungkinan besar rate-limit,
   BUKAN proxy yang jelek. Artinya country/timezone sebagian proxy berasal dari
   fallback ipwho.is yang akurasinya lebih rendah.

Temuan #1 adalah justifikasi langsung untuk fitur health-check cache
(`last_health_check_at` + `health_check_max_age_minutes`) yang dibahas terpisah —
tanpa cache, tiap refresh mengulang 14 menit penuh dari nol.

**Catatan realistis:** dari ~3567 mentah, yang lolos health-check jauh lebih sedikit (proxy publik mayoritas mati) — normal, tersaring otomatis oleh `CheckAll`. Ini cuma nambah volume direct/social; Google tetap butuh residential ber-auth.

---

## Fase 6 — Commit + Uji Live 🟡 SEBAGIAN

### Commit ✅ SUDAH (tapi tidak granular)

Semua Fase 1–5 & 7 masuk ke SATU commit: **`6fd6335`**
`feat: v2 multi-engine anti-detection + per-proxy routing`

Saran 5-commit-granular di bawah ini **tidak jadi dipakai** — disimpan sebagai catatan sejarah saja:
1. ~~`feat(stealth): seed fingerprint deterministically per identity`~~
2. ~~`feat(proxy): detect datacenter IPs and route engines by proxy type`~~
3. ~~`feat(proxy): run residential proxies alongside Webshare datacenter`~~
4. ~~`fix(proxy): parse quoted JSON port (geonode) + add free proxy sources`~~
5. ~~`docs: rewrite README to match current code`~~

### Push ❌ BELUM

`origin/v2` masih di `54a9115`. Commit `6fd6335` ada di lokal saja.
Branch `v2` dan `single-engine` identik — `single-engine` belum berisi kode apa pun,
baru ada dokumen plannya (`2026-09-10-single-engine-simplify.md`).

### Uji live 🟡 SEBAGIAN (2026-09-11)

Dijalankan `./scripts/run.sh` lalu **dihentikan user di tengah** sebelum ada task tuntas.

Yang SUDAH terbukti dari run itu:
- ✅ Scrape 6 sumber jalan, 4166 unik → 702 sehat (lihat Fase 7)
- ✅ Worker gRPC listen :50051, SB session sukses dibuat
  (contoh: `UA=iPhone... tz=Asia/Hong_Kong, headless=True`)
- ✅ **Routing Fase 3 terbukti live**: `.env` TIDAK punya `RESIDENTIAL_*` sama sekali,
  jadi 702 proxy semuanya datacenter → log hanya menunjukkan
  `Executing DIRECT traffic flow`, NOL task dikirim ke google/bing.
  Ini persis perilaku yang didesain, bukan bug.
- ✅ Nol `/sorry/`, nol "automated queries" — konsekuensi wajar karena memang tidak
  ada task yang menyentuh search engine.

Yang MASIH BELUM diuji:
- ❌ Jalur google dengan proxy residential (butuh `RESIDENTIAL_HOST`/`USER` diisi — saat ini kosong)
- ❌ Task selesai end-to-end sampai lapor `serp_position` / dwell
- ❌ Perilaku anti-captcha di kondisi nyata

**Blocker uji sisa:** tanpa kredensial residential, jalur google memang mustahil diuji.
Ini bukan bug — lihat bagian Risiko.

**Catatan build artifacts:** `bin/orchestrator` & `bin/dashboard` ikut ke-commit di `6fd6335`.
`run.sh` rebuild otomatis dari source, jadi binary tracked itu selalu ketimpa saat run.

---

## Files Changed — isi commit `6fd6335` (vs `54a9115`)

> Dulu blok ini berjudul "git status saat ini". Sudah TIDAK relevan: tree sekarang bersih,
> daftar di bawah adalah isi commit `6fd6335`.

```
 M .env.example
 M README.md
 M config/config.yaml
 M cmd/dashboard/main.go
 M internal/config/config.go
 M internal/orchestrator/orchestrator.go
 M internal/proxy/health.go
 M internal/proxy/manager.go
 M internal/proxy/pool.go
 M internal/proxy/residential.go
 M internal/proxy/scraper.go
 M internal/scheduler/dynamic.go
 M worker/browser/profiles.py
 M worker/browser/session.py
 M worker/browser/stealth.py
 M bin/dashboard          (rebuild artifact)
 M bin/orchestrator       (rebuild artifact)
```

## Verifikasi yang sudah lulus
- `go build ./...` OK — diulang 2026-09-11, tetap exit 0
- `go vet ./internal/...` OK — diulang 2026-09-11, tetap exit 0
- Python: determinisme fingerprint per-IP OK (dicek langsung via venv)
- Unit test sementara (dibuat lalu dihapus): 5 skenario routing + residential-gen PASS
- Live 2026-09-11: scrape 6/6 sumber OK, 4166→702 sehat, routing datacenter→direct terbukti

## Sisa pekerjaan Fase 6 (checklist eksekusi)
- [ ] `git push origin v2` (dan/atau `single-engine`) — belum pernah di-push
- [ ] Isi `RESIDENTIAL_HOST` / `RESIDENTIAL_PORT` / `RESIDENTIAL_USER` / `RESIDENTIAL_PASSWORD` di `.env`
- [ ] Run ulang, biarkan sampai ≥1 task google tuntas, cek `serp_position` terisi
- [ ] Pastikan nol `_is_doscaptcha` saat jalur google benar-benar aktif

## Risiko / Catatan penting
- **Tanpa proxy residential terisi, engine Google praktis tidak jalan** (semua datacenter → direct/social). Ini konsekuensi desain yang benar, bukan bug. **Terkonfirmasi live 2026-09-11.**
- Warm-profile residential share 1 slot per gateway IP (lihat Fase 5).
- Nilai cooldown/active_hours di `config.yaml` masih MODE TESTING — terverifikasi masih begitu per 2026-09-11: `min_cooldown_seconds: 5`, `max_cooldown_seconds: 15`, `active_hours_start: 0`, `active_hours_end: 24`, `max_search_per_proxy: 5`. Untuk produksi naikkan (30–120s, jam wajar) agar pola manusiawi.
- **Startup lambat:** health-check penuh makan ~14,5 menit sebelum task pertama jalan (lihat Fase 7 temuan #1).
- **Akurasi geo menurun di skala besar:** ip-api rate-limit → 303 lookup jatuh ke fallback (lihat Fase 7 temuan #2).
