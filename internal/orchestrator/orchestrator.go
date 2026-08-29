// Package orchestrator ties together all subsystems (proxy, article, scheduler,
// storage, analytics, gRPC) into the main task loop.
package orchestrator

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"math/rand"
	"os"
	"sync"
	"time"

	"google-automation/internal/analytics"
	"google-automation/internal/article"
	"google-automation/internal/bandwidth"
	"google-automation/internal/config"
	grpcclient "google-automation/internal/grpc"
	pb "google-automation/internal/grpc/proto"
	"google-automation/internal/notify"
	"google-automation/internal/proxy"
	"google-automation/internal/scheduler"
	"google-automation/internal/storage"
)

// Orchestrator is the top-level coordinator.
type Orchestrator struct {
	cfg       *config.Config
	db        *storage.DB
	proxyMgr  *proxy.Manager
	pool      *proxy.Pool
	articleQ  *article.Queue
	scheduler *scheduler.Scheduler
	cooldown  *scheduler.CooldownManager
	spread    *scheduler.SpreadTracker
	stats     *analytics.Stats
	serp      *analytics.SerpTracker
	grpc      *grpcclient.Client
	bwTracker *bandwidth.Tracker
	telegram  *notify.Telegram

	// running tracks whether the loop should continue.
	running bool
	stopCh  chan struct{}
	stopOnce sync.Once
}

// New creates and wires all subsystems. It does NOT start the loop —
// call Run() for that.
func New(cfg *config.Config, db *storage.DB, grpcClient *grpcclient.Client) *Orchestrator {
	proxyMgr := proxy.NewManager(&cfg.Proxy, db)
	articleQ := article.NewQueue(db)
	sched := scheduler.New(cfg, db, proxyMgr)
	cooldown := scheduler.NewCooldownManager(&cfg.Scheduler, db)
	spread := scheduler.NewSpreadTracker(db)
	stats := analytics.NewStats(db)
	serpTracker := analytics.NewSerpTracker(db)

	// Bandwidth tracker: reads/writes data/bandwidth.json (shared with Python worker).
	baseDir, _ := os.Getwd()
	bwTracker := bandwidth.NewTracker(
		baseDir,
		cfg.Bandwidth.MonthlyLimitMB,
		cfg.Bandwidth.PauseThresholdPercent,
	)
	proxyMgr.Pool().SetBandwidthTracker(bwTracker)

	var tg *notify.Telegram
	if cfg.Telegram.Enabled && cfg.Telegram.BotToken != "" && cfg.Telegram.ChatID != "" {
		tg = notify.NewTelegram(cfg.Telegram.BotToken, cfg.Telegram.ChatID)
	}

	return &Orchestrator{
		cfg:       cfg,
		db:        db,
		proxyMgr:  proxyMgr,
		pool:      proxyMgr.Pool(),
		articleQ:  articleQ,
		scheduler: sched,
		cooldown:  cooldown,
		spread:    spread,
		stats:     stats,
		serp:      serpTracker,
		grpc:      grpcClient,
		bwTracker: bwTracker,
		telegram:  tg,
		stopCh:    make(chan struct{}),
	}
}

