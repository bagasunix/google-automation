package orchestrator

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"time"
)

// fleetStatusFile is where live worker state is mirrored to disk so any
// process can read it. The orchestrator loop that actually updates worker
// state may run as the dashboard's own child process, as an independently
// launched binary (e.g. via scripts/run.sh or systemd), or in-process — in
// every case the FleetManager singleton it updates lives only in ITS OWN
// process memory. The dashboard's HTTP handlers run in a different process
// and can never see those updates directly, so the grid would stay stuck on
// the default IDLE/Standby state forever. Mirroring to disk (same pattern as
// data/bandwidth.json, shared with the Python worker) lets the dashboard
// show real data regardless of which process is actually running the fleet.
const fleetStatusFile = "data/fleet_status.json"

// FleetSnapshot is the on-disk representation of live fleet state.
type FleetSnapshot struct {
	UpdatedAt   time.Time      `json:"updated_at"`
	PID         int            `json:"pid"`
	Concurrency int            `json:"concurrency"`
	Workers     []*WorkerState `json:"workers"`
}

// fleetControlFile mirrors the desired concurrency in the opposite direction
// of fleetStatusFile: the dashboard writes it when the user adjusts the
// worker count in the UI, and whichever process is actually running the
// orchestrator loop polls it and applies the change to its own FleetManager.
// Without this, the dashboard's +/- control only touches its own (inert)
// in-process FleetManager and never reaches the real orchestrator process.
const fleetControlFile = "data/fleet_control.json"

// FleetControl is the on-disk representation of a requested concurrency change.
type FleetControl struct {
	Concurrency int       `json:"concurrency"`
	RequestedAt time.Time `json:"requested_at"`
}

// WriteFleetControl persists a desired concurrency (clamped to 1-10) for the
// running orchestrator process to pick up.
func WriteFleetControl(baseDir string, concurrency int) error {
	if concurrency < 1 {
		concurrency = 1
	}
	if concurrency > 10 {
		concurrency = 10
	}
	data, err := json.MarshalIndent(FleetControl{
		Concurrency: concurrency,
		RequestedAt: time.Now(),
	}, "", "  ")
	if err != nil {
		return err
	}
	path := filepath.Join(baseDir, fleetControlFile)
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, data, 0o644); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}

// ReadFleetControl reads the last requested concurrency, if any has ever
// been written.
func ReadFleetControl(baseDir string) (*FleetControl, error) {
	path := filepath.Join(baseDir, fleetControlFile)
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var ctrl FleetControl
	if err := json.Unmarshal(data, &ctrl); err != nil {
		return nil, err
	}
	return &ctrl, nil
}

// WorkerState represents the live status of an individual worker slot.
type WorkerState struct {
	ID             int       `json:"id"`
	Status         string    `json:"status"` // "IDLE" | "SEARCHING" | "READING" | "COOLDOWN" | "SOLVING"
	ProxyIP        string    `json:"proxy_ip"`
	ProxyPort      int       `json:"proxy_port"`
	ProxyCountry   string    `json:"proxy_country"`
	Engine         string    `json:"engine"`
	ArticleTitle   string    `json:"article_title"`
	ArticleURL     string    `json:"article_url"`
	CurrentAction  string    `json:"current_action"`
	ProgressPercent int      `json:"progress_percent"`
	DwellElapsedS  int       `json:"dwell_elapsed_s"`
	DwellTotalS    int       `json:"dwell_total_s"`
	LastUpdate     time.Time `json:"last_update"`
}

// LogEntry represents a structured log line for the Web Terminal.
type LogEntry struct {
	ID        int64     `json:"id"`
	Timestamp time.Time `json:"timestamp"`
	WorkerID  int       `json:"worker_id"`
	Country   string    `json:"country"`
	Level     string    `json:"level"`
	Message   string    `json:"message"`
}

// FleetManager manages multi-worker concurrency, state tracking, and log ring buffer.
type FleetManager struct {
	mu           sync.RWMutex
	concurrency  int
	workers      map[int]*WorkerState
	logs         []LogEntry
	maxLogs      int
	nextLogID    int64
	paused       bool
	manualQueue  []int64 // article IDs queued manually from UI
}

var (
	GlobalFleet *FleetManager
	fleetOnce   sync.Once
)

// GetFleetManager returns the singleton FleetManager instance.
func GetFleetManager(initialConcurrency int) *FleetManager {
	fleetOnce.Do(func() {
		if initialConcurrency <= 0 {
			initialConcurrency = 2 // default 2 parallel workers
		}
		fm := &FleetManager{
			concurrency: initialConcurrency,
			workers:     make(map[int]*WorkerState),
			logs:        make([]LogEntry, 0, 500),
			maxLogs:     500,
			nextLogID:   1,
		}
		for i := 1; i <= 10; i++ {
			fm.workers[i] = &WorkerState{
				ID:            i,
				Status:        "IDLE",
				CurrentAction: "Standby",
				LastUpdate:    time.Now(),
			}
		}
		GlobalFleet = fm
	})
	return GlobalFleet
}

