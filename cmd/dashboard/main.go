// dashboard generates and serves the full Google Automation Control Panel (Dashboard v3).
package main

import (
	"bufio"
	"bytes"
	"crypto/rand"
	"crypto/subtle"
	"database/sql"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"flag"
	"fmt"
	"html/template"
	"io"
	"log"
	"math"
	"mime/multipart"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"

	"google-automation/internal/orchestrator"
	"google-automation/internal/proxy"
	_ "modernc.org/sqlite"
	"gopkg.in/yaml.v3"
)

type DailyRow struct {
	Date            string  `json:"date"`
	TotalSearch     int     `json:"total_search"`
	Success         int     `json:"success"`
	Fail            int     `json:"fail"`
	Captcha         int     `json:"captcha"`
	AvgDwellSeconds float64 `json:"avg_dwell_seconds"`
	AvgSerpPosition float64 `json:"avg_serp_position"`
	SuccessRate     float64 `json:"success_rate"`
	CaptchaRate     float64 `json:"captcha_rate"`
}

type ProxyRow struct {
	IP              string  `json:"ip"`
	Port            int     `json:"port"`
	Country         string  `json:"country"`
	Username        string  `json:"username"`
	APIKeyIndex     int     `json:"api_key_index"`
	AccountLabel    string  `json:"account_label"`
	Used            int     `json:"used"`
	Success         int     `json:"success"`
	Fail            int     `json:"fail"`
	Captcha         int     `json:"captcha"`
	SuccessRate     float64 `json:"success_rate"`
	Blacklisted     bool    `json:"blacklisted"`
	BlacklistReason string  `json:"blacklist_reason"`
	StatusBadge     string  `json:"status_badge"`
}

type ArticleRow struct {
	ID           int64  `json:"id"`
	Title        string `json:"title"`
	URL          string `json:"url"`
	Searched     int    `json:"searched"`
	SerpPosition string `json:"serp_position"`
	LastSearched string `json:"last_searched"`
}

type ReportData struct {
	Generated        string
	Daily            []DailyRow
	Proxies          []ProxyRow
	Articles         []ArticleRow
	Totals           DailyRow
	Concurrency      int
	BandwidthUsedMB  float64
	BandwidthLimitMB int
	BandwidthPercent float64
	BandwidthStatus  string
}

// Session store
var (
	sessionMu      sync.RWMutex
	activeSessions = make(map[string]time.Time)
)

// errNoCredentials is returned when auth is on but nothing configured it.
// Callers must treat this as "deny", never as "use a default".
var errNoCredentials = fmt.Errorf(
	"dashboard auth is enabled but DASHBOARD_USERNAME/DASHBOARD_PASSWORD are not set " +
		"(checked environment and %s) — refusing to serve with built-in credentials",
	"the resolved .env file")

// resolveEnvPath locates the .env file without depending on the working
// directory. Under systemd without an explicit WorkingDirectory= the CWD is /,
// so a bare ".env" silently resolves to nothing; that used to mean the
// dashboard fell back to its built-in credentials while looking healthy.
// DASHBOARD_ENV_FILE overrides everything for deployments that keep it
// elsewhere.
func resolveEnvPath() string {
	if p := os.Getenv("DASHBOARD_ENV_FILE"); p != "" {
		return p
	}
	if _, err := os.Stat(".env"); err == nil {
		return ".env"
	}
	if exe, err := os.Executable(); err == nil {
		dir := filepath.Dir(exe)
		// The binary normally sits in bin/ under the project root, so check
		// beside it and one level up.
		for _, cand := range []string{
			filepath.Join(dir, ".env"),
			filepath.Join(filepath.Dir(dir), ".env"),
		} {
			if _, err := os.Stat(cand); err == nil {
				return cand
			}
		}
	}
	return ".env"
}

// getAuthConfig resolves the dashboard credentials.
//
// It deliberately has NO built-in fallback. It previously defaulted to
// admin/<a password that is present in this repository's public git history>,
// so any deployment where .env was missing — see resolveEnvPath — accepted a
// login that anyone reading the repo already knew. When auth is enabled and
// no credentials are configured this now returns errNoCredentials and every
// caller denies access.
func getAuthConfig() (enabled bool, user, pass string, err error) {
	enabled = true

	// config.yaml may still set `enabled` (not a secret, fine to version
	// control) but username/password are no longer read from it — .env is
	// the single source of truth for credentials, checked below.
	if data, err := os.ReadFile("config/config.yaml"); err == nil {
		var cfg struct {
			Auth struct {
				Enabled *bool `yaml:"enabled"`
			} `yaml:"auth"`
		}
		if err := yaml.Unmarshal(data, &cfg); err == nil && cfg.Auth.Enabled != nil {
			enabled = *cfg.Auth.Enabled
		}
	}

	envMap := loadEnvFile(resolveEnvPath())
	user = firstNonEmpty(os.Getenv("DASHBOARD_USERNAME"), envMap["DASHBOARD_USERNAME"])
	pass = firstNonEmpty(os.Getenv("DASHBOARD_PASSWORD"), envMap["DASHBOARD_PASSWORD"])

	if enabled && (user == "" || pass == "") {
		return enabled, "", "", errNoCredentials
	}
	return enabled, user, pass, nil
}

func firstNonEmpty(vals ...string) string {
	for _, v := range vals {
		if v != "" {
			return v
		}
	}
	return ""
}

func createSession() string {
	b := make([]byte, 24)
	_, _ = rand.Read(b)
	token := hex.EncodeToString(b)
	sessionMu.Lock()
	activeSessions[token] = time.Now().Add(7 * 24 * time.Hour)
	sessionMu.Unlock()
	return token
}

func isValidSession(token string) bool {
	if token == "" {
		return false
	}
	sessionMu.RLock()
	exp, ok := activeSessions[token]
	sessionMu.RUnlock()
	if !ok {
		return false
	}
	if time.Now().After(exp) {
		sessionMu.Lock()
		delete(activeSessions, token)
		sessionMu.Unlock()
		return false
	}
	return true
}

func getClientIP(r *http.Request) string {
	if xff := r.Header.Get("X-Forwarded-For"); xff != "" {
		parts := strings.Split(xff, ",")
		return strings.TrimSpace(parts[0])
	}
	if xri := r.Header.Get("X-Real-IP"); xri != "" {
		return strings.TrimSpace(xri)
	}
	ip, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		return r.RemoteAddr
	}
	return ip
}

type RateLimiter struct {
	mu          sync.Mutex
	loginFails  map[string][]time.Time
	apiRequests map[string][]time.Time
}

var globalLimiter = &RateLimiter{
	loginFails:  make(map[string][]time.Time),
	apiRequests: make(map[string][]time.Time),
}

func (rl *RateLimiter) AllowLoginAttempt(ip string) (bool, int) {
	rl.mu.Lock()
	defer rl.mu.Unlock()

	now := time.Now()
	cutoff := now.Add(-5 * time.Minute)

	var valid []time.Time
	for _, t := range rl.loginFails[ip] {
		if t.After(cutoff) {
			valid = append(valid, t)
		}
	}
	rl.loginFails[ip] = valid

	if len(valid) >= 5 {
		remaining := int(valid[0].Add(5 * time.Minute).Sub(now).Seconds())
		if remaining < 0 {
			remaining = 0
		}
		return false, remaining
	}
	return true, 0
}

func (rl *RateLimiter) RecordLoginFailure(ip string) {
	rl.mu.Lock()
	defer rl.mu.Unlock()
	rl.loginFails[ip] = append(rl.loginFails[ip], time.Now())
}

func (rl *RateLimiter) ClearLoginFailures(ip string) {
	rl.mu.Lock()
	defer rl.mu.Unlock()
	delete(rl.loginFails, ip)
}

func (rl *RateLimiter) AllowAPI(ip string, maxReq int, window time.Duration) bool {
	rl.mu.Lock()
	defer rl.mu.Unlock()

	now := time.Now()
	cutoff := now.Add(-window)

	var valid []time.Time
	for _, t := range rl.apiRequests[ip] {
		if t.After(cutoff) {
			valid = append(valid, t)
		}
	}
	if len(valid) >= maxReq {
		rl.apiRequests[ip] = valid
		return false
	}
	valid = append(valid, now)
	rl.apiRequests[ip] = valid
	return true
}

func recoveryMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		defer func() {
			if rec := recover(); rec != nil {
				stack := make([]byte, 4096)
				n := runtime.Stack(stack, false)
				log.Printf("🔥 [PANIC RECOVERED] %v\nStack:\n%s", rec, stack[:n])

				if strings.HasPrefix(r.URL.Path, "/api/") {
					w.Header().Set("Content-Type", "application/json")
					w.WriteHeader(http.StatusInternalServerError)
					fmt.Fprintf(w, `{"error":"Internal server error recovered","details":"%v"}`, rec)
				} else {
					w.Header().Set("Content-Type", "text/html; charset=utf-8")
					w.WriteHeader(http.StatusInternalServerError)
					fmt.Fprintf(w, `<!DOCTYPE html><html><body style="background:#0b0f19;color:#f8fafc;font-family:sans-serif;padding:40px;text-align:center;"><h2>⚠️ 500 Internal Server Error</h2><p style="color:#94a3b8;">A panic occurred but the server was safely recovered.</p><p><a href="/" style="color:#6366f1;">Back to Dashboard</a></p></body></html>`)
				}
			}
		}()
		next.ServeHTTP(w, r)
	})
}

func requireAuth(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		ip := getClientIP(r)
		if strings.HasPrefix(r.URL.Path, "/api/") {
			if !globalLimiter.AllowAPI(ip, 120, time.Minute) {
				w.Header().Set("Content-Type", "application/json")
				w.WriteHeader(http.StatusTooManyRequests)
				fmt.Fprintf(w, `{"error":"Rate limit exceeded. Max 120 requests/minute."}`)
				return
			}
		}

		enabled, _, _, authErr := getAuthConfig()
		if authErr != nil {
			// Misconfigured, not unauthenticated: deny everything rather
			// than fall through to a default login.
			log.Printf("SECURITY: %v", authErr)
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusServiceUnavailable)
			fmt.Fprintf(w, `{"error":"Dashboard auth is not configured"}`)
			return
		}
		if !enabled {
			next(w, r)
			return
		}

		cookie, err := r.Cookie("admin_session")
		if err != nil || !isValidSession(cookie.Value) {
			if strings.HasPrefix(r.URL.Path, "/api/") {
				w.Header().Set("Content-Type", "application/json")
				w.WriteHeader(http.StatusUnauthorized)
				fmt.Fprintf(w, `{"error":"Unauthorized"}`)
				return
			}
			http.Redirect(w, r, "/login", http.StatusFound)
			return
		}
		next(w, r)
	}
}

type FleetController struct {
	mu        sync.Mutex
	workerCmd *exec.Cmd
	orchCmd   *exec.Cmd
	running   bool
	startedAt time.Time
	workerPID int
	orchPID   int
}

var globalFleet = &FleetController{}

func isProcessAlive(pid int) bool {
	if pid <= 0 {
		return false
	}
	proc, err := os.FindProcess(pid)
	if err != nil {
		return false
	}
	err = proc.Signal(syscall.Signal(0))
	return err == nil
}

func readPID(file string) int {
	data, err := os.ReadFile(file)
	if err != nil {
		return 0
	}
	pid, _ := strconv.Atoi(strings.TrimSpace(string(data)))
	return pid
}

func (f *FleetController) IsRunning() (bool, string, int, int) {
	f.mu.Lock()
	defer f.mu.Unlock()

	wPid := readPID(".worker.pid")
	oPid := readPID(".orchestrator.pid")
	if (wPid > 0 && isProcessAlive(wPid)) || (oPid > 0 && isProcessAlive(oPid)) {
		f.running = true
		f.workerPID = wPid
		f.orchPID = oPid
		return true, "running", f.workerPID, f.orchPID
	}
	f.running = false
	return false, "idle", 0, 0
}

func streamLogsToFleetManager(r io.Reader, fm *orchestrator.FleetManager, workerID int, prefix string) {
	if r == nil {
		return
	}
	scanner := bufio.NewScanner(r)
	for scanner.Scan() {
		line := scanner.Text()
		if strings.TrimSpace(line) == "" {
			continue
		}
		status := "INFO"
		lower := strings.ToLower(line)
		if strings.Contains(lower, "error") || strings.Contains(lower, "fail") {
			status = "ERROR"
		} else if strings.Contains(lower, "success") || strings.Contains(lower, "solved") || strings.Contains(lower, "ready") {
			status = "SUCCESS"
		}
		fm.Log(workerID, prefix, status, line)
	}
}

func (f *FleetController) Start(fm *orchestrator.FleetManager) error {
	f.mu.Lock()
	defer f.mu.Unlock()

	wPid := readPID(".worker.pid")
	oPid := readPID(".orchestrator.pid")
	if (wPid > 0 && isProcessAlive(wPid)) || (oPid > 0 && isProcessAlive(oPid)) {
		f.running = true
		return fmt.Errorf("bot fleet is already running (Worker PID: %d, Orch PID: %d)", wPid, oPid)
	}

	// 1. Start Python worker
	absDir, _ := os.Getwd()
	workerDir := filepath.Join(absDir, "worker")
	pythonBin := filepath.Join(workerDir, ".venv", "bin", "python")
	if _, err := os.Stat(pythonBin); os.IsNotExist(err) {
		pythonBin = "python3"
	}
	workerCmd := exec.Command(pythonBin, "main.py")
	workerCmd.Dir = workerDir

	workerStdout, _ := workerCmd.StdoutPipe()
	workerStderr, _ := workerCmd.StderrPipe()

	if err := workerCmd.Start(); err != nil {
		return fmt.Errorf("failed to start Python worker: %v", err)
	}
	f.workerCmd = workerCmd
	f.workerPID = workerCmd.Process.Pid
	_ = os.WriteFile(filepath.Join(absDir, ".worker.pid"), []byte(strconv.Itoa(f.workerPID)), 0644)
	fm.Log(1, "WORKER", "INFO", fmt.Sprintf("Python Worker started (PID: %d) listening on gRPC :50051", f.workerPID))

	go streamLogsToFleetManager(workerStdout, fm, 1, "WORKER")
	go streamLogsToFleetManager(workerStderr, fm, 1, "WORKER")

	// Reap the child on exit — without this, a crashed worker sits as a
	// zombie until the dashboard process itself exits, and IsRunning() keeps
	// reporting it as alive since the zombie PID still responds to kill(0).
	go func(cmd *exec.Cmd, pid int, pidFile string) {
		err := cmd.Wait()
		if err != nil {
			fm.Log(1, "WORKER", "ERROR", fmt.Sprintf("Python Worker (PID: %d) exited: %v", pid, err))
		} else {
			fm.Log(1, "WORKER", "WARN", fmt.Sprintf("Python Worker (PID: %d) exited.", pid))
		}
		if readPID(pidFile) == pid {
			_ = os.Remove(pidFile)
		}
	}(workerCmd, f.workerPID, filepath.Join(absDir, ".worker.pid"))

	// Wait 2.5s for worker gRPC server to bind
	time.Sleep(2500 * time.Millisecond)

	// 2. Start Go orchestrator
	orchBin := filepath.Join(absDir, "bin", "orchestrator")
	var orchCmd *exec.Cmd
	if _, err := os.Stat(orchBin); err == nil {
		orchCmd = exec.Command(orchBin)
	} else {
		goCmd := "go"
		if _, err := os.Stat("/home/user/go-sdk/go/bin/go"); err == nil {
			goCmd = "/home/user/go-sdk/go/bin/go"
		}
		orchCmd = exec.Command(goCmd, "run", "cmd/main.go")
	}
	orchCmd.Dir = absDir

	orchStdout, _ := orchCmd.StdoutPipe()
	orchStderr, _ := orchCmd.StderrPipe()

	if err := orchCmd.Start(); err != nil {
		_ = workerCmd.Process.Kill()
		return fmt.Errorf("failed to start Go orchestrator: %v", err)
	}
	f.orchCmd = orchCmd
	f.orchPID = orchCmd.Process.Pid
	_ = os.WriteFile(filepath.Join(absDir, ".orchestrator.pid"), []byte(strconv.Itoa(f.orchPID)), 0644)
	fm.Log(0, "SYSTEM", "SUCCESS", fmt.Sprintf("Go Orchestrator engine started (PID: %d). Bot fleet is LIVE!", f.orchPID))

	go streamLogsToFleetManager(orchStdout, fm, 0, "ORCH")
	go streamLogsToFleetManager(orchStderr, fm, 0, "ORCH")

	go func(cmd *exec.Cmd, pid int, pidFile string) {
		err := cmd.Wait()
		if err != nil {
			fm.Log(0, "SYSTEM", "ERROR", fmt.Sprintf("Go Orchestrator (PID: %d) exited: %v", pid, err))
		} else {
			fm.Log(0, "SYSTEM", "WARN", fmt.Sprintf("Go Orchestrator (PID: %d) exited.", pid))
		}
		if readPID(pidFile) == pid {
			_ = os.Remove(pidFile)
		}
	}(orchCmd, f.orchPID, filepath.Join(absDir, ".orchestrator.pid"))

	f.running = true
	f.startedAt = time.Now()
	return nil
}