// Run starts the orchestrator loop. It blocks until Stop() is called or the
// context is cancelled. The loop:
//  1. Refreshes proxies (initial + periodic background)
//  2. Refreshes articles (initial + periodic background)
//  3. In each iteration: picks a proxy, picks an article, sends a gRPC task,
//     records the result, applies cooldowns.
func (o *Orchestrator) Run(ctx context.Context) error {
	o.running = true
	log.Println("[orchestrator] starting…")

	// --- Initial setup ---
	log.Println("[orchestrator] initial proxy refresh…")
	if err := o.proxyMgr.InitialRefresh(); err != nil {
		return fmt.Errorf("initial proxy refresh: %w", err)
	}

	// Start background proxy refresh loop.
	go o.proxyMgr.StartRefreshLoop()

	log.Println("[orchestrator] initial article collection…")
	if err := o.articleQ.RefreshArticles(o.cfg.Domains, o.cfg.ArticleCollection.MaxConcurrentFetches); err != nil {
		log.Printf("[orchestrator] article refresh failed: %v (continuing with cached articles)", err)
		if err := o.articleQ.LoadFromDB(); err != nil {
			return fmt.Errorf("load articles from DB: %w", err)
		}
	}
	go o.articleRefreshLoop()

	// Start the midnight daily-reset goroutine.
	go o.dailyResetLoop()

	// Start Telegram interactive command listener if configured.
	if o.telegram != nil {
		o.telegram.StartCommandListener(notify.CommandHandlers{
			OnStatus: func() string {
				return fmt.Sprintf("<b>⚡ Bot Status</b>\n• Available Proxies: <b>%d</b>\n• Total Proxies: <b>%d</b>\n• Orchestrator Running: <b>%t</b>",
					o.pool.AvailableCount(), o.pool.TotalCount(), o.running)
			},
			OnStats: func() string {
				summary, err := o.stats.TodaySummary()
				if err != nil {
					return "Error fetching stats: " + err.Error()
				}
				return fmt.Sprintf("<b>📊 Today Stats (%s)</b>\n• Searches: <b>%d</b>\n• Success: <b>%d (%.1f%%)</b>\n• CAPTCHA: <b>%d (%.1f%%)</b>\n• Avg Dwell: <b>%.1fs</b>\n• Avg SERP: <b>%.1f</b>",
					summary.Date, summary.TotalSearch, summary.Success, summary.SuccessRate, summary.Captcha, summary.CaptchaRate, summary.AvgDwellSeconds, summary.AvgSerpPosition)
			},
			OnPause: func() string {
				o.scheduler.TriggerEnginePause("google")
				o.scheduler.TriggerEnginePause("bing")
				return "⏸️ All search engines paused for 3 hours."
			},
			OnResume: func() string {
				o.scheduler.DailyReset()
				return "▶️ Engines resumed and pauses cleared."
			},
		})
		log.Println("[orchestrator] telegram command listener started")
	}

	// Initialize FleetManager
	concurrency := o.cfg.Scheduler.Concurrency
	if concurrency <= 0 {
		concurrency = 2
	}
	fm := GetFleetManager(concurrency)

	// Apply any standing dashboard-requested concurrency BEFORE launching
	// worker goroutines — without this synchronous check, there was a ~3s
	// window at every startup where worker slots ran at config.yaml's
	// default instead of the live-set value, letting more browser sessions
	// spin up simultaneously than intended.
	baseDir, _ := os.Getwd()
	lastApplied := applyFleetControlIfNewer(fm, baseDir, time.Time{})

	log.Printf("[orchestrator] Fleet Manager initialized with %d workers", fm.GetConcurrency())
	fm.Log(0, "SYSTEM", "INFO", fmt.Sprintf("Orchestrator started with %d workers", fm.GetConcurrency()))

	go o.fleetControlPollLoop(fm, lastApplied)
	go o.proxyExhaustionAlertLoop(fm)

	// Launch concurrent worker loops
	var wg sync.WaitGroup
	for i := 1; i <= 10; i++ {
		slotID := i
		wg.Add(1)
		go func() {
			defer wg.Done()
			o.runWorkerLoop(ctx, slotID, fm)
		}()
	}

	<-ctx.Done()
	log.Println("[orchestrator] context cancelled — stopping fleet")
	wg.Wait()
	return nil
}

// runWorkerLoop handles task execution for an individual worker slot.
func (o *Orchestrator) runWorkerLoop(ctx context.Context, slotID int, fm *FleetManager) {
	for {
		if !o.running {
			return
		}
		select {
		case <-ctx.Done():
			return
		case <-o.stopCh:
			return
		default:
		}

		// Check if this slot is active under current concurrency setting
		if slotID > fm.GetConcurrency() {
			fm.UpdateWorkerState(slotID, func(w *WorkerState) {
				w.Status = "IDLE"
				w.CurrentAction = "Inactive Slot"
				w.ProgressPercent = 0
			})
			select {
			case <-ctx.Done():
				return
			case <-time.After(2 * time.Second):
				continue
			}
		}

		o.runOneSlotCycle(ctx, slotID, fm)
	}
}

