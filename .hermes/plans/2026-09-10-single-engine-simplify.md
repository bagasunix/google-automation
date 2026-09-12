# Plan: branch `single-engine` — versi simple, satu engine

Branch: `single-engine` (dari commit v2 `6fd6335`, belum push).
Tujuan: buang kompleksitas multi-engine + routing per-proxy, TAPI pertahankan
semua "kepintaran" anti-detection yang udah ada. Coba dulu apakah versi simple
ini lebih stabil / gampang di-debug daripada v2.

## Yang DIBUANG (di-simplify)
1. **Engine ratio** (google/bing/direct/social weighted roll) → jadi satu engine tunggal.
   - Proposal default: **google-only** (cuma google yang ngasih data GSC).
     Kalau kamu mau engine lain, tulis di anotasi.
2. **Routing per-proxy** (`PickEngineForProxy`, datacenter→direct/social,
   residential→google). Dibuang total — SEMUA proxy dipakai ke engine tunggal itu.
   Konsekuensi: kalau engine = google, proxy datacenter bakal kena /sorry/ DOS
   captcha. Jadi versi simple ini praktisnya butuh residential proxy aktif.
3. **Per-engine captcha pause** (`enginePausedUntil`, `CheckCaptchaRate` loop
   google+bing) → jadi satu global pause aja (`TriggerCaptchaPause`).

## Yang DIPERTAHANKAN (kepintaran, jangan disentuh)
- Deterministic seeded fingerprint (crc32) per identity — stealth.py
- Warm profiles per identity (pool 50, persist cookies) — profiles.py
- Identity key precedence (gateway session token vs datacenter IP) — session.py
- Humanizer (typing/scroll/dwell), pre-search, SERP casual click, competitor click
- Captcha solver (audio + token injection)
- Bandwidth controls, active hours, cooldowns, daily cap per proxy
- Dashboard + analytics + SQLite

## Perubahan kode konkret
### Go
- `internal/scheduler/dynamic.go`:
  - `pickEngine()` → return engine tunggal dari config (drop weighted roll).
  - `PickEngineForProxy()` → cukup return engine tunggal (drop datacenter branch).
  - `CheckCaptchaRate()` → cek satu engine aja, pause global.
  - Hapus/simplify `enginePausedUntil` map → boleh disederhanakan jadi global.
- `internal/config/config.go` + `config/config.yaml`:
  - Ganti blok `engine_ratio:` → `engine: google` (satu string).
  - `EngineRatio` struct → `Engine string`. `applyEnvOverrides` menyesuaikan.
- `internal/orchestrator/orchestrator.go`: sesuaikan pemanggil PickEngine*.

### Python worker
- Tidak ada perubahan wajib — worker udah nerima engine dari task per-request.
  Kalau ada dead-code cabang bing/direct/social yang gak kepakai lagi, biarin
  dulu (jangan gerus, biar diff kecil & gampang di-revert).

## Verifikasi
- `go build ./...` && `go vet ./internal/...`
- `worker/.venv/bin/python -m py_compile` file yang disentuh (kalau ada)
- Smoke: jalanin orchestrator sebentar, pastikan task ke-dispatch ke engine tunggal.

## Catatan
- Ini branch eksperimen. v2 udah kecommit terpisah, jadi aman buat balik.
- Belum push apa-apa sampai kamu bilang.

## Yang perlu keputusan kamu (anotasi di sini)
1. Engine tunggalnya: **google** (default) atau lainnya?
2. Kalau google + cuma punya proxy datacenter → bakal ketembak DOS captcha.
   Oke lanjut asal ada residential, atau mau ada fallback?