func (f *FleetController) Stop(fm *orchestrator.FleetManager) {
	f.mu.Lock()
	defer f.mu.Unlock()

	stopCmd := exec.Command("./scripts/stop.sh")
	_ = stopCmd.Run()

	if f.orchCmd != nil && f.orchCmd.Process != nil {
		_ = f.orchCmd.Process.Kill()
	}
	if f.workerCmd != nil && f.workerCmd.Process != nil {
		_ = f.workerCmd.Process.Kill()
	}

	_ = os.Remove(".worker.pid")
	_ = os.Remove(".orchestrator.pid")

	f.running = false
	f.workerCmd = nil
	f.orchCmd = nil
	f.workerPID = 0
	f.orchPID = 0

	fm.Log(0, "SYSTEM", "WARN", "Bot Fleet stopped by user via Dashboard Control")
}

func main() {
	dbPath := flag.String("db", "search_automation.db", "path to SQLite DB")
	outPath := flag.String("out", "analytics/dashboard.html", "output HTML file")
	serveAddr := flag.String("serve", "", "run live web server on address (e.g. ':8080' or '0.0.0.0:8080')")
	flag.Parse()

	db, err := sql.Open("sqlite", *dbPath+"?mode=ro")
	if err != nil {
		log.Fatalf("open db: %v", err)
	}
	defer db.Close()

	fm := orchestrator.GetFleetManager(2)
	baseDir, _ := os.Getwd()

	if *serveAddr != "" {
		// Fail fast and loudly instead of coming up on an internet-facing
		// port with no working credentials.
		if _, _, _, err := getAuthConfig(); err != nil {
			log.Fatalf("refusing to start: %v", err)
		}
		log.Printf("🚀 Starting Google Automation Control Panel on http://localhost%s", *serveAddr)

		// Public: Static assets & Favicon
		http.Handle("/static/", http.StripPrefix("/static/", http.FileServer(http.Dir("web/static"))))
		http.HandleFunc("/favicon.ico", func(w http.ResponseWriter, r *http.Request) {
			http.ServeFile(w, r, "web/static/favicon.png")
		})

		// Public: PWA endpoints
		http.HandleFunc("/manifest.json", func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Type", "application/json")
			fmt.Fprint(w, `{"name":"Google Automation Dashboard","short_name":"GAutoDash","start_url":"/","display":"standalone","background_color":"#0b0f19","theme_color":"#0b0f19","icons":[{"src":"/static/pwa-icon.jpg","sizes":"192x192","type":"image/jpeg"},{"src":"/static/pwa-icon.jpg","sizes":"512x512","type":"image/jpeg"}]}`)
		})
		http.HandleFunc("/sw.js", func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Type", "application/javascript")
			fmt.Fprint(w, `const CACHE_NAME = 'gautodash-v1';
const urlsToCache = ['/static/favicon.png', '/static/pwa-icon.jpg'];
self.addEventListener('install', event => {
  event.waitUntil(caches.open(CACHE_NAME).then(cache => cache.addAll(urlsToCache)));
});
self.addEventListener('fetch', event => {
  if (event.request.method !== 'GET') return;
  event.respondWith(fetch(event.request).catch(() => caches.match(event.request)));
});`)
		})

		// Public: Login Page with Brute-Force Rate Limiting
		http.HandleFunc("/login", func(w http.ResponseWriter, r *http.Request) {
			enabled, validUser, validPass, authErr := getAuthConfig()
			if authErr != nil {
				log.Printf("SECURITY: %v", authErr)
				w.Header().Set("Content-Type", "text/html; charset=utf-8")
				w.WriteHeader(http.StatusServiceUnavailable)
				_ = loginTmpl.Execute(w, map[string]interface{}{
					"Error": "Dashboard credentials are not configured on the server.",
				})
				return
			}
			if !enabled {
				http.Redirect(w, r, "/", http.StatusFound)
				return
			}

			ip := getClientIP(r)

			if r.Method == http.MethodGet {
				if cookie, err := r.Cookie("admin_session"); err == nil && isValidSession(cookie.Value) {
					http.Redirect(w, r, "/", http.StatusFound)
					return
				}
				w.Header().Set("Content-Type", "text/html; charset=utf-8")
				_ = loginTmpl.Execute(w, map[string]interface{}{"Error": ""})
				return
			} else if r.Method == http.MethodPost {
				allowed, waitSec := globalLimiter.AllowLoginAttempt(ip)
				if !allowed {
					w.Header().Set("Content-Type", "text/html; charset=utf-8")
					w.WriteHeader(http.StatusTooManyRequests)
					_ = loginTmpl.Execute(w, map[string]interface{}{
						"Error": fmt.Sprintf("Too many failed attempts. Rate limited for %d seconds.", waitSec),
					})
					return
				}

				user := r.FormValue("username")
				pass := r.FormValue("password")

				// Constant-time so a wrong password cannot be narrowed down
				// by timing the response.
				userOK := subtle.ConstantTimeCompare([]byte(user), []byte(validUser)) == 1
				passOK := subtle.ConstantTimeCompare([]byte(pass), []byte(validPass)) == 1
				if userOK && passOK {
					globalLimiter.ClearLoginFailures(ip)
					token := createSession()
					http.SetCookie(w, &http.Cookie{
						Name:     "admin_session",
						Value:    token,
						Path:     "/",
						HttpOnly: true,
						SameSite: http.SameSiteLaxMode,
						MaxAge:   7 * 24 * 3600,
					})
					fm.Log(0, "SYSTEM", "SUCCESS", fmt.Sprintf("Admin user '%s' authenticated successfully from %s", user, ip))
					http.Redirect(w, r, "/", http.StatusFound)
					return
				}

				globalLimiter.RecordLoginFailure(ip)
				w.Header().Set("Content-Type", "text/html; charset=utf-8")
				w.WriteHeader(http.StatusUnauthorized)
				_ = loginTmpl.Execute(w, map[string]interface{}{"Error": "Invalid username or password"})
			}
		})

		// Logout
		http.HandleFunc("/logout", func(w http.ResponseWriter, r *http.Request) {
			if cookie, err := r.Cookie("admin_session"); err == nil {
				sessionMu.Lock()
				delete(activeSessions, cookie.Value)
				sessionMu.Unlock()
			}
			http.SetCookie(w, &http.Cookie{
				Name:     "admin_session",
				Value:    "",
				Path:     "/",
				HttpOnly: true,
				MaxAge:   -1,
			})
			http.Redirect(w, r, "/login", http.StatusFound)
		})

		// Protected: Dashboard Home
		http.HandleFunc("/", requireAuth(func(w http.ResponseWriter, r *http.Request) {
			usedMB, limitMB, pct, status := getBandwidthStats()
			data := ReportData{
				Generated:        time.Now().Format("2006-01-02 15:04:05"),
				Daily:            queryDaily(db),
				Proxies:          queryProxies(db),
				Articles:         queryArticles(db),
				Concurrency:      fm.GetConcurrency(),
				BandwidthUsedMB:  usedMB,
				BandwidthLimitMB: limitMB,
				BandwidthPercent: pct,
				BandwidthStatus:  status,
			}
			data.Totals = computeTotals(data.Daily)

			w.Header().Set("Content-Type", "text/html; charset=utf-8")
			var buf bytes.Buffer
			if err := tmpl.Execute(&buf, data); err != nil {
				http.Error(w, err.Error(), http.StatusInternalServerError)
				return
			}
			w.Write(buf.Bytes())
		}))

		// Protected API: Fleet Live Status.
		//
		// The orchestrator loop that actually updates worker state runs in a
		// different OS process than this dashboard (spawned subprocess,
		// independently launched binary, or systemd service) — its
		// FleetManager singleton lives in ITS memory, not ours. So prefer the
		// on-disk snapshot that process mirrors its state to; only fall back
		// to our own (always-idle) in-process FleetManager when no fresh
		// snapshot exists, i.e. nothing is actually running the fleet.
		http.HandleFunc("/api/fleet/status", requireAuth(func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Type", "application/json")

			const staleAfter = 15 * time.Second
			if snap, err := orchestrator.LoadFleetSnapshot(baseDir); err == nil && time.Since(snap.UpdatedAt) < staleAfter {
				json.NewEncoder(w).Encode(map[string]interface{}{
					"concurrency": snap.Concurrency,
					"workers":     snap.Workers,
					"source_pid":  snap.PID,
				})
				return
			}

			json.NewEncoder(w).Encode(map[string]interface{}{
				"concurrency": fm.GetConcurrency(),
				"workers":     fm.GetFleetStatus(),
			})
		}))

		// Protected API: Concurrency update
		http.HandleFunc("/api/fleet/concurrency", requireAuth(func(w http.ResponseWriter, r *http.Request) {
			if r.Method != http.MethodPost {
				http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
				return
			}
			var req struct {
				Concurrency int `json:"concurrency"`
			}
			applied := fm.GetConcurrency()
			if err := json.NewDecoder(r.Body).Decode(&req); err == nil && req.Concurrency > 0 {
				fm.SetConcurrency(req.Concurrency) // keeps this process's own copy in sync too
				if err := orchestrator.WriteFleetControl(baseDir, req.Concurrency); err != nil {
					log.Printf("write fleet control: %v", err)
				}
				applied = fm.GetConcurrency()
			}
			w.Header().Set("Content-Type", "application/json")
			// The real orchestrator process (if running separately) polls
			// fleet_control.json every few seconds and applies it — the grid
			// will reflect the actual value shortly after this response.
			fmt.Fprintf(w, `{"status":"ok","concurrency":%d}`, applied)
		}))

		// API: Logs stream
		http.HandleFunc("/api/logs", requireAuth(func(w http.ResponseWriter, r *http.Request) {
			sinceStr := r.URL.Query().Get("since")
			sinceID, _ := strconv.ParseInt(sinceStr, 10, 64)
			workerStr := r.URL.Query().Get("worker")
			workerID, _ := strconv.Atoi(workerStr)

			logs := fm.GetLogs(sinceID, workerID)
			w.Header().Set("Content-Type", "application/json")
			json.NewEncoder(w).Encode(logs)
		}))

		// API: Fleet Engine Control (Start / Stop / Status)
		http.HandleFunc("/api/fleet/start", requireAuth(func(w http.ResponseWriter, r *http.Request) {
			if r.Method != http.MethodPost {
				http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
				return
			}
			err := globalFleet.Start(fm)
			w.Header().Set("Content-Type", "application/json")
			if err != nil {
				json.NewEncoder(w).Encode(map[string]interface{}{
					"status":  "error",
					"message": err.Error(),
				})
				return
			}
			json.NewEncoder(w).Encode(map[string]interface{}{
				"status":  "ok",
				"message": "Bot Fleet Engine launched successfully! Orchestrator & Worker are active.",
			})
		}))

		http.HandleFunc("/api/fleet/stop", requireAuth(func(w http.ResponseWriter, r *http.Request) {
			if r.Method != http.MethodPost {
				http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
				return
			}
			globalFleet.Stop(fm)
			w.Header().Set("Content-Type", "application/json")
			json.NewEncoder(w).Encode(map[string]interface{}{
				"status":  "ok",
				"message": "Bot Fleet Engine stopped successfully.",
			})
		}))

		http.HandleFunc("/api/fleet/engine-status", requireAuth(func(w http.ResponseWriter, r *http.Request) {
			running, status, wPid, oPid := globalFleet.IsRunning()
			w.Header().Set("Content-Type", "application/json")
			json.NewEncoder(w).Encode(map[string]interface{}{
				"running":          running,
				"status":           status,
				"worker_pid":       wPid,
				"orchestrator_pid": oPid,
			})
		}))

		// API: Trigger Search for article
		http.HandleFunc("/api/action/trigger_search", requireAuth(func(w http.ResponseWriter, r *http.Request) {
			if r.Method != http.MethodPost {
				http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
				return
			}
			var req struct {
				ArticleID int64 `json:"article_id"`
			}
			if err := json.NewDecoder(r.Body).Decode(&req); err == nil && req.ArticleID > 0 {
				fm.EnqueueManualArticle(req.ArticleID)
				w.Header().Set("Content-Type", "application/json")
				fmt.Fprintf(w, `{"status":"ok","message":"Article prioritized"}`)
				return
			}
			http.Error(w, "Invalid article ID", http.StatusBadRequest)
		}))

		// API: Reset Analytics Stats
		http.HandleFunc("/api/action/reset-stats", requireAuth(func(w http.ResponseWriter, r *http.Request) {
			if r.Method != http.MethodPost {
				http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
				return
			}
			_, _ = db.Exec("DELETE FROM daily_stats")
			_, _ = db.Exec("DELETE FROM tasks")
			_, _ = db.Exec("UPDATE articles SET searched_count=0, last_searched_at=NULL, first_searched_at=NULL")
			_, _ = db.Exec("UPDATE proxies SET used_count=0, last_used_at=NULL")
			fm.Log(0, "SYSTEM", "WARN", "Analytics stats & historical tasks reset to 0 by operator")
			w.Header().Set("Content-Type", "application/json")
			fmt.Fprintf(w, `{"status":"ok","message":"All analytics stats and task histories have been reset to 0."}`)
		}))

		// API: Export CSV
		http.HandleFunc("/api/export/csv", requireAuth(func(w http.ResponseWriter, r *http.Request) {
			daily := queryDaily(db)
			w.Header().Set("Content-Type", "text/csv")
			w.Header().Set("Content-Disposition", "attachment; filename=\"search_automation_report.csv\"")
			fmt.Fprintf(w, "Date,TotalSearches,Success,Fail,CAPTCHA,SuccessRatePercent,AvgDwellSeconds,AvgSerpPosition\n")
			for _, d := range daily {
				fmt.Fprintf(w, "%s,%d,%d,%d,%d,%.2f,%.2f,%.2f\n",
					d.Date, d.TotalSearch, d.Success, d.Fail, d.Captcha, d.SuccessRate, d.AvgDwellSeconds, d.AvgSerpPosition)
			}
		}))

		// API: Bulk Proxy Import & Database Ingestion
		http.HandleFunc("/api/proxies/import", requireAuth(func(w http.ResponseWriter, r *http.Request) {
			if r.Method != http.MethodPost {
				http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
				return
			}
			var req struct {
				ProxyList string `json:"proxy_list"`
				Mode      string `json:"mode"` // "append" or "replace"
			}
			if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
				http.Error(w, err.Error(), http.StatusBadRequest)
				return
			}
			parsed := proxy.ParseProxyString(req.ProxyList)
			if len(parsed) == 0 {
				http.Error(w, "No valid proxies found in input", http.StatusBadRequest)
				return
			}

			// In-memory deduplication of the incoming batch
			seen := make(map[string]bool)
			var uniqueProxies []proxy.Proxy
			dupCount := 0
			for _, p := range parsed {
				key := fmt.Sprintf("%s:%d", p.IP, p.Port)
				if seen[key] {
					dupCount++
					continue
				}
				seen[key] = true
				uniqueProxies = append(uniqueProxies, p)
			}

			tx, err := db.Begin()
			if err != nil {
				http.Error(w, "Database error: "+err.Error(), http.StatusInternalServerError)
				return
			}
			defer tx.Rollback()

			if req.Mode == "replace" {
				// Deactivate old proxies before inserting fresh batch
				_, _ = tx.Exec("UPDATE proxies SET active=0")
			}

			newCount := 0
			updatedCount := 0

			for _, p := range uniqueProxies {
				var existingID int64
				err := tx.QueryRow("SELECT id FROM proxies WHERE ip=? AND port=?", p.IP, p.Port).Scan(&existingID)
				if err == sql.ErrNoRows {
					_, err := tx.Exec(`
						INSERT INTO proxies (ip, port, protocol, country, timezone, username, password, active, used_count, blacklisted)
						VALUES (?, ?, ?, ?, ?, ?, ?, 1, 0, 0)
					`, p.IP, p.Port, p.Protocol, p.Country, "UTC", p.Username, p.Password)
					if err == nil {
						newCount++
					}
				} else if err == nil {
					_, err := tx.Exec(`
						UPDATE proxies
						SET protocol=?, country=?, username=?, password=?, active=1, blacklisted=0
						WHERE id=?
					`, p.Protocol, p.Country, p.Username, p.Password, existingID)
					if err == nil {
						updatedCount++
					}
				}
			}

			if err := tx.Commit(); err != nil {
				http.Error(w, "Database commit failed: "+err.Error(), http.StatusInternalServerError)
				return
			}

			// Export clean, deduplicated proxies from DB to data/proxies.txt
			_ = os.MkdirAll("data", 0755)
			var outLines []string
			rows, err := db.Query("SELECT ip, port, username, password FROM proxies WHERE active=1 AND blacklisted=0")
			if err == nil {
				for rows.Next() {
					var ip, u, pw string
					var port int
					if err := rows.Scan(&ip, &port, &u, &pw); err == nil {
						if u != "" && pw != "" {
							outLines = append(outLines, fmt.Sprintf("%s:%d:%s:%s", ip, port, u, pw))
						} else {
							outLines = append(outLines, fmt.Sprintf("%s:%d", ip, port))
						}
					}
				}
				rows.Close()
				_ = os.WriteFile("data/proxies.txt", []byte(strings.Join(outLines, "\n")), 0644)
			}

			msg := fmt.Sprintf("Processed %d proxies: %d new added, %d updated, %d duplicates skipped",
				len(parsed), newCount, updatedCount, dupCount)
			fm.Log(0, "SYSTEM", "SUCCESS", msg)

			w.Header().Set("Content-Type", "application/json")
			json.NewEncoder(w).Encode(map[string]interface{}{
				"status":          "ok",
				"total_processed": len(parsed),
				"new_added":       newCount,
				"updated":         updatedCount,
				"duplicates":      dupCount,
				"message":         msg,
			})
		}))

		// API: Visual Structured Config (JSON)
		http.HandleFunc("/api/config/json", requireAuth(func(w http.ResponseWriter, r *http.Request) {
			configPath := "config/config.yaml"
			if r.Method == http.MethodGet {
				data, err := os.ReadFile(configPath)
				if err != nil {
					http.Error(w, err.Error(), http.StatusInternalServerError)
					return
				}
				var cfg map[string]interface{}
				_ = yaml.Unmarshal(data, &cfg)
				if cfg == nil {
					cfg = make(map[string]interface{})
				}

				envMap := loadEnvFile(".env")

				// Enrich captcha.openai_api_key if empty
				if c, ok := cfg["captcha"].(map[string]interface{}); ok {
					if k, _ := c["openai_api_key"].(string); k == "" {
						if envVal := os.Getenv("OPENAI_API_KEY"); envVal != "" {
							c["openai_api_key"] = envVal
						} else if envVal := os.Getenv("GROQ_API_KEY"); envVal != "" {
							c["openai_api_key"] = envVal
						} else if envVal := envMap["OPENAI_API_KEY"]; envVal != "" {
							c["openai_api_key"] = envVal
						} else if envVal := envMap["GROQ_API_KEY"]; envVal != "" {
							c["openai_api_key"] = envVal
						}
					}
				}

				// Enrich proxy.webshare_api_keys if empty
				if p, ok := cfg["proxy"].(map[string]interface{}); ok {
					var keysList []string
					if rawKeys, ok := p["webshare_api_keys"].([]interface{}); ok {
						for _, rk := range rawKeys {
							if sk, ok := rk.(string); ok && sk != "" {
								keysList = append(keysList, sk)
							}
						}
					}
					if singleKey, _ := p["webshare_api_key"].(string); singleKey != "" && len(keysList) == 0 {
						keysList = append(keysList, singleKey)
					}
					if len(keysList) == 0 {
						envKeys := os.Getenv("WEBSHARE_API_KEYS")
						if envKeys == "" {
							envKeys = envMap["WEBSHARE_API_KEYS"]
						}
						if envKeys != "" {
							for _, k := range strings.Split(envKeys, ",") {
								k = strings.TrimSpace(k)
								if k != "" {
									keysList = append(keysList, k)
								}
							}
						}
					}
					p["webshare_api_keys"] = keysList
				}

				w.Header().Set("Content-Type", "application/json")
				json.NewEncoder(w).Encode(cfg)
				return
			} else if r.Method == http.MethodPost {
				var incoming map[string]interface{}
				if err := json.NewDecoder(r.Body).Decode(&incoming); err != nil {
					http.Error(w, err.Error(), http.StatusBadRequest)
					return
				}
				existingBytes, _ := os.ReadFile(configPath)
				var existing map[string]interface{}
				_ = yaml.Unmarshal(existingBytes, &existing)
				if existing == nil {
					existing = make(map[string]interface{})
				}
				for k, v := range incoming {
					existing[k] = v
				}
				outYAML, err := yaml.Marshal(existing)
				if err != nil {
					http.Error(w, err.Error(), http.StatusInternalServerError)
					return
				}
				if err := os.WriteFile(configPath, outYAML, 0644); err != nil {
					http.Error(w, err.Error(), http.StatusInternalServerError)
					return
				}
				fm.Log(0, "SYSTEM", "INFO", "Campaign settings updated & applied via Visual Form GUI")
				w.Header().Set("Content-Type", "application/json")
				fmt.Fprintf(w, `{"status":"ok","message":"Settings saved & applied successfully!"}`)
			}
		}))

		// API: Test Audio Model Support & STT Connection
		http.HandleFunc("/api/captcha/test-audio", requireAuth(func(w http.ResponseWriter, r *http.Request) {
			if r.Method != http.MethodPost {
				http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
				return
			}

			var req struct {
				BaseURL  string `json:"base_url"`
				APIKey   string `json:"api_key"`
				Model    string `json:"model"`
				Language string `json:"language"`
			}
			if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
				http.Error(w, err.Error(), http.StatusBadRequest)
				return
			}

			if req.Model == "" {
				req.Model = "whisper-large-v3-turbo"
			}
			if req.BaseURL == "" {
				req.BaseURL = "https://api.groq.com/openai/v1"
			}
			req.BaseURL = strings.TrimRight(req.BaseURL, "/")
			if req.Language == "" {
				req.Language = "en"
			}

			wavBytes, err := os.ReadFile("data/benchmark_sample.wav")
			if err != nil || len(wavBytes) == 0 {
				wavBytes = createBenchmarkWAV()
			}

			var body bytes.Buffer
			writer := multipart.NewWriter(&body)

			part, err := writer.CreateFormFile("file", "benchmark.wav")
			if err != nil {
				http.Error(w, err.Error(), http.StatusInternalServerError)
				return
			}
			_, _ = part.Write(wavBytes)
			_ = writer.WriteField("model", req.Model)
			_ = writer.WriteField("language", req.Language)
			_ = writer.WriteField("response_format", "json")
			_ = writer.Close()

			endpoint := req.BaseURL + "/audio/transcriptions"
			httpReq, err := http.NewRequestWithContext(r.Context(), "POST", endpoint, &body)
			if err != nil {
				http.Error(w, err.Error(), http.StatusInternalServerError)
				return
			}
			httpReq.Header.Set("Content-Type", writer.FormDataContentType())
			if req.APIKey != "" {
				httpReq.Header.Set("Authorization", "Bearer "+req.APIKey)
			}

			client := &http.Client{Timeout: 12 * time.Second}
			start := time.Now()
			resp, err := client.Do(httpReq)
			latency := time.Since(start)

			w.Header().Set("Content-Type", "application/json")
			if err != nil {
				json.NewEncoder(w).Encode(map[string]interface{}{
					"status":     "error",
					"latency_ms": latency.Milliseconds(),
					"supported":  false,
					"message":    fmt.Sprintf("Connection failed to %s: %v", endpoint, err),
				})
				return
			}
			defer resp.Body.Close()

			respBytes, _ := io.ReadAll(resp.Body)
			if resp.StatusCode != http.StatusOK {
				var errObj struct {
					Error struct {
						Message string `json:"message"`
					} `json:"error"`
				}
				errMsg := string(respBytes)
				if json.Unmarshal(respBytes, &errObj) == nil && errObj.Error.Message != "" {
					errMsg = errObj.Error.Message
				}
				json.NewEncoder(w).Encode(map[string]interface{}{
					"status":      "error",
					"status_code": resp.StatusCode,
					"latency_ms":  latency.Milliseconds(),
					"supported":   false,
					"message":     fmt.Sprintf("HTTP %d: %s", resp.StatusCode, errMsg),
				})
				return
			}

			var successObj struct {
				Text string `json:"text"`
			}
			text := string(respBytes)
			if json.Unmarshal(respBytes, &successObj) == nil && successObj.Text != "" {
				text = successObj.Text
			}
			text = strings.TrimSpace(text)

			fm.Log(0, "SYSTEM", "SUCCESS", fmt.Sprintf("Audio STT Model '%s' tested successfully (latency: %dms)", req.Model, latency.Milliseconds()))
			json.NewEncoder(w).Encode(map[string]interface{}{
				"status":           "ok",
				"status_code":      200,
				"latency_ms":       latency.Milliseconds(),
				"supported":        true,
				"transcribed_text": text,
				"model":            req.Model,
				"message":          fmt.Sprintf("✅ Model '%s' verified & fully supports audio transcription! (Latency: %dms)", req.Model, latency.Milliseconds()),
			})
		}))

		if err := http.ListenAndServe(*serveAddr, recoveryMiddleware(http.DefaultServeMux)); err != nil {
			log.Fatalf("http serve: %v", err)
		}
		return
	}

	data := ReportData{
		Generated: time.Now().Format("2006-01-02 15:04:05"),
	}
	data.Daily = queryDaily(db)
	data.Proxies = queryProxies(db)
	data.Articles = queryArticles(db)
	data.Totals = computeTotals(data.Daily)

	f, err := os.Create(*outPath)
	if err != nil {
		log.Fatalf("create %s: %v", *outPath, err)
	}
	defer f.Close()

	if err := tmpl.Execute(f, data); err != nil {
		log.Fatalf("render template: %v", err)
	}
	log.Printf("Dashboard written to %s", *outPath)
}