// runOneSlotCycle executes a single cycle for slotID.
func (o *Orchestrator) runOneSlotCycle(ctx context.Context, slotID int, fm *FleetManager) {
	if o.pool.AvailableCount() == 0 && o.pool.TotalCount() > 0 {
		o.pool.ResetCycle()
	}

	ok, reason := o.scheduler.ShouldRun()
	if !ok {
		fm.UpdateWorkerState(slotID, func(w *WorkerState) {
			w.Status = "COOLDOWN"
			w.CurrentAction = reason
			w.ProgressPercent = 0
		})
		select {
		case <-ctx.Done():
			return
		case <-time.After(5 * time.Second):
			return
		}
	}

	px, found := o.scheduler.AcquireUsableProxy()
	if !found {
		wait := o.scheduler.SleepUntilEarliestActiveHour()
		action := fmt.Sprintf("Outside Active Hours (%v remaining)", wait.Round(time.Minute))
		if wait < 5*time.Second {
			// AcquireUsableProxy can also fail while every proxy is within
			// active hours but bandwidth-exhausted or quarantined — in that
			// case SleepUntilEarliestActiveHour reports ~0 remaining, which
			// would otherwise cause a rapid retry loop instead of a real
			// backoff.
			wait = 5 * time.Second
			action = "No usable proxy available (bandwidth exhausted or quarantined) — retrying"
		}
		fm.UpdateWorkerState(slotID, func(w *WorkerState) {
			w.Status = "COOLDOWN"
			w.CurrentAction = action
		})
		select {
		case <-ctx.Done():
			return
		case <-time.After(wait):
			return
		}
	}

	// Pick article: check manual queue first
	var selected *article.SelectedArticle
	if manualID, ok := fm.PopManualArticle(); ok {
		if art, err := o.db.ArticleByID(manualID); err == nil && art != nil {
			selected = &article.SelectedArticle{
				Article:      *art,
				SearchMethod: article.MethodExactTitle,
				Query:        art.Title,
			}
		}
	}

	if selected == nil {
		var ok bool
		selected, ok = o.articleQ.PickRandom(&o.cfg.Scheduler)
		if !ok {
			o.pool.Release(px)
			fm.UpdateWorkerState(slotID, func(w *WorkerState) {
				w.Status = "IDLE"
				w.CurrentAction = "Waiting for articles"
			})
			select {
			case <-ctx.Done():
				return
			case <-time.After(5 * time.Second):
				return
			}
		}
	}

	if !o.spread.IsSpreadOK(selected.Article.ID) {
		o.pool.Release(px)
		fm.UpdateWorkerState(slotID, func(w *WorkerState) {
			w.Status = "IDLE"
			w.CurrentAction = fmt.Sprintf("Article %d already searched today, retrying pick", selected.Article.ID)
		})
		select {
		case <-ctx.Done():
			return
		case <-time.After(1 * time.Second):
			return
		}
	}

	engine := o.scheduler.PickEngineAvailable()
	if engine == "" {
		o.pool.Release(px)
		fm.UpdateWorkerState(slotID, func(w *WorkerState) {
			w.Status = "COOLDOWN"
			w.CurrentAction = "No traffic engine available (all paused or ratio 0)"
		})
		select {
		case <-ctx.Done():
			return
		case <-time.After(5 * time.Second):
			return
		}
	}

	taskID, err := o.db.CreateTask(selected.Article.ID, px.ID, engine)
	if err != nil {
		o.pool.Release(px)
		log.Printf("[orchestrator] slot %d: create task failed: %v", slotID, err)
		fm.UpdateWorkerState(slotID, func(w *WorkerState) {
			w.Status = "IDLE"
			w.CurrentAction = fmt.Sprintf("Task creation failed: %v", err)
		})
		select {
		case <-ctx.Done():
			return
		case <-time.After(3 * time.Second):
			return
		}
	}

	taskIDStr := fmt.Sprintf("%d", taskID)
	preSearchQueries := o.articleQ.GeneratePreSearchQueries(selected.Article)

	fm.UpdateWorkerState(slotID, func(w *WorkerState) {
		w.Status = "SEARCHING"
		w.ProxyIP = px.IP
		w.ProxyPort = px.Port
		w.ProxyCountry = px.Country
		w.Engine = engine
		w.ArticleTitle = selected.Article.Title
		w.ArticleURL = selected.Article.URL
		w.CurrentAction = fmt.Sprintf("Searching %s via %s", engine, px.Country)
		w.ProgressPercent = 25
	})
	fm.Log(slotID, px.Country, "TASK", fmt.Sprintf("Target: %q (%s)", selected.Article.Title, engine))

	req := &pb.TaskRequest{
		TaskId:                  taskIDStr,
		ArticleTitle:           selected.Article.Title,
		ArticleUrl:             selected.Article.URL,
		Domain:                 selected.Article.Domain,
		ProxyIp:                px.IP,
		ProxyPort:              int32(px.Port),
		Engine:                 engine,
		PreSearchQueries:       preSearchQueries,
		ProxyUsername:          px.Username,
		ProxyPassword:          px.Password,
		ProxyCountry:           px.Country,
		ProxyTimezone:          px.Timezone,
		PreSearchEnabled:       o.cfg.Scheduler.PreSearchEnabled,
		PreSearch_2Chance:       o.cfg.Scheduler.PreSearch2Chance,
		SerpCasualClickChance:  o.cfg.Scheduler.SerpCasualClickChance,
		CompetitorClickChance:  o.cfg.Scheduler.CompetitorClickChance,
		DistractionExitChance:  o.cfg.Scheduler.DistractionExitChance,
		SerpDwellSecondsMin:    int32(o.cfg.Scheduler.SerpDwellSecondsMin),
		SerpDwellSecondsMax:    int32(o.cfg.Scheduler.SerpDwellSecondsMax),
	}

	fm.UpdateWorkerState(slotID, func(w *WorkerState) {
		w.Status = "READING"
		w.CurrentAction = "Simulating Reading & Heatmaps"
		w.ProgressPercent = 65
	})

	resp, err := o.grpc.ExecuteTask(req)
	_ = o.db.MarkProxyUsed(px.ID)
	o.processResult(taskID, selected.Article.ID, resp, err)

	if resp != nil && resp.BandwidthUsedKb > 0 {
		o.bwTracker.RecordUsage(px.APIKeyIndex, float64(resp.BandwidthUsedKb))
	} else if err != nil {
		o.bwTracker.RecordUsage(px.APIKeyIndex, 1024)
	}

	if resp != nil && resp.CaptchaHit {
		fm.UpdateWorkerState(slotID, func(w *WorkerState) {
			w.Status = "SOLVING"
			w.CurrentAction = "CAPTCHA Hit"
		})
		fm.Log(slotID, px.Country, "WARN", "CAPTCHA hit on "+engine)
		o.scheduler.TriggerEnginePause(engine)
		o.pool.RecordFailure(px, true, resp.Error)
	} else if err != nil {
		fm.Log(slotID, px.Country, "ERROR", err.Error())
		o.pool.RecordFailure(px, false, err.Error())
	} else if resp != nil && resp.Success {
		fm.Log(slotID, px.Country, "SUCCESS", fmt.Sprintf("Completed dwell=%ds serp=%d", resp.DwellTimeSeconds, resp.SerpPosition))
		o.pool.RecordSuccess(px)
	}

	success := err == nil && resp != nil && resp.Success
	o.cooldown.RecordTaskEnd(success)
	_ = o.db.UpdateDailyStats()

	// Cooldown
	cooldown := o.cooldown.InterTaskCooldown()
	fm.UpdateWorkerState(slotID, func(w *WorkerState) {
		w.Status = "COOLDOWN"
		w.CurrentAction = fmt.Sprintf("Resting for %v", cooldown.Round(time.Second))
		w.ProgressPercent = 100
	})

	select {
	case <-ctx.Done():
		return
	case <-time.After(cooldown):
	}
}