// GetConcurrency returns current concurrency limit.
func (f *FleetManager) GetConcurrency() int {
	f.mu.RLock()
	defer f.mu.RUnlock()
	return f.concurrency
}

// SetConcurrency updates concurrency limit dynamically.
func (f *FleetManager) SetConcurrency(n int) {
	f.mu.Lock()
	if n < 1 {
		n = 1
	}
	if n > 10 {
		n = 10
	}
	f.concurrency = n
	f.logUnlocked(0, "SYSTEM", "INFO", fmt.Sprintf("Concurrency updated to %d active workers", n))
	f.mu.Unlock()

	f.persistSnapshot()
}

// UpdateWorkerState updates the live state of a worker slot.
func (f *FleetManager) UpdateWorkerState(id int, fn func(w *WorkerState)) {
	f.mu.Lock()
	w, ok := f.workers[id]
	if !ok {
		w = &WorkerState{ID: id}
		f.workers[id] = w
	}
	fn(w)
	w.LastUpdate = time.Now()
	f.mu.Unlock()

	f.persistSnapshot()
}

// persistSnapshot mirrors current worker state to fleetStatusFile so any
// process (dashboard included) can read real fleet data without needing to
// share this FleetManager's process memory. Best-effort: I/O errors are
// silently dropped since a stale/missing status file just falls back to
// idle display, never breaks the automation loop itself.
func (f *FleetManager) persistSnapshot() {
	f.mu.RLock()
	snap := FleetSnapshot{
		UpdatedAt:   time.Now(),
		PID:         os.Getpid(),
		Concurrency: f.concurrency,
	}
	for i := 1; i <= f.concurrency; i++ {
		if w, ok := f.workers[i]; ok {
			cp := *w
			snap.Workers = append(snap.Workers, &cp)
		}
	}
	f.mu.RUnlock()

	data, err := json.MarshalIndent(snap, "", "  ")
	if err != nil {
		return
	}
	baseDir, _ := os.Getwd()
	path := filepath.Join(baseDir, fleetStatusFile)
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return
	}
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, data, 0o644); err != nil {
		return
	}
	_ = os.Rename(tmp, path)
}

// LoadFleetSnapshot reads the fleet status last written by whichever process
// is actually running the orchestrator loop (in-process, spawned
// subprocess, or an independent systemd service). Returns an error if no
// snapshot has ever been written.
func LoadFleetSnapshot(baseDir string) (*FleetSnapshot, error) {
	path := filepath.Join(baseDir, fleetStatusFile)
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var snap FleetSnapshot
	if err := json.Unmarshal(data, &snap); err != nil {
		return nil, err
	}
	return &snap, nil
}

// GetFleetStatus returns the live states of all active workers up to concurrency limit.
func (f *FleetManager) GetFleetStatus() []*WorkerState {
	f.mu.RLock()
	defer f.mu.RUnlock()
	var out []*WorkerState
	for i := 1; i <= f.concurrency; i++ {
		if w, ok := f.workers[i]; ok {
			cp := *w
			out = append(out, &cp)
		}
	}
	return out
}

// Log adds a log entry to the ring buffer.
func (f *FleetManager) Log(workerID int, country, level, message string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.logUnlocked(workerID, country, level, message)
}

func (f *FleetManager) logUnlocked(workerID int, country, level, message string) {
	entry := LogEntry{
		ID:        f.nextLogID,
		Timestamp: time.Now(),
		WorkerID:  workerID,
		Country:   country,
		Level:     level,
		Message:   message,
	}
	f.nextLogID++
	if len(f.logs) >= f.maxLogs {
		f.logs = f.logs[1:]
	}
	f.logs = append(f.logs, entry)
}

// GetLogs returns logs since a given log ID, optionally filtered by worker ID.
func (f *FleetManager) GetLogs(sinceID int64, filterWorker int) []LogEntry {
	f.mu.RLock()
	defer f.mu.RUnlock()
	var out []LogEntry
	for _, l := range f.logs {
		if l.ID > sinceID {
			if filterWorker == 0 || l.WorkerID == filterWorker {
				out = append(out, l)
			}
		}
	}
	return out
}

// EnqueueManualArticle queues an article for immediate priority execution.
func (f *FleetManager) EnqueueManualArticle(articleID int64) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.manualQueue = append(f.manualQueue, articleID)
	f.logUnlocked(0, "SYSTEM", "INFO", fmt.Sprintf("Article ID %d prioritized for immediate search", articleID))
}

// PopManualArticle pops a manually queued article if available.
func (f *FleetManager) PopManualArticle() (int64, bool) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if len(f.manualQueue) == 0 {
		return 0, false
	}
	id := f.manualQueue[0]
	f.manualQueue = f.manualQueue[1:]
	return id, true
}