func queryDaily(db *sql.DB) []DailyRow {
	rows, err := db.Query(`
		SELECT date, total_search, success, fail, captcha,
		       COALESCE(avg_dwell_seconds, 0),
		       COALESCE(avg_serp_position, 0)
		FROM daily_stats
		ORDER BY date DESC
		LIMIT 30`)
	if err != nil {
		log.Printf("query daily_stats: %v", err)
		return nil
	}
	defer rows.Close()

	var out []DailyRow
	for rows.Next() {
		var r DailyRow
		if err := rows.Scan(
			&r.Date, &r.TotalSearch, &r.Success, &r.Fail, &r.Captcha,
			&r.AvgDwellSeconds, &r.AvgSerpPosition,
		); err != nil {
			continue
		}
		if r.TotalSearch > 0 {
			r.SuccessRate = float64(r.Success) / float64(r.TotalSearch) * 100
			r.CaptchaRate = float64(r.Captcha) / float64(r.TotalSearch) * 100
		}
		out = append(out, r)
	}
	return out
}

func queryProxies(db *sql.DB) []ProxyRow {
	rows, err := db.Query(`
		SELECT ip, port, country, COALESCE(username, ''), COALESCE(api_key_index, 0), used_count, blacklisted, COALESCE(blacklist_reason, '')
		FROM proxies
		ORDER BY blacklisted ASC, username ASC, used_count DESC`)
	if err != nil {
		log.Printf("query proxies: %v", err)
		return nil
	}
	defer rows.Close()

	var out []ProxyRow
	var toEnrich []string
	for rows.Next() {
		var r ProxyRow
		var bl int
		if err := rows.Scan(&r.IP, &r.Port, &r.Country, &r.Username, &r.APIKeyIndex, &r.Used, &bl, &r.BlacklistReason); err != nil {
			continue
		}
		r.Blacklisted = bl == 1
		if r.Username != "" {
			// api_key_index is recorded at scrape time (see proxy.Proxy.APIKeyIndex)
			// from the actual WEBSHARE_API_KEYS position — not guessed from the
			// sub-account username, which changes if a key is ever rotated.
			r.AccountLabel = fmt.Sprintf("🔑 Webshare Key #%d", r.APIKeyIndex+1)
		} else {
			r.AccountLabel = "🌐 Public / Direct"
		}

		if r.Blacklisted {
			if strings.Contains(r.BlacklistReason, "402") || strings.Contains(strings.ToLower(r.BlacklistReason), "quota") {
				r.StatusBadge = "⚠️ Quota 402"
			} else if r.BlacklistReason != "" {
				r.StatusBadge = "⛔ " + r.BlacklistReason
			} else {
				r.StatusBadge = "⛔ Blacklisted"
			}
		} else {
			r.StatusBadge = "🟢 Active"
		}

		if r.Country == "" || r.Country == "CUSTOM" {
			r.Country = "Detecting..."
			toEnrich = append(toEnrich, r.IP)
		}
		out = append(out, r)
	}

	if len(toEnrich) > 0 {
		go enrichProxyGeoIP(db, toEnrich)
	}
	return out
}

var enrichMu sync.Mutex
var enrichedMap = make(map[string]bool)

func enrichProxyGeoIP(db *sql.DB, ips []string) {
	enrichMu.Lock()
	var targets []string
	for _, ip := range ips {
		if !enrichedMap[ip] {
			enrichedMap[ip] = true
			targets = append(targets, ip)
		}
	}
	enrichMu.Unlock()

	for _, ip := range targets {
		cc, tz := proxy.DetectGeoIP(ip)
		if cc != "" {
			_, _ = db.Exec("UPDATE proxies SET country=?, timezone=? WHERE ip=?", cc, tz, ip)
		}
		time.Sleep(150 * time.Millisecond) // avoid rate limiting
	}
}

func getBandwidthStats() (float64, int, float64, string) {
	limitMB := 1024
	cfgData, err := os.ReadFile("config/config.yaml")
	if err == nil {
		var cfg struct {
			Bandwidth struct {
				MonthlyLimitMB int `yaml:"monthly_limit_mb"`
			} `yaml:"bandwidth"`
		}
		if err := yaml.Unmarshal(cfgData, &cfg); err == nil && cfg.Bandwidth.MonthlyLimitMB > 0 {
			limitMB = cfg.Bandwidth.MonthlyLimitMB
		}
	}

	// Which keys are actually configured right now (WEBSHARE_API_KEYS) —
	// bandwidth.json can carry leftover entries (e.g. a "key2" from a key
	// removed from the env var long ago) that must never be counted, since
	// they sit frozen forever and silently override the real active keys'
	// usage otherwise (this exact bug previously hardcoded "key2" as the
	// display source, showing a permanently stale, unrelated percentage).
	numActiveKeys := 1
	envKeys := os.Getenv("WEBSHARE_API_KEYS")
	if envKeys == "" {
		envKeys = loadEnvFile(".env")["WEBSHARE_API_KEYS"]
	}
	if envKeys != "" {
		var keys []string
		for _, k := range strings.Split(envKeys, ",") {
			if strings.TrimSpace(k) != "" {
				keys = append(keys, k)
			}
		}
		if len(keys) > 0 {
			numActiveKeys = len(keys)
		}
	}

	bwData, err := os.ReadFile("data/bandwidth.json")
	if err != nil {
		return 0, limitMB, 0, "Normal"
	}
	var entries map[string]struct {
		UsedKB float64 `json:"used_kb"`
		UsedMB float64 `json:"used_mb"`
	}
	if err := json.Unmarshal(bwData, &entries); err != nil {
		return 0, limitMB, 0, "Normal"
	}

	// Sum usage across only the active keys (key0_, key1_, ... key{N-1}_),
	// ignoring stale entries from any key no longer in WEBSHARE_API_KEYS.
	var totalUsedMB float64
	for i := 0; i < numActiveKeys; i++ {
		prefix := fmt.Sprintf("key%d_", i)
		for k, e := range entries {
			if strings.HasPrefix(k, prefix) {
				m := e.UsedMB
				if m == 0 && e.UsedKB > 0 {
					m = e.UsedKB / 1024.0
				}
				totalUsedMB += m
				break
			}
		}
	}

	totalLimitMB := limitMB * numActiveKeys
	pct := 0.0
	if totalLimitMB > 0 {
		pct = (totalUsedMB / float64(totalLimitMB)) * 100.0
	}
	if pct > 100.0 {
		pct = 100.0
	}

	status := "Active (Normal)"
	if pct >= 95.0 {
		status = "Paused (Quota Exhausted)"
	} else if pct >= 80.0 {
		status = "Warning (>80%)"
	}
	return totalUsedMB, totalLimitMB, pct, status
}

func createBenchmarkWAV() []byte {
	sampleRate := 16000
	durationSeconds := 1.0
	numSamples := int(float64(sampleRate) * durationSeconds)
	dataSize := numSamples * 2 // 16-bit mono = 2 bytes per sample

	buf := new(bytes.Buffer)
	buf.WriteString("RIFF")
	_ = binary.Write(buf, binary.LittleEndian, uint32(36+dataSize))
	buf.WriteString("WAVE")

	buf.WriteString("fmt ")
	_ = binary.Write(buf, binary.LittleEndian, uint32(16)) // subchunk1 size
	_ = binary.Write(buf, binary.LittleEndian, uint16(1))  // PCM format
	_ = binary.Write(buf, binary.LittleEndian, uint16(1))  // mono channel
	_ = binary.Write(buf, binary.LittleEndian, uint32(sampleRate))
	_ = binary.Write(buf, binary.LittleEndian, uint32(sampleRate*2)) // byte rate
	_ = binary.Write(buf, binary.LittleEndian, uint16(2))           // block align
	_ = binary.Write(buf, binary.LittleEndian, uint16(16))          // bits per sample

	buf.WriteString("data")
	_ = binary.Write(buf, binary.LittleEndian, uint32(dataSize))

	frequency := 440.0
	for i := 0; i < numSamples; i++ {
		t := float64(i) / float64(sampleRate)
		sample := int16(32767 * 0.3 * math.Sin(2*math.Pi*frequency*t))
		_ = binary.Write(buf, binary.LittleEndian, sample)
	}

	return buf.Bytes()
}