// processResult records the task outcome in the DB and updates article stats.
func (o *Orchestrator) processResult(taskID int64, articleID int64, resp *pb.TaskResponse, err error) {
	if err != nil || resp == nil {
		// Task failed (gRPC error or nil response).
		errMsg := "unknown error"
		if err != nil {
			errMsg = err.Error()
		}
		_ = o.db.UpdateTaskResult(taskID, "fail", "", errMsg)
		log.Printf("[orchestrator] task %d FAILED: %s", taskID, errMsg)
		return
	}

	// Marshal the response to JSON for storage.
	resultJSON, _ := json.Marshal(resp)

	status := "success"
	if !resp.Success {
		status = "fail"
	}
	if resp.CaptchaHit {
		status = "captcha"
	}

	_ = o.db.UpdateTaskResult(taskID, status, string(resultJSON), resp.Error)

	// Update article search stats with SERP position.
	serpPos := 0
	if resp.SerpPosition > 0 {
		serpPos = int(resp.SerpPosition)
	}
	_ = o.db.MarkArticleSearched(articleID, serpPos)

	log.Printf("[orchestrator] task %d %s — dwell=%ds serp=%d captcha=%v",
		taskID, status, resp.DwellTimeSeconds, resp.SerpPosition, resp.CaptchaHit)
}