func loadEnvFile(path string) map[string]string {
	res := make(map[string]string)
	data, err := os.ReadFile(path)
	if err != nil {
		return res
	}
	lines := strings.Split(string(data), "\n")
	for _, l := range lines {
		l = strings.TrimSpace(l)
		if l == "" || strings.HasPrefix(l, "#") {
			continue
		}
		parts := strings.SplitN(l, "=", 2)
		if len(parts) == 2 {
			k := strings.TrimSpace(parts[0])
			v := strings.Trim(strings.TrimSpace(parts[1]), `"'`)
			res[k] = v
		}
	}
	return res
}

func queryArticles(db *sql.DB) []ArticleRow {
	rows, err := db.Query(`
		SELECT id, title, url, searched_count,
		       COALESCE(serp_position, 0),
		       COALESCE(date(last_searched_at), 'never')
		FROM articles
		ORDER BY last_searched_at DESC
		LIMIT 100`)
	if err != nil {
		log.Printf("query articles: %v", err)
		return nil
	}
	defer rows.Close()

	var out []ArticleRow
	for rows.Next() {
		var r ArticleRow
		var pos int
		rows.Scan(&r.ID, &r.Title, &r.URL, &r.Searched, &pos, &r.LastSearched)
		if pos == 0 {
			r.SerpPosition = "N/A"
		} else {
			r.SerpPosition = fmt.Sprintf("#%d", pos)
		}
		out = append(out, r)
	}
	return out
}

func computeTotals(rows []DailyRow) DailyRow {
	var t DailyRow
	t.Date = "TOTAL"
	for _, r := range rows {
		t.TotalSearch += r.TotalSearch
		t.Success += r.Success
		t.Fail += r.Fail
		t.Captcha += r.Captcha
	}
	if t.TotalSearch > 0 {
		t.SuccessRate = float64(t.Success) / float64(t.TotalSearch) * 100
		t.CaptchaRate = float64(t.Captcha) / float64(t.TotalSearch) * 100
	}
	return t
}

var loginTmpl = template.Must(template.New("login").Parse(`<!DOCTYPE html>
<html lang="en">
<head>
<meta charset="UTF-8">
<meta name="viewport" content="width=device-width, initial-scale=1.0">
<title>Sign In • Google Automation Suite</title>
<link rel="icon" type="image/png" href="/static/favicon.png">
<link rel="manifest" href="/manifest.json">
<meta name="theme-color" content="#0b0f19">
<link rel="apple-touch-icon" href="/static/pwa-icon.jpg">
<style>
  :root { --bg: #0b0f19; --card: #161f30; --text: #f8fafc; --muted: #94a3b8; --border: #334155; --primary: #6366f1; --primary-hover: #4f46e5; --danger: #ef4444; }
  * { box-sizing: border-box; margin: 0; padding: 0; }
  body { font-family: -apple-system, BlinkMacSystemFont, "Segoe UI", Roboto, sans-serif; background: var(--bg); color: var(--text); min-height: 100vh; display: flex; align-items: center; justify-content: center; padding: 20px; }
  .login-card { width: 100%; max-width: 380px; background: var(--card); border: 1px solid var(--border); border-radius: 16px; padding: 36px 28px; box-shadow: 0 10px 40px rgba(0,0,0,0.6); text-align: center; }
  .logo-box { width: 56px; height: 56px; border-radius: 14px; background: #0f172a; border: 1px solid var(--border); margin: 0 auto 16px; display: flex; align-items: center; justify-content: center; overflow: hidden; box-shadow: 0 4px 20px rgba(99, 102, 241, 0.3); }
  h1 { font-size: 20px; font-weight: 700; background: linear-gradient(135deg, #ffffff, #c7d2fe); -webkit-background-clip: text; -webkit-text-fill-color: transparent; margin-bottom: 6px; }
  p.subtitle { color: var(--muted); font-size: 13px; margin-bottom: 24px; }
  .form-group { text-align: left; margin-bottom: 16px; }
  label { display: block; font-size: 12px; font-weight: 600; color: var(--muted); margin-bottom: 6px; }
  input[type="text"], input[type="password"] { width: 100%; background: #0b0f19; color: white; border: 1px solid var(--border); padding: 11px 14px; border-radius: 8px; font-size: 14px; outline: none; transition: border-color 0.2s; }
  input[type="text"]:focus, input[type="password"]:focus { border-color: var(--primary); }
  .btn-submit { width: 100%; background: linear-gradient(135deg, #6366f1, #8b5cf6); color: white; border: none; padding: 12px; border-radius: 8px; font-size: 14px; font-weight: 600; cursor: pointer; transition: opacity 0.2s; margin-top: 8px; }
  .btn-submit:hover { opacity: 0.92; }
  .error-box { background: #ef44441a; border: 1px solid #ef444444; color: #f87171; font-size: 13px; padding: 10px; border-radius: 8px; margin-bottom: 16px; text-align: left; }
</style>
</head>
<body>
<div class="login-card">
  <div class="logo-box">
    <img src="/static/favicon.png" alt="Logo" style="width: 100%; height: 100%; object-fit: cover;">
  </div>
  <h1>Google Automation Suite</h1>
  <p class="subtitle">Enter your credentials to access Fleet Control Panel</p>
  {{if .Error}}
  <div class="error-box">⚠️ {{.Error}}</div>
  {{end}}
  <form method="POST" action="/login">
    <div class="form-group">
      <label>Username</label>
      <input type="text" name="username" placeholder="admin" required autofocus>
    </div>
    <div class="form-group">
      <label>Password</label>
      <input type="password" name="password" placeholder="••••••••••••" required>
    </div>
    <button type="submit" class="btn-submit">Sign In to Dashboard</button>
  </form>
</div>
<script>
  if ('serviceWorker' in navigator) {
    window.addEventListener('load', () => {
      navigator.serviceWorker.register('/sw.js').then(r => console.log('SW', r.scope)).catch(e => console.log('SW err', e));
    });
  }
</script>
</body>
</html>`))