// fleetControlPollLoop applies concurrency changes requested via the
// dashboard's +/- control (see FleetControl / WriteFleetControl). The
// dashboard runs in a different OS process and can only write its request to
// disk, so this process must poll for it and apply it to its own
// FleetManager — the one that actually gates active worker slots.
// applyFleetControlIfNewer reads fleet_control.json and applies its
// concurrency to fm if it's newer than lastApplied. Returns the (possibly
// updated) lastApplied timestamp.
func applyFleetControlIfNewer(fm *FleetManager, baseDir string, lastApplied time.Time) time.Time {
	ctrl, err := ReadFleetControl(baseDir)
	if err != nil || ctrl == nil {
		return lastApplied
	}
	if ctrl.RequestedAt.After(lastApplied) && ctrl.Concurrency != fm.GetConcurrency() {
		fm.SetConcurrency(ctrl.Concurrency)
		log.Printf("[orchestrator] concurrency changed to %d via dashboard control", ctrl.Concurrency)
		return ctrl.RequestedAt
	}
	return lastApplied
}

func (o *Orchestrator) fleetControlPollLoop(fm *FleetManager, lastApplied time.Time) {
	baseDir, _ := os.Getwd()
	ticker := time.NewTicker(3 * time.Second)
	defer ticker.Stop()

	for {
		select {
		case <-o.stopCh:
			return
		case <-ticker.C:
			lastApplied = applyFleetControlIfNewer(fm, baseDir, lastApplied)
		}
	}
}

// proxyExhaustionAlertLoop watches for the whole proxy pool becoming
// unusable (every proxy bandwidth-exhausted or quarantined) and fires one
// alert when it happens, then one recovery notice when it clears — instead
// of the silence that let today's exhaustion go unnoticed for hours. Always
// logs to the dashboard's terminal feed; also pushes to Telegram when
// configured.
func (o *Orchestrator) proxyExhaustionAlertLoop(fm *FleetManager) {
	ticker := time.NewTicker(30 * time.Second)
	defer ticker.Stop()

	alerted := false
	for {
		select {
		case <-o.stopCh:
			return
		case <-ticker.C:
			if o.pool.TotalCount() == 0 {
				continue // proxies not loaded yet
			}
			usable := o.pool.HasUsableProxy()
			switch {
			case !usable && !alerted:
				msg := "⚠️ ALL proxies exhausted (bandwidth limit or quarantine) — fleet is idling with no usable proxy. Check Webshare bandwidth / add more API keys."
				log.Println("[orchestrator] " + msg)
				fm.Log(0, "SYSTEM", "ERROR", msg)
				if o.telegram != nil {
					if err := o.telegram.Send(msg); err != nil {
						log.Printf("[orchestrator] telegram alert failed: %v", err)
					}
				}
				alerted = true
			case usable && alerted:
				msg := "✅ Proxy pool recovered — usable proxies available again."
				log.Println("[orchestrator] " + msg)
				fm.Log(0, "SYSTEM", "SUCCESS", msg)
				if o.telegram != nil {
					if err := o.telegram.Send(msg); err != nil {
						log.Printf("[orchestrator] telegram alert failed: %v", err)
					}
				}
				alerted = false
			}
		}
	}
}