var tmpl = template.Must(template.New("dashboard").Parse(`<!DOCTYPE html>
<html lang="en">
<head>
<meta charset="UTF-8">
<meta name="viewport" content="width=device-width, initial-scale=1.0">
<title>Google Automation Control Panel</title>
<link rel="icon" type="image/png" href="/static/favicon.png">
<link rel="shortcut icon" href="/static/favicon.png">
<link rel="manifest" href="/manifest.json">
<meta name="theme-color" content="#0b0f19">
<link rel="apple-touch-icon" href="/static/pwa-icon.jpg">
<script src="https://cdn.jsdelivr.net/npm/chart.js"></script>
<style>
  :root { --bg: #0b0f19; --card: #161f30; --card-hover: #1e293b; --text: #f8fafc; --muted: #94a3b8; --border: #334155; --primary: #6366f1; --primary-hover: #4f46e5; --success: #10b981; --danger: #ef4444; --warning: #f59e0b; }
  * { box-sizing: border-box; }
  body { font-family: -apple-system, BlinkMacSystemFont, "Segoe UI", Roboto, sans-serif; background: var(--bg); color: var(--text); margin: 0; padding: 24px; }
  .header { display: flex; justify-content: space-between; align-items: center; border-bottom: 1px solid var(--border); padding-bottom: 16px; margin-bottom: 20px; }
  h1 { margin: 0; font-size: 22px; color: var(--primary); display: flex; align-items: center; gap: 8px; }
  .badge { background: #10b98122; color: #10b981; border: 1px solid #10b98144; padding: 4px 10px; border-radius: 9999px; font-size: 12px; font-weight: 600; }
  .meta { color: var(--muted); font-size: 13px; margin-top: 4px; }
  
  /* Tabs */
  .nav-tabs { display: flex; gap: 8px; border-bottom: 1px solid var(--border); margin-bottom: 20px; }
  .tab-btn { background: transparent; color: var(--muted); border: none; padding: 10px 18px; font-size: 14px; font-weight: 600; cursor: pointer; border-bottom: 2px solid transparent; transition: all 0.2s; }
  .tab-btn:hover { color: var(--text); }
  .tab-btn.active { color: var(--primary); border-bottom-color: var(--primary); }
  .tab-content { display: none; }
  .tab-content.active { display: block; }

  /* Grid Stats */
  .grid-stats { display: grid; grid-template-columns: repeat(auto-fit, minmax(200px, 1fr)); gap: 16px; margin-bottom: 20px; }
  .stat-card { background: var(--card); border: 1px solid var(--border); border-radius: 12px; padding: 16px; }
  .stat-card .label { color: var(--muted); font-size: 13px; font-weight: 500; }
  .stat-card .val { font-size: 26px; font-weight: 700; margin-top: 4px; }
  
  /* Fleet Grid */
  .fleet-header { display: flex; justify-content: space-between; align-items: center; margin-bottom: 12px; }
  .fleet-grid { display: grid; grid-template-columns: repeat(auto-fill, minmax(280px, 1fr)); gap: 16px; margin-bottom: 24px; }
  .worker-card { background: var(--card); border: 1px solid var(--border); border-radius: 12px; padding: 16px; position: relative; }
  .worker-card .head { display: flex; justify-content: space-between; align-items: center; margin-bottom: 8px; }
  .worker-card .title { font-weight: 700; font-size: 15px; }
  .worker-card .proxy { font-size: 13px; color: var(--muted); margin-bottom: 6px; }
  .worker-card .action { font-size: 13px; font-weight: 600; color: #cbd5e1; margin-bottom: 10px; min-height: 20px; }
  .progress-bg { background: #1e293b; border-radius: 9999px; height: 8px; overflow: hidden; width: 100%; }
  .progress-bar { background: var(--primary); height: 100%; width: 0%; transition: width 0.3s; }
  .status-tag { font-size: 11px; padding: 2px 8px; border-radius: 6px; font-weight: 700; }
  .status-ACTIVE { background: #10b98122; color: #10b981; border: 1px solid #10b98144; }
  .status-READING { background: #6366f122; color: #a5b4fc; border: 1px solid #6366f144; }
  .status-SEARCHING { background: #38bdf822; color: #38bdf8; border: 1px solid #38bdf844; }
  .status-COOLDOWN { background: #f59e0b22; color: #f59e0b; border: 1px solid #f59e0b44; }
  .status-SOLVING { background: #ef444422; color: #ef4444; border: 1px solid #ef444444; }
  .status-IDLE { background: #47556922; color: #94a3b8; border: 1px solid #47556944; }

  /* Buttons & Controls */
  .btn { display: inline-flex; align-items: center; gap: 6px; background: var(--primary); color: white; border: none; padding: 8px 16px; border-radius: 8px; font-size: 13px; font-weight: 600; cursor: pointer; text-decoration: none; }
  .btn:hover { background: var(--primary-hover); }
  .btn-sm { padding: 4px 10px; font-size: 12px; }
  .btn-secondary { background: #334155; }
  .btn-secondary:hover { background: #475569; }

  /* Terminal */
  .terminal-box { background: #050811; border: 1px solid var(--border); border-radius: 12px; padding: 16px; font-family: monospace; font-size: 13px; height: 420px; overflow-y: auto; line-height: 1.5; }
  .log-line { margin-bottom: 4px; }
  .log-INFO { color: #38bdf8; }
  .log-TASK { color: #c084fc; font-weight: 600; }
  .log-SUCCESS { color: #4ade80; font-weight: 600; }
  .log-WARN { color: #facc15; }
  .log-ERROR { color: #f87171; font-weight: 700; }

  /* Cards & Tables */
  .card { background: var(--card); border: 1px solid var(--border); border-radius: 12px; padding: 20px; margin-bottom: 20px; }
  h2 { font-size: 16px; font-weight: 600; color: #c4b5fd; margin: 0 0 16px 0; }
  table { width: 100%; border-collapse: collapse; font-size: 13px; }
  th { background: #0b0f1988; color: var(--muted); text-align: left; padding: 10px 12px; font-weight: 600; border-bottom: 1px solid var(--border); }
  td { padding: 9px 12px; border-bottom: 1px solid var(--border); }
  tr:hover td { background: var(--card-hover); }
  .good { color: var(--success); font-weight: 600; }
  .bad { color: var(--danger); font-weight: 600; }
  .warn { color: var(--warning); font-weight: 600; }
  .na { color: #64748b; }
  
  /* Pagination Styles */
  .pagination-wrapper { display: flex; justify-content: space-between; align-items: center; margin-top: 16px; font-size: 13px; color: var(--muted); flex-wrap: wrap; gap: 10px; }
  .pagination-controls { display: flex; gap: 6px; align-items: center; }
  .page-btn { background: #1e293b; color: var(--text); border: 1px solid var(--border); padding: 5px 12px; border-radius: 6px; cursor: pointer; font-size: 12px; font-weight: 600; transition: all 0.15s; }
  .page-btn:hover:not(:disabled) { background: var(--primary); border-color: var(--primary); }
  .page-btn:disabled { opacity: 0.4; cursor: not-allowed; }
  .page-btn.active { background: var(--primary); border-color: var(--primary); }
  .page-select { background: #1e293b; color: var(--text); border: 1px solid var(--border); padding: 5px 10px; border-radius: 6px; font-size: 12px; font-weight: 600; }
  
  /* Config editor textarea */
  .yaml-editor { width: 100%; height: 380px; background: #050811; color: #a5b4fc; border: 1px solid var(--border); border-radius: 8px; padding: 14px; font-family: monospace; font-size: 13px; resize: vertical; }
</style>
</head>
<body>

<div class="header">
  <div style="display: flex; align-items: center; gap: 14px;">
    <div style="width: 46px; height: 46px; border-radius: 12px; background: #0f172a; border: 1px solid #334155; display: flex; align-items: center; justify-content: center; overflow: hidden; box-shadow: 0 4px 20px rgba(99, 102, 241, 0.25);">
      <img src="/static/favicon.png" alt="Logo" style="width: 100%; height: 100%; object-fit: cover;">
    </div>
    <div>
      <h1 style="margin: 0; font-size: 21px; font-weight: 700; background: linear-gradient(135deg, #ffffff, #c7d2fe); -webkit-background-clip: text; -webkit-text-fill-color: transparent; display: flex; align-items: center; gap: 10px;">
        Google Automation Suite <span class="badge">LIVE</span>
      </h1>
      <div class="meta" style="color: #94a3b8; font-size: 13px; margin-top: 2px;">Multi-Worker Fleet System • bagasunix.com • Last update: {{.Generated}}</div>
    </div>
  </div>
  <div style="display: flex; gap: 8px; align-items: center;">
    <button id="btnHeaderFleetToggle" onclick="toggleFleetEngine()" class="btn" style="background: linear-gradient(135deg, #10b981, #059669); font-weight: 700; display: inline-flex; align-items: center; gap: 6px; box-shadow: 0 4px 15px rgba(16, 185, 129, 0.25);">
      ▶️ Start Bot Fleet
    </button>
    <a href="/api/export/csv" class="btn btn-secondary" style="text-decoration: none;">📥 Export CSV</a>
    <a href="/logout" class="btn btn-secondary" style="background: #ef444418; color: #f87171; border: 1px solid #ef444444; text-decoration: none;" title="Sign Out">🚪 Sign Out</a>
  </div>
</div>

<div class="nav-tabs">
  <button class="tab-btn active" onclick="showTab('tab-fleet')">🤖 Bot Fleet & Terminal</button>
  <button class="tab-btn" onclick="showTab('tab-analytics')">📊 Analytics & Trends</button>
  <button class="tab-btn" onclick="showTab('tab-articles')">🌐 Articles & SERP</button>
  <button class="tab-btn" onclick="showTab('tab-proxies')">🛡️ Proxy Hub</button>
  <button class="tab-btn" onclick="showTab('tab-settings')">⚙️ Settings Editor</button>
</div>

<!-- TAB 1: FLEET & LIVE TERMINAL -->
<div id="tab-fleet" class="tab-content active">
  <!-- MASTER FLEET ENGINE CONTROL CARD -->
  <div class="card" style="margin-bottom: 20px; background: linear-gradient(135deg, #0f172a, #161a36); border: 1px solid #4338ca; box-shadow: 0 4px 20px rgba(99, 102, 241, 0.15);">
    <div style="display: flex; justify-content: space-between; align-items: center; flex-wrap: wrap; gap: 14px;">
      <div>
        <div style="display: flex; align-items: center; gap: 10px;">
          <span id="fleetStatusDot" style="width: 12px; height: 12px; border-radius: 50%; background: #94a3b8; display: inline-block;"></span>
          <h2 style="margin: 0; font-size: 17px; color: #ffffff;" id="fleetStatusTitle">Bot Fleet Engine: Standby / Idle</h2>
          <span class="badge" id="fleetStatusBadge" style="background: #64748b22; color: #94a3b8; border: 1px solid #64748b44;">STANDBY</span>
        </div>
        <div class="meta" id="fleetStatusSub" style="margin-top: 4px; font-size: 12px; color: #94a3b8;">
          Tekan tombol Start untuk menjalankan Go Orchestrator & Python Stealth Worker secara otomatis.
        </div>
      </div>
      <div style="display: flex; gap: 10px; align-items: center;">
        <button id="btnMainFleetToggle" onclick="toggleFleetEngine()" class="btn" style="background: linear-gradient(135deg, #10b981, #059669); font-weight: 700; font-size: 14px; padding: 10px 22px; box-shadow: 0 4px 15px rgba(16, 185, 129, 0.3);">
          ▶️ Start Bot Fleet Engine
        </button>
      </div>
    </div>
  </div>

  <div class="fleet-header">
    <h2>Active Bot Fleet Grid</h2>
    <div style="display: flex; align-items: center; gap: 8px;">
      <span class="meta">Active Workers:</span>
      <button class="btn btn-sm btn-secondary" onclick="changeConcurrency(-1)">-</button>
      <span id="concurrencyCount" style="font-weight: 700;">{{.Concurrency}}</span>
      <button class="btn btn-sm btn-secondary" onclick="changeConcurrency(1)">+</button>
    </div>
  </div>

  <div class="fleet-grid" id="fleetGridContainer">
    <!-- Rendered dynamically via JS -->
  </div>

  <div class="card">
    <div style="display: flex; justify-content: space-between; align-items: center; margin-bottom: 12px;">
      <h2>🖥️ Multi-Stream Live Terminal Output</h2>
      <div>
        <select id="workerLogFilter" onchange="filterLogs()" style="background: #1e293b; color: white; border: 1px solid var(--border); padding: 4px 8px; border-radius: 6px;">
          <option value="0">All Workers</option>
          <option value="1">Worker #1</option>
          <option value="2">Worker #2</option>
          <option value="3">Worker #3</option>
          <option value="4">Worker #4</option>
          <option value="5">Worker #5</option>
        </select>
        <button class="btn btn-sm btn-secondary" onclick="clearLogs()">Clear</button>
      </div>
    </div>
    <div class="terminal-box" id="terminalBox">
      <!-- Live logs stream here -->
    </div>
  </div>
</div>

<!-- TAB 2: ANALYTICS & TRENDS -->
<div id="tab-analytics" class="tab-content">
  <div style="display: flex; justify-content: space-between; align-items: center; margin-bottom: 16px; flex-wrap: wrap; gap: 10px;">
    <div>
      <h2 style="margin: 0; font-size: 18px;">📊 Search Engine Performance & Conversion Metrics</h2>
      <div class="meta">Aggregated search engine automation metrics over the last 30 days</div>
    </div>
    <button type="button" class="btn btn-sm btn-secondary" onclick="resetAnalyticsStats()" style="background: #ef444418; color: #f87171; border: 1px solid #ef444444;">
      🗑️ Reset Stats to 0
    </button>
  </div>

  <div class="grid-stats">
    <div class="stat-card">
      <div class="label">Total Searches (30d)</div>
      <div class="val">{{.Totals.TotalSearch}}</div>
    </div>
    <div class="stat-card">
      <div class="label">Success Rate</div>
      <div class="val good">{{printf "%.1f" .Totals.SuccessRate}}%</div>
    </div>
    <div class="stat-card">
      <div class="label">CAPTCHA Rate</div>
      <div class="val warn">{{printf "%.1f" .Totals.CaptchaRate}}%</div>
    </div>
    <div class="stat-card">
      <div class="label">Active Proxies</div>
      <div class="val">{{len .Proxies}}</div>
    </div>
  </div>

  <div class="card">
    <h2>Performance & Ranking Trend (30 Days)</h2>
    <div style="height: 260px; position: relative;">
      <canvas id="perfChart"></canvas>
    </div>
  </div>

  <div class="card">
    <h2>Daily Performance Table</h2>
    <table id="dailyTable">
      <thead>
        <tr><th>Date</th><th>Searches</th><th>OK</th><th>Fail</th><th>CAPTCHA</th><th>Success%</th><th>Avg Dwell</th><th>Avg SERP</th></tr>
      </thead>
      <tbody>
        {{range .Daily}}
        <tr>
          <td>{{.Date}}</td>
          <td>{{.TotalSearch}}</td>
          <td class="good">{{.Success}}</td>
          <td class="bad">{{.Fail}}</td>
          <td class="warn">{{.Captcha}}</td>
          <td class="{{if ge .SuccessRate 50.0}}good{{else}}bad{{end}}">{{printf "%.1f" .SuccessRate}}%</td>
          <td>{{printf "%.1f" .AvgDwellSeconds}}s</td>
          <td>{{if eq .AvgSerpPosition 0.0}}<span class="na">N/A</span>{{else}}{{printf "%.1f" .AvgSerpPosition}}{{end}}</td>
        </tr>
        {{end}}
      </tbody>
    </table>
    <div class="pagination-wrapper">
      <div>
        Show 
        <select id="dailyPageSize" class="page-select" onchange="changeDailyPageSize()">
          <option value="10" selected>10</option>
          <option value="25">25</option>
          <option value="50">50</option>
        </select>
        entries per page
      </div>
      <div id="dailyPageInfo">Showing entries</div>
      <div class="pagination-controls">
        <button class="page-btn" id="dailyPrevBtn" onclick="prevDailyPage()">‹ Prev</button>
        <span id="dailyPageNumbers"></span>
        <button class="page-btn" id="dailyNextBtn" onclick="nextDailyPage()">Next ›</button>
      </div>
    </div>
  </div>
</div>

<!-- TAB 3: ARTICLES & SERP -->
<div id="tab-articles" class="tab-content">
  <div class="card">
    <div style="display: flex; justify-content: space-between; align-items: center; margin-bottom: 12px; flex-wrap: wrap; gap: 10px;">
      <h2>Articles & SERP Rankings</h2>
      <input type="text" id="articleSearchInput" onkeyup="filterArticles()" placeholder="Filter article title..." style="background: #1e293b; color: white; border: 1px solid var(--border); padding: 6px 12px; border-radius: 6px; width: 260px;">
    </div>
    <table id="articlesTable">
      <thead>
        <tr><th>Title</th><th>Searches</th><th>SERP Rank</th><th>Last Searched</th><th>Action</th></tr>
      </thead>
      <tbody id="articlesTableBody">
        {{range .Articles}}
        <tr>
          <td><a href="{{.URL}}" target="_blank" style="color: #a5b4fc; text-decoration: none;">{{.Title}}</a></td>
          <td>{{.Searched}}</td>
          <td class="{{if eq .SerpPosition "N/A"}}na{{else}}good{{end}}">{{.SerpPosition}}</td>
          <td>{{.LastSearched}}</td>
          <td>
            <button class="btn btn-sm" onclick="triggerSearch({{.ID}})">⚡ Cari Sekarang</button>
          </td>
        </tr>
        {{end}}
      </tbody>
    </table>
    
    <div class="pagination-wrapper">
      <div>
        Show 
        <select id="articlePageSize" class="page-select" onchange="changeArticlePageSize()">
          <option value="10">10</option>
          <option value="25" selected>25</option>
          <option value="50">50</option>
          <option value="100">100</option>
        </select>
        articles per page
      </div>
      <div id="articlePageInfo">Showing articles</div>
      <div class="pagination-controls">
        <button class="page-btn" id="artPrevBtn" onclick="prevArticlePage()">‹ Prev</button>
        <span id="artPageNumbers"></span>
        <button class="page-btn" id="artNextBtn" onclick="nextArticlePage()">Next ›</button>
      </div>
    </div>
  </div>
</div>

<!-- TAB 4: PROXIES -->
<div id="tab-proxies" class="tab-content">
  <div class="grid-stats" style="margin-bottom: 20px;">
    <div class="stat-card">
      <div class="label">Monthly Bandwidth Used</div>
      <div class="val">{{printf "%.1f" .BandwidthUsedMB}} / {{.BandwidthLimitMB}} MB</div>
    </div>
    <div class="stat-card">
      <div class="label">Bandwidth Quota Status</div>
      <div class="val {{if ge .BandwidthPercent 95.0}}bad{{else if ge .BandwidthPercent 80.0}}warn{{else}}good{{end}}">{{.BandwidthStatus}}</div>
    </div>
    <div class="stat-card">
      <div class="label">Auto-Pause Protection</div>
      <div class="val good">95% (Active)</div>
    </div>
    <div class="stat-card">
      <div class="label">Active Proxy Pool</div>
      <div class="val">{{len .Proxies}} Nodes</div>
    </div>
  </div>

  <div class="card" style="margin-bottom: 20px;">
    <div style="display: flex; justify-content: space-between; align-items: center; margin-bottom: 8px;">
      <h2 style="margin: 0; font-size: 15px;">⚡ Monthly Bandwidth Usage Bar</h2>
      <span style="font-weight: 700; font-size: 13px; color: #a5b4fc;">{{printf "%.1f" .BandwidthPercent}}%</span>
    </div>
    <div class="progress-bg" style="height: 12px; border-radius: 6px;">
      <div class="progress-bar" style="width: {{printf "%.1f" .BandwidthPercent}}%; background: {{if ge .BandwidthPercent 95.0}}var(--danger){{else if ge .BandwidthPercent 80.0}}var(--warning){{else}}linear-gradient(90deg, #6366f1, #10b981){{end}};"></div>
    </div>
  </div>

  <div class="card">
    <div style="display: flex; justify-content: space-between; align-items: center; margin-bottom: 12px; flex-wrap: wrap; gap: 8px;">
      <div>
        <h2 style="margin: 0;">🛡️ Multi-Account Proxy Pool & Health Hub</h2>
        <div class="meta">Combined active proxies from all connected Webshare API keys & custom lists</div>
      </div>
      <span class="badge" style="background: #10b98122; color: #10b981; border: 1px solid #10b98144;">Pool Size: {{len .Proxies}} Proxies</span>
    </div>
    <table id="proxiesTable">
      <thead>
        <tr>
          <th>IP:Port</th>
          <th>Account / Provider</th>
          <th>Country</th>
          <th>Used Count</th>
          <th>Status</th>
        </tr>
      </thead>
      <tbody>
        {{range .Proxies}}
        <tr>
          <td><code style="color: #38bdf8; font-size: 13px;">{{.IP}}:{{.Port}}</code></td>
          <td><span class="badge" style="background: #1e293b; color: #cbd5e1; border: 1px solid var(--border); font-size: 11px;">{{.AccountLabel}}</span></td>
          <td><b>{{.Country}}</b></td>
          <td>{{.Used}} reqs</td>
          <td>
            {{if eq .StatusBadge "🟢 Active"}}
              <span class="badge" style="background: #10b98122; color: #10b981; border: 1px solid #10b98144; font-size: 11px;">🟢 Active</span>
            {{else if eq .StatusBadge "⚠️ Quota 402"}}
              <span class="badge" style="background: #f59e0b22; color: #f59e0b; border: 1px solid #f59e0b44; font-size: 11px;" title="Bandwidth 1GB Monthly Quota Exhausted on Webshare (HTTP 402)">⚠️ Quota 402</span>
            {{else}}
              <span class="badge" style="background: #ef444422; color: #ef4444; border: 1px solid #ef444444; font-size: 11px;" title="{{.BlacklistReason}}">{{.StatusBadge}}</span>
            {{end}}
          </td>
        </tr>
        {{end}}
      </tbody>
    </table>
    <div class="pagination-wrapper">
      <div>
        Show 
        <select id="proxyPageSize" class="page-select" onchange="changeProxyPageSize()">
          <option value="10" selected>10</option>
          <option value="25">25</option>
          <option value="50">50</option>
        </select>
        proxies per page
      </div>
      <div id="proxyPageInfo">Showing proxies</div>
      <div class="pagination-controls">
        <button class="page-btn" id="proxyPrevBtn" onclick="prevProxyPage()">‹ Prev</button>
        <span id="proxyPageNumbers"></span>
        <button class="page-btn" id="proxyNextBtn" onclick="nextProxyPage()">Next ›</button>
      </div>
    </div>
  </div>
</div>

<!-- TAB 5: SETTINGS EDITOR -->
<div id="tab-settings" class="tab-content">
  <div style="display: flex; justify-content: space-between; align-items: center; margin-bottom: 20px; flex-wrap: wrap; gap: 12px;">
    <div>
      <h2 style="margin: 0; font-size: 20px; color: var(--primary);">⚙️ Campaign & Engine Settings</h2>
      <div class="meta">Manage search engine distribution, scheduler timing, target domains, and anti-detection engines</div>
    </div>
    <div style="display: flex; gap: 10px;">
      <button class="btn btn-secondary" onclick="loadConfigVisual()">🔄 Reset</button>
      <button class="btn" onclick="saveConfigVisual()" style="background: linear-gradient(135deg, #6366f1, #8b5cf6); padding: 10px 22px; font-size: 14px;">💾 Save & Apply Live</button>
    </div>
  </div>

  <div style="display: grid; grid-template-columns: repeat(auto-fit, minmax(320px, 1fr)); gap: 20px;">
    <!-- CARD 1: TRAFFIC MIXER -->
    <div class="card" style="margin-bottom: 0;">
      <h2>🔀 Traffic Source Distribution Matrix</h2>
      <p class="meta" style="margin-bottom: 16px;">Proportional traffic split across search engines & referral channels.</p>
      
      <div style="margin-bottom: 14px;">
        <div style="display: flex; justify-content: space-between; font-size: 13px; font-weight: 600; margin-bottom: 6px;">
          <span>🔵 Google Search</span>
          <span id="googleVal" style="color: #6366f1;">70%</span>
        </div>
        <input type="range" id="cfgGoogle" min="0" max="100" value="70" oninput="updateSliders()" style="width: 100%; accent-color: #6366f1;">
      </div>

      <div style="margin-bottom: 14px;">
        <div style="display: flex; justify-content: space-between; font-size: 13px; font-weight: 600; margin-bottom: 6px;">
          <span>🔷 Bing Search</span>
          <span id="bingVal" style="color: #38bdf8;">10%</span>
        </div>
        <input type="range" id="cfgBing" min="0" max="100" value="10" oninput="updateSliders()" style="width: 100%; accent-color: #38bdf8;">
      </div>

      <div style="margin-bottom: 14px;">
        <div style="display: flex; justify-content: space-between; font-size: 13px; font-weight: 600; margin-bottom: 6px;">
          <span>🟣 Direct Bookmark / Referral</span>
          <span id="directVal" style="color: #c084fc;">10%</span>
        </div>
        <input type="range" id="cfgDirect" min="0" max="100" value="10" oninput="updateSliders()" style="width: 100%; accent-color: #c084fc;">
      </div>

      <div style="margin-bottom: 14px;">
        <div style="display: flex; justify-content: space-between; font-size: 13px; font-weight: 600; margin-bottom: 6px;">
          <span>🟠 Social Referral (Twitter/Reddit)</span>
          <span id="socialVal" style="color: #f59e0b;">10%</span>
        </div>
        <input type="range" id="cfgSocial" min="0" max="100" value="10" oninput="updateSliders()" style="width: 100%; accent-color: #f59e0b;">
      </div>

      <div style="background: #0b0f19; border: 1px solid var(--border); border-radius: 8px; padding: 10px 14px; display: flex; justify-content: space-between; align-items: center;">
        <span style="font-size: 13px; font-weight: 600;">Total Split:</span>
        <span id="totalRatioBadge" class="badge">100% (Balanced)</span>
      </div>
    </div>

    <!-- CARD 2: SCHEDULER & TIMING -->
    <div class="card" style="margin-bottom: 0;">
      <h2>⏱️ Scheduler & Fleet Cooldowns</h2>
      <p class="meta" style="margin-bottom: 16px;">Fine-tune humanized pauses between search sessions.</p>

      <div style="display: grid; grid-template-columns: 1fr 1fr; gap: 12px; margin-bottom: 14px;">
        <div>
          <label style="font-size: 12px; color: var(--muted); font-weight: 600;">Min Cooldown (s)</label>
          <input type="number" id="cfgMinCooldown" value="30" style="width: 100%; background: #0b0f19; color: white; border: 1px solid var(--border); padding: 8px 12px; border-radius: 6px; margin-top: 4px;">
        </div>
        <div>
          <label style="font-size: 12px; color: var(--muted); font-weight: 600;">Max Cooldown (s)</label>
          <input type="number" id="cfgMaxCooldown" value="120" style="width: 100%; background: #0b0f19; color: white; border: 1px solid var(--border); padding: 8px 12px; border-radius: 6px; margin-top: 4px;">
        </div>
      </div>

      <div style="margin-bottom: 14px;">
        <label style="font-size: 12px; color: var(--muted); font-weight: 600;">Max Searches Per Proxy / Day</label>
        <input type="number" id="cfgMaxPerProxy" value="5" style="width: 100%; background: #0b0f19; color: white; border: 1px solid var(--border); padding: 8px 12px; border-radius: 6px; margin-top: 4px;">
      </div>

      <div style="display: grid; grid-template-columns: 1fr 1fr; gap: 12px; margin-bottom: 14px;">
        <div>
          <label style="font-size: 12px; color: var(--muted); font-weight: 600;">Active Start (0-23)</label>
          <input type="number" id="cfgActiveStart" value="7" min="0" max="23" style="width: 100%; background: #0b0f19; color: white; border: 1px solid var(--border); padding: 8px 12px; border-radius: 6px; margin-top: 4px;">
        </div>
        <div>
          <label style="font-size: 12px; color: var(--muted); font-weight: 600;">Active End (0-23)</label>
          <input type="number" id="cfgActiveEnd" value="23" min="0" max="23" style="width: 100%; background: #0b0f19; color: white; border: 1px solid var(--border); padding: 8px 12px; border-radius: 6px; margin-top: 4px;">
        </div>
      </div>

      <div style="display: flex; justify-content: space-between; align-items: center; padding-top: 6px;">
        <div>
          <div style="font-size: 13px; font-weight: 600;">Pre-Search Warmup</div>
          <div class="meta" style="font-size: 11px;">Simulate general search history</div>
        </div>
        <input type="checkbox" id="cfgPreSearch" checked style="width: 20px; height: 20px; accent-color: var(--primary);">
      </div>
    </div>

    <!-- CARD 3: TARGET DOMAINS -->
    <div class="card" style="margin-bottom: 0;">
      <h2>🎯 Target Websites & Domains</h2>
      <p class="meta" style="margin-bottom: 16px;">Domains crawled via sitemap and targeted on search engines.</p>

      <div style="display: flex; gap: 8px; margin-bottom: 14px;">
        <input type="text" id="newDomainInput" placeholder="e.g. bagasunix.com" style="flex: 1; background: #0b0f19; color: white; border: 1px solid var(--border); padding: 8px 12px; border-radius: 6px;">
        <button class="btn" onclick="addDomainTag()">+ Add</button>
      </div>

      <div id="domainTagsContainer" style="display: flex; flex-wrap: wrap; gap: 8px; min-height: 48px; background: #0b0f19; border: 1px solid var(--border); border-radius: 8px; padding: 10px;">
        <!-- Domain tags rendered here -->
      </div>
    </div>

    <!-- CARD 4: PROXY CONNECTION & BULK IMPORT -->
    <div class="card" style="margin-bottom: 0; grid-column: span 2;">
      <h2>🛡️ Proxy Provider & Connection Hub</h2>
      <p class="meta" style="margin-bottom: 16px;">Configure upstream proxy rotation mode, custom gateway, or bulk upload proxy lists.</p>

      <div style="margin-bottom: 16px;">
        <label style="font-size: 12px; color: var(--muted); font-weight: 600;">Active Connection Provider</label>
        <select id="cfgProxyProvider" onchange="toggleProxyPanels()" style="width: 100%; background: #0b0f19; color: white; border: 1px solid var(--border); padding: 10px 12px; border-radius: 8px; margin-top: 4px; font-weight: 600;">
          <option value="webshare">🌐 Webshare API (Multi-Key Rotation & Auto-Scrape)</option>
          <option value="residential">🏠 Custom Residential Gateway (Smartproxy / IPRoyal / Sticky Sessions)</option>
          <option value="custom_file">📄 Custom Proxy List & Bulk Upload (.txt / Paste)</option>
        </select>
      </div>

      <!-- PANEL A: WEBSHARE -->
      <div id="panelWebshare" style="display: none; background: #0b0f19; border: 1px solid var(--border); border-radius: 8px; padding: 16px; margin-bottom: 14px;">
        <div style="display: flex; justify-content: space-between; align-items: center; margin-bottom: 12px; flex-wrap: wrap; gap: 8px;">
          <div>
            <h3 style="margin: 0; font-size: 14px; color: #a5b4fc;">🌐 Webshare API Key Rotation Pool</h3>
            <div class="meta" style="font-size: 11px;">Add unlimited keys for automatic quota failover & rotation.</div>
          </div>
          <div style="display: flex; gap: 8px;">
            <button type="button" class="btn btn-sm btn-secondary" onclick="toggleWebshareBulkMode()">📋 Bulk Paste Mode</button>
            <button type="button" class="btn btn-sm" onclick="addWebshareKeyRow()">+ Add Key</button>
          </div>
        </div>

        <!-- Dynamic Key Rows Container -->
        <div id="webshareKeysListContainer" style="display: flex; flex-direction: column; gap: 8px; margin-bottom: 10px;">
          <!-- Rendered dynamically -->
        </div>

        <!-- Bulk Paste Box -->
        <div id="webshareBulkBox" style="display: none; margin-top: 10px; padding-top: 10px; border-top: 1px dashed var(--border);">
          <label style="font-size: 11px; color: var(--muted); font-weight: 600;">Bulk Paste Webshare Keys (one token per line):</label>
          <textarea id="webshareBulkTextarea" placeholder="token_alpha_12345&#10;token_beta_67890&#10;token_gamma_11223" style="width: 100%; height: 90px; background: #161f30; color: #a5b4fc; border: 1px solid var(--border); border-radius: 6px; padding: 8px; font-family: monospace; font-size: 12px; margin-top: 4px;"></textarea>
          <button type="button" class="btn btn-sm btn-secondary" onclick="applyWebshareBulkKeys()" style="margin-top: 6px;">⚡ Apply Bulk Keys</button>
        </div>
      </div>

      <!-- PANEL B: RESIDENTIAL GATEWAY -->
      <div id="panelResidential" style="display: none; background: #0b0f19; border: 1px solid var(--border); border-radius: 8px; padding: 16px; margin-bottom: 14px;">
        <h3 style="margin: 0 0 10px 0; font-size: 14px; color: #a5b4fc;">🏠 Custom Residential Gateway Connection</h3>
        <div style="display: grid; grid-template-columns: repeat(auto-fit, minmax(180px, 1fr)); gap: 12px; margin-bottom: 12px;">
          <div>
            <label style="font-size: 11px; color: var(--muted); font-weight: 600;">Gateway Host</label>
            <input type="text" id="cfgResHost" placeholder="e.g. gate.smartproxy.com" style="width: 100%; background: #161f30; color: white; border: 1px solid var(--border); padding: 8px 12px; border-radius: 6px; margin-top: 4px;">
          </div>
          <div>
            <label style="font-size: 11px; color: var(--muted); font-weight: 600;">Port</label>
            <input type="number" id="cfgResPort" placeholder="7000" style="width: 100%; background: #161f30; color: white; border: 1px solid var(--border); padding: 8px 12px; border-radius: 6px; margin-top: 4px;">
          </div>
          <div>
            <label style="font-size: 11px; color: var(--muted); font-weight: 600;">Username</label>
            <input type="text" id="cfgResUser" placeholder="User prefix" style="width: 100%; background: #161f30; color: white; border: 1px solid var(--border); padding: 8px 12px; border-radius: 6px; margin-top: 4px;">
          </div>
          <div>
            <label style="font-size: 11px; color: var(--muted); font-weight: 600;">Password</label>
            <input type="password" id="cfgResPass" placeholder="Password" style="width: 100%; background: #161f30; color: white; border: 1px solid var(--border); padding: 8px 12px; border-radius: 6px; margin-top: 4px;">
          </div>
        </div>
        <div style="display: grid; grid-template-columns: 1fr 1fr; gap: 12px;">
          <div>
            <label style="font-size: 11px; color: var(--muted); font-weight: 600;">Target Country (ISO code)</label>
            <input type="text" id="cfgResCountry" placeholder="id, us, de, etc." value="id" style="width: 100%; background: #161f30; color: white; border: 1px solid var(--border); padding: 8px 12px; border-radius: 6px; margin-top: 4px;">
          </div>
          <div>
            <label style="font-size: 11px; color: var(--muted); font-weight: 600;">Rotating Session Pool Count</label>
            <input type="number" id="cfgResCount" value="20" style="width: 100%; background: #161f30; color: white; border: 1px solid var(--border); padding: 8px 12px; border-radius: 6px; margin-top: 4px;">
          </div>
        </div>
      </div>

      <!-- PANEL C: BULK UPLOAD & CUSTOM LIST -->
      <div id="panelCustomList" style="display: none; background: #0b0f19; border: 1px solid var(--border); border-radius: 8px; padding: 16px; margin-bottom: 14px;">
        <div style="display: flex; justify-content: space-between; align-items: center; margin-bottom: 8px; flex-wrap: wrap; gap: 8px;">
          <div>
            <h3 style="margin: 0; font-size: 14px; color: #a5b4fc;">📄 Bulk Proxy Importer & Database Processing</h3>
            <div class="meta" style="font-size: 11px;">Otomatis parsing, deduplikasi, dan insert bersih ke database SQLite.</div>
          </div>
          <button type="button" class="btn btn-sm btn-secondary" onclick="toggleBulkUploadDrawer()">➕ Upload / Paste Proxies ▾</button>
        </div>

        <div id="proxyImportStats" style="display: none; margin-bottom: 10px; padding: 10px 14px; background: #10b98115; border: 1px solid #10b98144; border-radius: 6px; font-size: 12px; color: #34d399;"></div>

        <div id="bulkUploadDrawer" style="display: none; padding-top: 10px; border-top: 1px dashed var(--border);">
          <div style="display: flex; justify-content: space-between; align-items: center; margin-bottom: 8px; flex-wrap: wrap; gap: 8px;">
            <select id="bulkImportMode" style="background: #161f30; color: white; border: 1px solid var(--border); padding: 5px 10px; border-radius: 6px; font-size: 12px;">
              <option value="append">➕ Append & Upsert (Update & Tambah Baru)</option>
              <option value="replace">🔄 Replace (Ganti List Aktif)</option>
            </select>
            <div style="display: flex; gap: 8px;">
              <label class="btn btn-sm btn-secondary" style="cursor: pointer;">
                📂 Choose .txt File
                <input type="file" id="proxyFileInput" accept=".txt" onchange="handleProxyFileUpload(event)" style="display: none;">
              </label>
              <button class="btn btn-sm" onclick="bulkImportProxies()">⚡ Process & Save to DB</button>
            </div>
          </div>
          <p class="meta" style="font-size: 12px; margin-bottom: 8px;">Paste daftar proxy (Mendukung: <code>ip:port</code>, <code>ip:port:user:pass</code>, <code>http://user:pass@ip:port</code>, <code>socks5://ip:port</code>):</p>
          <textarea id="bulkProxyTextarea" placeholder="185.220.101.4:8080:username:password&#10;103.145.22.18:3128&#10;http://user:pass@194.36.89.12:8000" style="width: 100%; height: 110px; background: #161f30; color: #a5b4fc; border: 1px solid var(--border); border-radius: 6px; padding: 10px; font-family: monospace; font-size: 12px; resize: vertical;"></textarea>
        </div>
      </div>
    </div>

    <!-- CARD 5: CAPTCHA SOLVER -->
    <div class="card" style="margin-bottom: 0;">
      <div style="display: flex; justify-content: space-between; align-items: center; margin-bottom: 12px; flex-wrap: wrap; gap: 8px;">
        <div>
          <h2 style="margin: 0; font-size: 16px;">🧩 Audio CAPTCHA Solver Engine</h2>
          <div class="meta">Automated Speech-to-Text solving backend for challenge bypass</div>
        </div>
        <button type="button" class="btn btn-sm btn-secondary" id="btnToggleAudioEdit" onclick="toggleAudioEditMode()">✏️ Edit Settings</button>
      </div>

      <!-- VIEW 1: ACTIVE CONFIGURATION SUMMARY CARD -->
      <div id="audioSummaryView" style="background: #0b0f19; border: 1px solid var(--border); border-radius: 8px; padding: 14px; margin-bottom: 10px;">
        <div style="display: flex; justify-content: space-between; align-items: center; margin-bottom: 10px; flex-wrap: wrap; gap: 8px;">
          <div>
            <div style="display: flex; align-items: center; gap: 8px;">
              <span style="font-weight: 700; font-size: 14px; color: #f8fafc;" id="sumAudioProviderName">⚡ Groq Cloud (Whisper AI)</span>
              <span class="badge" style="background: #10b98122; color: #10b981; border: 1px solid #10b98144; font-size: 11px;">Active & Connected</span>
            </div>
            <div class="meta" id="sumAudioEndpoint" style="margin-top: 3px; font-family: monospace; font-size: 11px; color: #94a3b8;">https://api.groq.com/openai/v1</div>
          </div>
          <button type="button" class="btn btn-sm" id="btnSummaryTestAudio" onclick="testAudioModelSupport()" style="background: #1e293b; color: #38bdf8; border: 1px solid var(--border); font-size: 12px; font-weight: 600;">
            🎙️ Test Audio STT
          </button>
        </div>

        <div style="display: grid; grid-template-columns: repeat(auto-fit, minmax(140px, 1fr)); gap: 10px; background: #161f30; padding: 10px 12px; border-radius: 6px; border: 1px solid var(--border);">
          <div>
            <div style="font-size: 11px; color: var(--muted); font-weight: 600;">STT Model</div>
            <div style="font-size: 12px; font-weight: 700; color: #38bdf8;" id="sumAudioModel">whisper-large-v3-turbo</div>
          </div>
          <div>
            <div style="font-size: 11px; color: var(--muted); font-weight: 600;">API Key</div>
            <div style="font-size: 12px; font-family: monospace; color: #cbd5e1;" id="sumAudioKeyMask">gsk_••••••••</div>
          </div>
          <div>
            <div style="font-size: 11px; color: var(--muted); font-weight: 600;">Max Attempts</div>
            <div style="font-size: 12px; font-weight: 700; color: #f8fafc;" id="sumAudioAttempts">3 Retries</div>
          </div>
        </div>

        <div id="audioTestResultBox" style="display: none; margin-top: 10px; padding: 10px 12px; border-radius: 6px; font-size: 12px;"></div>
      </div>

      <!-- VIEW 2: EDITABLE SETTINGS FORM (COLLAPSIBLE) -->
      <div id="audioEditFormView" style="display: none; background: #0b0f19; border: 1px solid var(--border); border-radius: 8px; padding: 14px; margin-bottom: 10px;">
        <div style="margin-bottom: 12px;">
          <label style="font-size: 12px; color: var(--muted); font-weight: 600;">Audio Solver Provider</label>
          <select id="cfgCaptchaSolver" onchange="toggleCaptchaPanels()" style="width: 100%; background: #161f30; color: white; border: 1px solid var(--border); padding: 8px 12px; border-radius: 6px; margin-top: 4px;">
            <option value="openai_api">⚡ Custom / OpenAI-Compatible (Groq, OpenAI, Together, Local vLLM)</option>
            <option value="google_web">🌐 Google Web Speech (Free Built-in Fallback)</option>
            <option value="whisper">💻 Local Whisper Model (Offline Python Engine)</option>
            <option value="google_cloud">☁️ Google Cloud Speech-to-Text API</option>
          </select>
        </div>

        <!-- Custom OpenAI / Groq / Whisper API Configuration Subpanel -->
        <div id="panelCustomAudio" style="padding-top: 10px; border-top: 1px dashed var(--border); margin-bottom: 10px;">
          <div style="display: flex; justify-content: space-between; align-items: center; margin-bottom: 10px; flex-wrap: wrap; gap: 6px;">
            <span style="font-size: 11px; font-weight: 700; color: #a5b4fc;">⚡ 1-Click Provider Presets:</span>
            <div style="display: flex; gap: 6px; flex-wrap: wrap;">
              <button type="button" class="btn btn-sm btn-secondary" onclick="applyAudioPreset('groq')" style="font-size: 11px; padding: 2px 8px;">⚡ Groq Cloud</button>
              <button type="button" class="btn btn-sm btn-secondary" onclick="applyAudioPreset('openai')" style="font-size: 11px; padding: 2px 8px;">🟢 OpenAI</button>
              <button type="button" class="btn btn-sm btn-secondary" onclick="applyAudioPreset('local')" style="font-size: 11px; padding: 2px 8px;">💻 Local vLLM</button>
            </div>
          </div>

          <div style="margin-bottom: 10px;">
            <label style="font-size: 11px; color: var(--muted); font-weight: 600;">API Base URL</label>
            <input type="text" id="cfgCaptchaBaseUrl" placeholder="https://api.groq.com/openai/v1" style="width: 100%; background: #161f30; color: white; border: 1px solid var(--border); padding: 8px 12px; border-radius: 6px; font-family: monospace; font-size: 12px; margin-top: 4px;">
          </div>

          <div style="margin-bottom: 10px;">
            <label style="font-size: 11px; color: var(--muted); font-weight: 600;">API Key / Secret Token</label>
            <div style="display: flex; gap: 6px; margin-top: 4px;">
              <input type="password" id="cfgCaptchaApiKey" placeholder="gsk_... or sk-..." style="flex: 1; background: #161f30; color: white; border: 1px solid var(--border); padding: 8px 12px; border-radius: 6px; font-family: monospace; font-size: 12px;">
              <button type="button" class="btn btn-sm btn-secondary" onclick="toggleKeyVisibility(this)">👁️</button>
            </div>
          </div>

          <div style="margin-bottom: 10px;">
            <label style="font-size: 11px; color: var(--muted); font-weight: 600;">STT Model Name</label>
            <input type="text" id="cfgCaptchaModel" placeholder="whisper-large-v3-turbo" style="width: 100%; background: #161f30; color: white; border: 1px solid var(--border); padding: 8px 12px; border-radius: 6px; font-family: monospace; font-size: 12px; margin-top: 4px;">
          </div>
        </div>

        <div style="margin-bottom: 12px;">
          <label style="font-size: 11px; color: var(--muted); font-weight: 600;">Max Solver Attempts per CAPTCHA</label>
          <input type="number" id="cfgCaptchaAttempts" value="3" style="width: 100%; background: #161f30; color: white; border: 1px solid var(--border); padding: 8px 12px; border-radius: 6px; margin-top: 4px;">
        </div>

        <div id="audioTestResultBoxForm" style="display: none; margin-bottom: 12px; padding: 10px 12px; border-radius: 6px; font-size: 12px;"></div>

        <div style="display: flex; justify-content: space-between; align-items: center; flex-wrap: wrap; gap: 8px; border-top: 1px dashed var(--border); padding-top: 10px;">
          <button type="button" class="btn btn-sm" id="btnFormTestAudio" onclick="testAudioModelSupport()" style="background: #1e293b; color: #38bdf8; border: 1px solid var(--border); font-size: 12px; font-weight: 600;">
            🎙️ Test Audio STT Connection
          </button>
          <button type="button" class="btn btn-sm btn-secondary" onclick="toggleAudioEditMode(false)">Done Editing</button>
        </div>
      </div>
    </div>
  </div>
</div>

<script>
  let lastLogID = 0;
  let currentConcurrency = {{.Concurrency}};

  // --- Generic Client-Side Paginator ---
  class TablePaginator {
    constructor(tableId, pageSizeSelectId, pageInfoId, prevBtnId, nextBtnId, pageNumId, defaultSize) {
      this.table = document.getElementById(tableId);
      this.pageSizeSelect = document.getElementById(pageSizeSelectId);
      this.pageInfo = document.getElementById(pageInfoId);
      this.prevBtn = document.getElementById(prevBtnId);
      this.nextBtn = document.getElementById(nextBtnId);
      this.pageNum = document.getElementById(pageNumId);
      this.currentPage = 1;
      this.pageSize = defaultSize || 25;
      this.allRows = [];
      this.filteredRows = [];
      this.init();
    }

    init() {
      if (this.pageSizeSelect) {
        this.pageSize = parseInt(this.pageSizeSelect.value, 10) || this.pageSize;
      }
      if (this.table) {
        this.allRows = Array.from(this.table.querySelectorAll('tbody tr'));
        this.filteredRows = [...this.allRows];
        this.render();
      }
    }

    setFilteredRows(rows) {
      this.filteredRows = rows;
      this.currentPage = 1;
      this.render();
    }

    changePageSize() {
      this.pageSize = parseInt(this.pageSizeSelect.value, 10);
      this.currentPage = 1;
      this.render();
    }

    prevPage() {
      if (this.currentPage > 1) {
        this.currentPage--;
        this.render();
      }
    }

    nextPage() {
      const maxPages = Math.ceil(this.filteredRows.length / this.pageSize) || 1;
      if (this.currentPage < maxPages) {
        this.currentPage++;
        this.render();
      }
    }

    render() {
      const total = this.filteredRows.length;
      const maxPages = Math.ceil(total / this.pageSize) || 1;
      if (this.currentPage > maxPages) this.currentPage = maxPages;
      if (this.currentPage < 1) this.currentPage = 1;

      const start = (this.currentPage - 1) * this.pageSize;
      const end = Math.min(start + this.pageSize, total);

      // Hide all rows first
      this.allRows.forEach(r => r.style.display = 'none');
      // Show sliced rows from filtered set
      for (let i = start; i < end; i++) {
        if (this.filteredRows[i]) this.filteredRows[i].style.display = '';
      }

      // Update info & buttons
      if (this.pageInfo) {
        if (total === 0) {
          this.pageInfo.innerText = 'Showing 0 of 0';
        } else {
          this.pageInfo.innerText = 'Showing ' + (start + 1) + ' to ' + end + ' of ' + total;
        }
      }
      if (this.prevBtn) this.prevBtn.disabled = (this.currentPage <= 1);
      if (this.nextBtn) this.nextBtn.disabled = (this.currentPage >= maxPages);
      if (this.pageNum) this.pageNum.innerText = 'Page ' + this.currentPage + ' of ' + maxPages;
    }
  }

  let artPaginator, dailyPaginator, proxyPaginator;

  function showTab(tabId) {
    document.querySelectorAll('.tab-btn').forEach(b => b.classList.remove('active'));
    document.querySelectorAll('.tab-content').forEach(c => c.classList.remove('active'));
    event.target.classList.add('active');
    document.getElementById(tabId).classList.add('active');
    if (tabId === 'tab-settings') loadConfigVisual();
  }

  let currentDomains = [];

  function loadConfigVisual() {
    fetch('/api/config/json').then(r => r.json()).then(cfg => {
      if (!cfg) return;
      currentDomains = cfg.domains || ['bagasunix.com'];
      renderDomainTags();

      if (cfg.engine_ratio) {
        document.getElementById('cfgGoogle').value = cfg.engine_ratio.google || 70;
        document.getElementById('cfgBing').value = cfg.engine_ratio.bing || 10;
        document.getElementById('cfgDirect').value = cfg.engine_ratio.direct || 10;
        document.getElementById('cfgSocial').value = cfg.engine_ratio.social || 10;
        updateSliders();
      }

      if (cfg.scheduler) {
        document.getElementById('cfgMinCooldown').value = cfg.scheduler.min_cooldown_seconds || 30;
        document.getElementById('cfgMaxCooldown').value = cfg.scheduler.max_cooldown_seconds || 120;
        document.getElementById('cfgMaxPerProxy').value = cfg.scheduler.max_search_per_proxy || 5;
        document.getElementById('cfgActiveStart').value = cfg.scheduler.active_hours_start !== undefined ? cfg.scheduler.active_hours_start : 7;
        document.getElementById('cfgActiveEnd').value = cfg.scheduler.active_hours_end !== undefined ? cfg.scheduler.active_hours_end : 23;
        document.getElementById('cfgPreSearch').checked = (cfg.scheduler.pre_search_enabled !== false);
      }

      if (cfg.proxy) {
        document.getElementById('cfgProxyProvider').value = cfg.proxy.provider || 'webshare';
        currentWebshareKeys = cfg.proxy.webshare_api_keys || [];
        if (currentWebshareKeys.length === 0 && cfg.proxy.webshare_api_key) {
          currentWebshareKeys = [cfg.proxy.webshare_api_key];
        }
        renderWebshareKeyRows();

        document.getElementById('cfgResHost').value = cfg.proxy.residential_host || 'gate.smartproxy.com';
        document.getElementById('cfgResPort').value = cfg.proxy.residential_port || 7000;
        document.getElementById('cfgResUser').value = cfg.proxy.residential_user || '';
        document.getElementById('cfgResPass').value = cfg.proxy.residential_password || '';
        document.getElementById('cfgResCountry').value = cfg.proxy.residential_country || 'id';
        document.getElementById('cfgResCount').value = cfg.proxy.residential_session_count || 20;
        toggleProxyPanels();
      }

      if (cfg.captcha) {
        document.getElementById('cfgCaptchaSolver').value = cfg.captcha.solver || 'openai_api';
        document.getElementById('cfgCaptchaAttempts').value = cfg.captcha.max_attempts || 3;
        document.getElementById('cfgCaptchaBaseUrl').value = cfg.captcha.openai_base_url || 'https://api.groq.com/openai/v1';
        document.getElementById('cfgCaptchaApiKey').value = cfg.captcha.openai_api_key || '';
        document.getElementById('cfgCaptchaModel').value = cfg.captcha.openai_model || 'whisper-large-v3-turbo';
        toggleCaptchaPanels();
        updateAudioSummaryView();
        toggleAudioEditMode(false);
      }
    });
  }

  // --- Dynamic Webshare Multi-Key Management ---
  let currentWebshareKeys = [];

  function renderWebshareKeyRows() {
    const container = document.getElementById('webshareKeysListContainer');
    if (!container) return;
    container.innerHTML = '';
    if (currentWebshareKeys.length === 0) {
      currentWebshareKeys.push('');
    }
    currentWebshareKeys.forEach((keyVal, idx) => {
      const row = document.createElement('div');
      row.style.cssText = 'display: flex; gap: 8px; align-items: center;';
      row.innerHTML = 
        '<span class="badge" style="width: 80px; text-align: center; font-size: 11px;">Key #' + (idx + 1) + '</span>' +
        '<input type="password" value="' + keyVal + '" placeholder="Webshare API Token..." oninput="updateWebshareKeyVal(' + idx + ', this.value)" style="flex: 1; background: #161f30; color: white; border: 1px solid var(--border); padding: 8px 12px; border-radius: 6px; font-family: monospace; font-size: 12px;">' +
        '<button type="button" class="btn btn-sm btn-secondary" onclick="toggleKeyVisibility(this)" title="Show/Hide Key">👁️</button>' +
        '<button type="button" class="btn btn-sm" onclick="removeWebshareKeyRow(' + idx + ')" style="background: #ef444422; color: #f87171; border: 1px solid #ef444444;" title="Delete Key">&times;</button>';
      container.appendChild(row);
    });
  }

  function addWebshareKeyRow() {
    currentWebshareKeys.push('');
    renderWebshareKeyRows();
  }

  function removeWebshareKeyRow(idx) {
    if (currentWebshareKeys.length > 1) {
      currentWebshareKeys.splice(idx, 1);
    } else {
      currentWebshareKeys[0] = '';
    }
    renderWebshareKeyRows();
  }

  function updateWebshareKeyVal(idx, val) {
    currentWebshareKeys[idx] = val.trim();
  }

  function toggleKeyVisibility(btn) {
    const input = btn.previousElementSibling;
    if (input.type === 'password') {
      input.type = 'text';
      btn.innerText = '🔒';
    } else {
      input.type = 'password';
      btn.innerText = '👁️';
    }
  }

  function toggleWebshareBulkMode() {
    const box = document.getElementById('webshareBulkBox');
    box.style.display = (box.style.display === 'none' || !box.style.display) ? 'block' : 'none';
  }

  function applyWebshareBulkKeys() {
    const text = document.getElementById('webshareBulkTextarea').value.trim();
    if (text) {
      const lines = text.split('\n').map(s => s.trim()).filter(s => s.length > 0);
      if (lines.length > 0) {
        currentWebshareKeys = lines;
        renderWebshareKeyRows();
        document.getElementById('webshareBulkBox').style.display = 'none';
        alert('✨ Added ' + lines.length + ' Webshare API keys to the rotation pool!');
      }
    }
  }

  function toggleProxyPanels() {
    const provider = document.getElementById('cfgProxyProvider').value;
    document.getElementById('panelWebshare').style.display = (provider === 'webshare') ? 'block' : 'none';
    document.getElementById('panelResidential').style.display = (provider === 'residential') ? 'block' : 'none';
    document.getElementById('panelCustomList').style.display = (provider === 'custom_file') ? 'block' : 'none';
  }

  function handleProxyFileUpload(event) {
    const file = event.target.files[0];
    if (file) {
      const reader = new FileReader();
      reader.onload = function(e) {
        document.getElementById('bulkProxyTextarea').value = e.target.result;
      };
      reader.readAsText(file);
    }
  }

  function toggleBulkUploadDrawer() {
    const d = document.getElementById('bulkUploadDrawer');
    if (d) d.style.display = (d.style.display === 'none' || d.style.display === '') ? 'block' : 'none';
  }

  function bulkImportProxies() {
    const text = document.getElementById('bulkProxyTextarea').value.trim();
    if (!text) {
      alert('Silakan masukkan atau upload file daftar proxy terlebih dahulu!');
      return;
    }
    const mode = document.getElementById('bulkImportMode').value;
    const statsBox = document.getElementById('proxyImportStats');
    statsBox.style.display = 'none';

    fetch('/api/proxies/import', {
      method: 'POST',
      headers: {'Content-Type': 'application/json'},
      body: JSON.stringify({proxy_list: text, mode: mode})
    }).then(r => r.json()).then(data => {
      if (data.status === 'ok') {
        statsBox.style.display = 'block';
        statsBox.innerHTML = 
          '<strong>✅ Sukses Memproses ke Database SQLite:</strong><br>' +
          '• Total Diproses: <b>' + data.total_processed + '</b><br>' +
          '• Proxy Baru Ditambahkan: <b>' + data.new_added + '</b><br>' +
          '• Proxy Terupdate: <b>' + data.updated + '</b><br>' +
          '• Duplikat Diabaikan: <b>' + data.duplicates + '</b>';
        // Clear textarea and collapse drawer
        document.getElementById('bulkProxyTextarea').value = '';
        const fileInp = document.getElementById('proxyFileInput');
        if (fileInp) fileInp.value = '';
        const drawer = document.getElementById('bulkUploadDrawer');
        if (drawer) drawer.style.display = 'none';
        alert('🎉 ' + data.message);
      } else {
        alert('Gagal memproses proxy: ' + (data.message || 'Error'));
      }
    }).catch(e => alert('Error importing proxies: ' + e));
  }

  function updateSliders() {
    const g = parseInt(document.getElementById('cfgGoogle').value, 10);
    const b = parseInt(document.getElementById('cfgBing').value, 10);
    const d = parseInt(document.getElementById('cfgDirect').value, 10);
    const s = parseInt(document.getElementById('cfgSocial').value, 10);

    document.getElementById('googleVal').innerText = g + '%';
    document.getElementById('bingVal').innerText = b + '%';
    document.getElementById('directVal').innerText = d + '%';
    document.getElementById('socialVal').innerText = s + '%';

    const total = g + b + d + s;
    const badge = document.getElementById('totalRatioBadge');
    if (total === 100) {
      badge.className = 'badge';
      badge.style.background = '#10b98122';
      badge.style.color = '#10b981';
      badge.innerText = '100% (Balanced)';
    } else {
      badge.className = 'badge';
      badge.style.background = '#ef444422';
      badge.style.color = '#ef4444';
      badge.innerText = total + '% (Adjust to 100%)';
    }
  }

  function renderDomainTags() {
    const container = document.getElementById('domainTagsContainer');
    container.innerHTML = '';
    currentDomains.forEach((dom, idx) => {
      const tag = document.createElement('span');
      tag.style.cssText = 'background: #1e293b; color: #a5b4fc; border: 1px solid var(--border); padding: 4px 10px; border-radius: 6px; font-size: 13px; display: inline-flex; align-items: center; gap: 6px; font-weight: 600;';
      tag.innerHTML = dom + ' <span onclick="removeDomainTag(' + idx + ')" style="cursor: pointer; color: #f87171; font-weight: bold;">&times;</span>';
      container.appendChild(tag);
    });
  }

  function addDomainTag() {
    const inp = document.getElementById('newDomainInput');
    const val = inp.value.trim();
    if (val && !currentDomains.includes(val)) {
      currentDomains.push(val);
      renderDomainTags();
      inp.value = '';
    }
  }

  function removeDomainTag(idx) {
    currentDomains.splice(idx, 1);
    renderDomainTags();
  }

  function saveConfigVisual() {
    const wsKeys = currentWebshareKeys.map(k => k.trim()).filter(k => k.length > 0);

    const payload = {
      domains: currentDomains,
      engine_ratio: {
        google: parseInt(document.getElementById('cfgGoogle').value, 10),
        bing: parseInt(document.getElementById('cfgBing').value, 10),
        direct: parseInt(document.getElementById('cfgDirect').value, 10),
        social: parseInt(document.getElementById('cfgSocial').value, 10)
      },
      scheduler: {
        min_cooldown_seconds: parseInt(document.getElementById('cfgMinCooldown').value, 10),
        max_cooldown_seconds: parseInt(document.getElementById('cfgMaxCooldown').value, 10),
        max_search_per_proxy: parseInt(document.getElementById('cfgMaxPerProxy').value, 10),
        active_hours_start: parseInt(document.getElementById('cfgActiveStart').value, 10),
        active_hours_end: parseInt(document.getElementById('cfgActiveEnd').value, 10),
        pre_search_enabled: document.getElementById('cfgPreSearch').checked
      },
      proxy: {
        provider: document.getElementById('cfgProxyProvider').value,
        webshare_api_keys: wsKeys,
        residential_host: document.getElementById('cfgResHost').value.trim(),
        residential_port: parseInt(document.getElementById('cfgResPort').value, 10) || 7000,
        residential_user: document.getElementById('cfgResUser').value.trim(),
        residential_password: document.getElementById('cfgResPass').value.trim(),
        residential_country: document.getElementById('cfgResCountry').value.trim(),
        residential_session_count: parseInt(document.getElementById('cfgResCount').value, 10) || 20
      },
      captcha: {
        enabled: true,
        solver: document.getElementById('cfgCaptchaSolver').value,
        max_attempts: parseInt(document.getElementById('cfgCaptchaAttempts').value, 10) || 3,
        openai_base_url: document.getElementById('cfgCaptchaBaseUrl').value.trim(),
        openai_api_key: document.getElementById('cfgCaptchaApiKey').value.trim(),
        openai_model: document.getElementById('cfgCaptchaModel').value.trim()
      }
    };

    fetch('/api/config/json', {
      method: 'POST',
      headers: {'Content-Type': 'application/json'},
      body: JSON.stringify(payload)
    }).then(r => r.json()).then(data => {
      updateAudioSummaryView();
      toggleAudioEditMode(false);
      alert('✨ ' + (data.message || 'Settings saved & applied!'));
    }).catch(e => alert('Error saving settings: ' + e));
  }

  function updateAudioSummaryView() {
    const solver = document.getElementById('cfgCaptchaSolver').value;
    const baseUrl = document.getElementById('cfgCaptchaBaseUrl').value.trim();
    const apiKey = document.getElementById('cfgCaptchaApiKey').value.trim();
    const model = document.getElementById('cfgCaptchaModel').value.trim();
    const attempts = document.getElementById('cfgCaptchaAttempts').value;

    let provName = '⚡ Custom OpenAI-Compatible STT';
    if (solver === 'openai_api') {
      if (baseUrl.includes('groq')) provName = '⚡ Groq Cloud (Whisper Large V3)';
      else if (baseUrl.includes('openai.com')) provName = '🟢 OpenAI Official (Whisper-1)';
      else if (baseUrl.includes('localhost') || baseUrl.includes('127.0.0.1')) provName = '💻 Local vLLM/Ollama Whisper';
    } else if (solver === 'google_web') {
      provName = '🌐 Google Web Speech (Free Built-in)';
    } else if (solver === 'whisper') {
      provName = '💻 Local Whisper Python Model';
    } else if (solver === 'google_cloud') {
      provName = '☁️ Google Cloud Speech API';
    }

    const nameEl = document.getElementById('sumAudioProviderName');
    if (nameEl) nameEl.innerText = provName;

    const epEl = document.getElementById('sumAudioEndpoint');
    if (epEl) epEl.innerText = (solver === 'openai_api') ? baseUrl : ('Built-in Engine: ' + solver);

    const modelEl = document.getElementById('sumAudioModel');
    if (modelEl) modelEl.innerText = (solver === 'openai_api') ? (model || 'whisper-large-v3-turbo') : 'Built-in';

    const keyEl = document.getElementById('sumAudioKeyMask');
    if (keyEl) {
      if (solver !== 'openai_api') {
        keyEl.innerText = 'No Key Required';
      } else if (apiKey.length > 8) {
        keyEl.innerText = apiKey.substring(0, 4) + '••••••••' + apiKey.substring(apiKey.length - 3);
      } else if (apiKey.length > 0) {
        keyEl.innerText = '••••••••';
      } else {
        keyEl.innerText = 'Not Set';
      }
    }

    const attEl = document.getElementById('sumAudioAttempts');
    if (attEl) attEl.innerText = attempts + ' Retries';
  }

  let isAudioEditing = false;
  function toggleAudioEditMode(forceState) {
    if (typeof forceState === 'boolean') {
      isAudioEditing = forceState;
    } else {
      isAudioEditing = !isAudioEditing;
    }
    const sumView = document.getElementById('audioSummaryView');
    const formView = document.getElementById('audioEditFormView');
    const btn = document.getElementById('btnToggleAudioEdit');

    if (isAudioEditing) {
      if (sumView) sumView.style.display = 'none';
      if (formView) formView.style.display = 'block';
      if (btn) btn.innerText = '👁️ View Summary';
    } else {
      updateAudioSummaryView();
      if (sumView) sumView.style.display = 'block';
      if (formView) formView.style.display = 'none';
      if (btn) btn.innerText = '✏️ Edit Settings';
    }
  }

  function toggleCaptchaPanels() {
    const solver = document.getElementById('cfgCaptchaSolver').value;
    document.getElementById('panelCustomAudio').style.display = (solver === 'openai_api') ? 'block' : 'none';
  }

  function applyAudioPreset(preset) {
    if (preset === 'groq') {
      document.getElementById('cfgCaptchaBaseUrl').value = 'https://api.groq.com/openai/v1';
      document.getElementById('cfgCaptchaModel').value = 'whisper-large-v3-turbo';
    } else if (preset === 'openai') {
      document.getElementById('cfgCaptchaBaseUrl').value = 'https://api.openai.com/v1';
      document.getElementById('cfgCaptchaModel').value = 'whisper-1';
    } else if (preset === 'local') {
      document.getElementById('cfgCaptchaBaseUrl').value = 'http://localhost:8000/v1';
      document.getElementById('cfgCaptchaModel').value = 'whisper-large-v3';
    }
  }

  function testAudioModelSupport() {
    const summaryBtn = document.getElementById('btnSummaryTestAudio');
    const formBtn = document.getElementById('btnFormTestAudio');
    const resBox1 = document.getElementById('audioTestResultBox');
    const resBox2 = document.getElementById('audioTestResultBoxForm');
    const baseUrl = (document.getElementById('cfgCaptchaBaseUrl') && document.getElementById('cfgCaptchaBaseUrl').value.trim()) || 'https://api.groq.com/openai/v1';
    const apiKey = (document.getElementById('cfgCaptchaApiKey') && document.getElementById('cfgCaptchaApiKey').value.trim()) || '';
    const model = (document.getElementById('cfgCaptchaModel') && document.getElementById('cfgCaptchaModel').value.trim()) || 'whisper-large-v3-turbo';

    const setButtons = (text, disabled) => {
      if (summaryBtn) { summaryBtn.innerText = text; summaryBtn.disabled = disabled; }
      if (formBtn) { formBtn.innerText = text; formBtn.disabled = disabled; }
    };

    const renderBoxes = (html, bg, border, color) => {
      [resBox1, resBox2].forEach(box => {
        if (box) {
          box.style.display = 'block';
          box.style.background = bg;
          box.style.border = '1px solid ' + border;
          box.style.color = color;
          box.innerHTML = html;
        }
      });
    };

    setButtons('⏳ Testing STT...', true);
    renderBoxes('🔄 Mengirim benchmark audio 16kHz ke <code>' + baseUrl + '/audio/transcriptions</code>...', '#1e293b', 'var(--border)', '#cbd5e1');

    fetch('/api/captcha/test-audio', {
      method: 'POST',
      headers: {'Content-Type': 'application/json'},
      body: JSON.stringify({
        base_url: baseUrl,
        api_key: apiKey,
        model: model,
        language: 'en'
      })
    }).then(r => r.json()).then(data => {
      setButtons('🎙️ Test Audio STT', false);
      if (data.status === 'ok') {
        renderBoxes(
          '<strong>✅ Audio STT Model Terverifikasi & Didukung!</strong><br>' +
          '• Model: <b>' + data.model + '</b> (STT Supported)<br>' +
          '• Latency: <b>' + data.latency_ms + ' ms</b><br>' +
          '• Transkripsi Output: <i>"' + (data.transcribed_text || '[Decoded Sound]') + '"</i>',
          '#10b98118', '#10b98144', '#34d399'
        );
      } else {
        renderBoxes(
          '<strong>❌ Pengujian Gagal / Model Tidak Support:</strong><br>' +
          '• Error: ' + (data.message || 'Unknown error') + '<br>' +
          '• Tips: Pastikan nama model mendukung endpoint STT audio (misal: <code>whisper-large-v3-turbo</code> atau <code>whisper-1</code>).',
          '#ef444418', '#ef444444', '#f87171'
        );
      }
    }).catch(e => {
      setButtons('🎙️ Test Audio STT', false);
      renderBoxes('<strong>❌ Error Request:</strong> ' + e, '#ef444418', '#ef444444', '#f87171');
    });
  }

  function changeConcurrency(delta) {
    let next = currentConcurrency + delta;
    if (next < 1) next = 1;
    if (next > 10) next = 10;
    fetch('/api/fleet/concurrency', {
      method: 'POST',
      headers: {'Content-Type': 'application/json'},
      body: JSON.stringify({concurrency: next})
    }).then(r => r.json()).then(data => {
      currentConcurrency = data.concurrency;
      document.getElementById('concurrencyCount').innerText = currentConcurrency;
      updateFleetGrid();
    });
  }

  function updateFleetGrid() {
    fetch('/api/fleet/status').then(r => r.json()).then(data => {
      const container = document.getElementById('fleetGridContainer');
      container.innerHTML = '';
      data.workers.forEach(w => {
        const card = document.createElement('div');
        card.className = 'worker-card';
        card.innerHTML = 
          '<div class="head">' +
            '<span class="title">🤖 WORKER #' + w.id + '</span>' +
            '<span class="status-tag status-' + w.status + '">' + w.status + '</span>' +
          '</div>' +
          '<div class="proxy">🛡️ Proxy: ' + (w.proxy_country || 'ID') + ' ' + (w.proxy_ip || '127.0.0.1') + '</div>' +
          '<div class="action">' + (w.current_action || 'Standby') + '</div>' +
          '<div class="progress-bg">' +
            '<div class="progress-bar" style="width: ' + (w.progress_percent || 0) + '%;"></div>' +
          '</div>';
        container.appendChild(card);
      });
    });
  }

  function pollLogs() {
    const filter = document.getElementById('workerLogFilter').value;
    fetch('/api/logs?since=' + lastLogID + '&worker=' + filter).then(r => r.json()).then(logs => {
      if (logs && logs.length > 0) {
        const box = document.getElementById('terminalBox');
        logs.forEach(l => {
          if (l.id > lastLogID) lastLogID = l.id;
          const p = document.createElement('div');
          p.className = 'log-line log-' + l.level;
          const time = new Date(l.timestamp).toLocaleTimeString();
          p.innerText = '[' + time + '] [Worker-' + (l.worker_id || 'SYS') + '][' + (l.country || 'ID') + '] ' + l.message;
          box.appendChild(p);
        });
        box.scrollTop = box.scrollHeight;
      }
    });
  }

  function clearLogs() {
    document.getElementById('terminalBox').innerHTML = '';
  }

  function triggerSearch(articleId) {
    fetch('/api/action/trigger_search', {
      method: 'POST',
      headers: {'Content-Type': 'application/json'},
      body: JSON.stringify({article_id: articleId})
    }).then(r => r.json()).then(data => {
      alert('⚡ ' + data.message);
    });
  }

  function resetAnalyticsStats() {
    if (!confirm('Apakah kamu yakin ingin mereset seluruh metrik Analytics dan riwayat task ke 0?')) return;
    fetch('/api/action/reset-stats', {method: 'POST'})
      .then(r => r.json())
      .then(data => {
        alert('✨ ' + data.message);
        location.reload();
      })
      .catch(e => alert('Error resetting stats: ' + e));
  }



  // Articles Pagination
  function filterArticles() {
    const input = document.getElementById('articleSearchInput').value.toLowerCase();
    if (!artPaginator) return;
    const rows = artPaginator.allRows.filter(r => {
      return r.cells[0].innerText.toLowerCase().includes(input);
    });
    artPaginator.setFilteredRows(rows);
  }
  function changeArticlePageSize() { if (artPaginator) artPaginator.changePageSize(); }
  function prevArticlePage() { if (artPaginator) artPaginator.prevPage(); }
  function nextArticlePage() { if (artPaginator) artPaginator.nextPage(); }

  // Daily Stats Pagination
  function changeDailyPageSize() { if (dailyPaginator) dailyPaginator.changePageSize(); }
  function prevDailyPage() { if (dailyPaginator) dailyPaginator.prevPage(); }
  function nextDailyPage() { if (dailyPaginator) dailyPaginator.nextPage(); }

  // Proxies Pagination
  function changeProxyPageSize() { if (proxyPaginator) proxyPaginator.changePageSize(); }
  function prevProxyPage() { if (proxyPaginator) proxyPaginator.prevPage(); }
  function nextProxyPage() { if (proxyPaginator) proxyPaginator.nextPage(); }

  // Init Paginators on DOM load
  window.addEventListener('DOMContentLoaded', () => {
    artPaginator = new TablePaginator('articlesTable', 'articlePageSize', 'articlePageInfo', 'artPrevBtn', 'artNextBtn', 'artPageNumbers', 25);
    dailyPaginator = new TablePaginator('dailyTable', 'dailyPageSize', 'dailyPageInfo', 'dailyPrevBtn', 'dailyNextBtn', 'dailyPageNumbers', 10);
    proxyPaginator = new TablePaginator('proxiesTable', 'proxyPageSize', 'proxyPageInfo', 'proxyPrevBtn', 'proxyNextBtn', 'proxyPageNumbers', 10);
    checkFleetEngineStatus();
  });

  let isFleetRunning = false;

  function checkFleetEngineStatus() {
    fetch('/api/fleet/engine-status').then(r => r.json()).then(data => {
      isFleetRunning = data.running;
      updateFleetEngineUI(data);
    }).catch(e => console.error('check fleet status:', e));
  }

  function updateFleetEngineUI(data) {
    const headBtn = document.getElementById('btnHeaderFleetToggle');
    const mainBtn = document.getElementById('btnMainFleetToggle');
    const dot = document.getElementById('fleetStatusDot');
    const title = document.getElementById('fleetStatusTitle');
    const badge = document.getElementById('fleetStatusBadge');
    const sub = document.getElementById('fleetStatusSub');

    if (data.running) {
      if (headBtn) {
        headBtn.innerHTML = '⏹️ Stop Bot Fleet';
        headBtn.style.background = 'linear-gradient(135deg, #ef4444, #dc2626)';
        headBtn.style.boxShadow = '0 4px 15px rgba(239, 68, 68, 0.3)';
      }
      if (mainBtn) {
        mainBtn.innerHTML = '⏹️ Stop Bot Fleet Engine';
        mainBtn.style.background = 'linear-gradient(135deg, #ef4444, #dc2626)';
        mainBtn.style.boxShadow = '0 4px 15px rgba(239, 68, 68, 0.3)';
      }
      if (dot) dot.style.background = '#10b981';
      if (title) title.innerText = 'Bot Fleet Engine: RUNNING (Autonomous)';
      if (badge) {
        badge.className = 'badge';
        badge.style.background = '#10b98122';
        badge.style.color = '#10b981';
        badge.style.border = '1px solid #10b98144';
        badge.innerText = 'LIVE RUNNING';
      }
      if (sub) sub.innerText = 'Campaign aktif: Worker PID ' + data.worker_pid + ' • Orchestrator PID ' + data.orchestrator_pid + ' • Multi-stream live terminal aktif di bawah.';
    } else {
      if (headBtn) {
        headBtn.innerHTML = '▶️ Start Bot Fleet';
        headBtn.style.background = 'linear-gradient(135deg, #10b981, #059669)';
        headBtn.style.boxShadow = '0 4px 15px rgba(16, 185, 129, 0.3)';
      }
      if (mainBtn) {
        mainBtn.innerHTML = '▶️ Start Bot Fleet Engine';
        mainBtn.style.background = 'linear-gradient(135deg, #10b981, #059669)';
        mainBtn.style.boxShadow = '0 4px 15px rgba(16, 185, 129, 0.3)';
      }
      if (dot) dot.style.background = '#94a3b8';
      if (title) title.innerText = 'Bot Fleet Engine: Standby / Idle';
      if (badge) {
        badge.className = 'badge';
        badge.style.background = '#64748b22';
        badge.style.color = '#94a3b8';
        badge.style.border = '1px solid #64748b44';
        badge.innerText = 'STANDBY';
      }
      if (sub) sub.innerText = 'Tekan tombol Start untuk menjalankan Go Orchestrator & Python Stealth Worker secara otomatis.';
    }
  }

  function toggleFleetEngine() {
    const headBtn = document.getElementById('btnHeaderFleetToggle');
    const mainBtn = document.getElementById('btnMainFleetToggle');
    if (headBtn) headBtn.disabled = true;
    if (mainBtn) mainBtn.disabled = true;

    const endpoint = isFleetRunning ? '/api/fleet/stop' : '/api/fleet/start';
    fetch(endpoint, {method: 'POST'}).then(r => r.json()).then(data => {
      if (headBtn) headBtn.disabled = false;
      if (mainBtn) mainBtn.disabled = false;
      if (data.status === 'ok') {
        checkFleetEngineStatus();
      } else {
        alert(data.message || 'Error toggling fleet engine');
        checkFleetEngineStatus();
      }
    }).catch(e => {
      if (headBtn) headBtn.disabled = false;
      if (mainBtn) mainBtn.disabled = false;
      alert('Request error: ' + e);
    });
  }

  // Init intervals
  setInterval(updateFleetGrid, 2000);
  setInterval(pollLogs, 1500);
  setInterval(checkFleetEngineStatus, 3000);
  updateFleetGrid();
  checkFleetEngineStatus();

  // Chart.js initialization
  const ctx = document.getElementById('perfChart').getContext('2d');
  const labels = [{{range .Daily}}"{{.Date}}",{{end}}];
  const searches = [{{range .Daily}}{{.TotalSearch}},{{end}}];
  const successes = [{{range .Daily}}{{.Success}},{{end}}];

  new Chart(ctx, {
    type: 'line',
    data: {
      labels: labels,
      datasets: [
        { label: 'Total Searches', data: searches, borderColor: '#6366f1', backgroundColor: '#6366f122', tension: 0.3, fill: true },
        { label: 'Successes', data: successes, borderColor: '#10b981', backgroundColor: '#10b98122', tension: 0.3, fill: true }
      ]
    },
    options: {
      responsive: true,
      maintainAspectRatio: false,
      scales: {
        x: { grid: { color: '#33415544' }, ticks: { color: '#94a3b8' } },
        y: { grid: { color: '#33415544' }, ticks: { color: '#94a3b8' }, beginAtZero: true }
      },
      plugins: {
        legend: { labels: { color: '#f8fafc' } }
      }
    }
  });
  if ('serviceWorker' in navigator) {
    window.addEventListener('load', () => {
      navigator.serviceWorker.register('/sw.js');
    });
  }
</script>
</body>
</html>
`))