// articleRefreshLoop periodically re-scrapes the domain for new articles.
func (o *Orchestrator) articleRefreshLoop() {
	interval := time.Duration(o.cfg.ArticleCollection.RefreshIntervalHours) * time.Hour
	ticker := time.NewTicker(interval)
	defer ticker.Stop()

	for {
		select {
		case <-o.stopCh:
			return
		case <-ticker.C:
			log.Println("[orchestrator] periodic article refresh")
			if err := o.articleQ.RefreshArticles(o.cfg.Domains, o.cfg.ArticleCollection.MaxConcurrentFetches); err != nil {
				log.Printf("[orchestrator] article refresh failed: %v", err)
			}
		}
	}
}

// dailyResetLoop runs a goroutine that performs a full system reset every
// midnight (local time): resets the proxy pool cycle, clears all engine
// CAPTCHA pauses, resets the cooldown failure counter, and zeroes out the
// daily proxy used_count in the database.
func (o *Orchestrator) dailyResetLoop() {
	for {
		nextMidnight := timeUntilNextMidnight()
		log.Printf("[orchestrator] daily reset scheduled in %v", nextMidnight.Round(time.Minute))

		timer := time.NewTimer(nextMidnight)
		select {
		case <-o.stopCh:
			timer.Stop()
			return
		case <-timer.C:
			o.performDailyReset()
		}
	}
}

// performDailyReset executes the actual reset sequence.
func (o *Orchestrator) performDailyReset() {
	log.Println("[orchestrator] === midnight daily reset ===")

	o.sendDailySummaryNotif()

	o.pool.DailyReset()
	o.scheduler.DailyReset()
	o.cooldown.DailyReset()

	if err := o.db.ResetDailyProxyUsage(); err != nil {
		log.Printf("[orchestrator] daily proxy usage reset failed: %v", err)
	}

	log.Println("[orchestrator] daily reset complete — starting fresh day")
}

// sendDailySummaryNotif sends today's summary to Telegram if configured.
func (o *Orchestrator) sendDailySummaryNotif() {
	if o.telegram == nil {
		return
	}
	summary, err := o.stats.TodaySummary()
	if err != nil {
		log.Printf("[orchestrator] telegram: failed to get summary: %v", err)
		return
	}
	if err := o.telegram.SendDailySummary(summary); err != nil {
		log.Printf("[orchestrator] telegram: send failed: %v", err)
		return
	}
	log.Println("[orchestrator] telegram: daily summary sent")
}

// timeUntilNextMidnight returns the duration until the next local midnight.
func timeUntilNextMidnight() time.Duration {
	now := time.Now()
	midnight := time.Date(now.Year(), now.Month(), now.Day()+1,
		0, 0, 0, 0, now.Location())
	return midnight.Sub(now)
}

// Stop signals the orchestrator to shut down. Safe to call multiple times
// (uses sync.Once to prevent double-close panic on stopCh).
func (o *Orchestrator) Stop() {
	o.stopOnce.Do(func() {
		o.running = false
		close(o.stopCh)
		log.Println("[orchestrator] stop signal sent")
	})
}

// PrintReports logs the analytics summary and proxy leaderboard.
func (o *Orchestrator) PrintReports() {
	o.stats.PrintDailyReport()
	o.stats.PrintProxyLeaderboard(10)
	o.serp.PrintSerpReport(20)
}

// init seeds the global random number generator for article/method selection.
func init() {
	rand.Seed(time.Now().UnixNano())
}
