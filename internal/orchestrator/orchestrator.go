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
	if err := o.articleQ.RefreshArticles(o.cfg.Domains); err != nil {
		log.Printf("[orchestrator] article refresh failed: %v (continuing with cached articles)", err)
		if err := o.articleQ.LoadFromDB(); err != nil {
			return fmt.Errorf("load articles from DB: %w", err)
		}
	}
	go o.articleRefreshLoop()

	// Start the midnight daily-reset goroutine.
	go o.dailyResetLoop()

	// --- Main loop ---
	ticker := time.NewTicker(5 * time.Second)
	defer ticker.Stop()

	for {
		if !o.running {
			break
		}

		select {
		case <-ctx.Done():
			log.Println("[orchestrator] context cancelled — stopping")
			return nil
		case <-o.stopCh:
			log.Println("[orchestrator] stop signal received")
			return nil
		case <-ticker.C:
			o.runOneCycle(ctx)
		}
	}
	return nil
}

// runOneCycle executes a single task attempt: check eligibility, acquire proxy,
// pick article, send gRPC task, record result, apply cooldown.
// The context allows interruption during sleep/cooldown phases.
func (o *Orchestrator) runOneCycle(ctx context.Context) {
	// Auto-reset cycle when all proxies have been used but none are blacklisted.
	if o.pool.AvailableCount() == 0 && o.pool.TotalCount() > 0 {
		log.Printf("[orchestrator] pool exhausted — resetting cycle (all %d proxies requeued)", o.pool.TotalCount())
		o.pool.ResetCycle()
	}
	log.Printf("[orchestrator] cycle tick — pool available=%d", o.pool.AvailableCount())
	// 1. Should we run right now?
	ok, reason := o.scheduler.ShouldRun()
	if !ok {
		log.Printf("[orchestrator] waiting: %s", reason)
		return
	}
	log.Printf("[orchestrator] step1 ok, acquiring proxy...")

	// 2. Acquire a proxy within active hours.
	px, found := o.scheduler.AcquireUsableProxy()
	if !found {
		// All proxies outside active hours — sleep until earliest 7am.
		wait := o.scheduler.SleepUntilEarliestActiveHour()
		log.Printf("[orchestrator] all proxies outside active hours — sleeping %v",
			wait.Round(time.Minute))
		select {
		case <-ctx.Done():
			return
		case <-time.After(wait):
		}
		return
	}
	log.Printf("[orchestrator] step2 ok, proxy=%s:%d, picking article...", px.IP, px.Port)

	// 3. Pick a random eligible article with a random search method.
	selected, ok := o.articleQ.PickRandom(&o.cfg.Scheduler)
	if !ok {
		log.Println("[orchestrator] no eligible articles — waiting for article refresh")
		o.pool.Release(px)
		return
	}
	log.Printf("[orchestrator] step3 ok, article=%q, checking spread...", selected.Article.Title)

	// 4. Spread check: don't search the same article twice in one day.
	if !o.spread.IsSpreadOK(selected.Article.ID) {
		log.Printf("[orchestrator] article %d already searched today — skipping",
			selected.Article.ID)
		o.pool.Release(px)
		return
	}
	log.Printf("[orchestrator] step4 ok, creating task...")
	engine := o.scheduler.PickEngineAvailable()
	if engine == "" {
		log.Printf("[orchestrator] all engines paused — releasing proxy and waiting")
		o.pool.Release(px)
		return
	}

	// 6. Create a task record in the DB.
	taskID, err := o.db.CreateTask(selected.Article.ID, px.ID, engine)
	if err != nil {
		log.Printf("[orchestrator] failed to create task: %v", err)
		o.pool.Release(px)
		return
	}

	// 7. Build the gRPC request.
	taskIDStr := fmt.Sprintf("%d", taskID)
	preSearchQueries := o.articleQ.GeneratePreSearchQueries(selected.Article)

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

	log.Printf("[orchestrator] task %s: article=%q method=%s engine=%s proxy=%s:%d (%s)",
		taskIDStr, selected.Article.Title, selected.SearchMethod, engine,
		px.IP, px.Port, px.Country)

	// 8. Send task to Python worker via gRPC.
	resp, err := o.grpc.ExecuteTask(req)

	// 9. Mark proxy as used (STRICT 1-proxy-1-search — already acquired).
	_ = o.db.MarkProxyUsed(px.ID)

	// 10. Process the result.
	o.processResult(taskID, selected.Article.ID, resp, err)

	// 10b. Record bandwidth used (if Python reported it).
	if resp != nil && resp.BandwidthUsedKb > 0 {
		o.bwTracker.RecordUsage(px.APIKeyIndex, float64(resp.BandwidthUsedKb))
	} else if err != nil {
		// gRPC error — proxy may have burned some bandwidth before failing.
		// Estimate ~1MB to avoid undercounting.
		o.bwTracker.RecordUsage(px.APIKeyIndex, 1024)
	}

	// 11. Check CAPTCHA rate and pause if needed.
	if resp != nil && resp.CaptchaHit {
		// Per-engine pause — Google CAPTCHA doesn't stop Bing
		o.scheduler.TriggerEnginePause(engine)
		o.pool.Blacklist(px, "CAPTCHA hit during search")
	} else if o.scheduler.CheckCaptchaRate() {
		// Aggregate CAPTCHA rate exceeded — global pause
	}

	// 12. Record task end for cooldown tracking.
	success := err == nil && resp != nil && resp.Success
	o.cooldown.RecordTaskEnd(success)

	// 13. Update daily stats.
	if err := o.db.UpdateDailyStats(); err != nil {
		log.Printf("[orchestrator] failed to update daily stats: %v", err)
	}

	// 14. Post-exit cooldown (simulate human attention shift).
	//     Context-aware: can be interrupted during cooldown.
	postExit := o.cooldown.PostExitCooldown()
	log.Printf("[orchestrator] post-exit cooldown: %v", postExit)
	select {
	case <-ctx.Done():
		return
	case <-time.After(postExit):
	}

	// 15. Inter-task cooldown (also context-aware).
	cooldown := o.cooldown.InterTaskCooldown()
	log.Printf("[orchestrator] inter-task cooldown: %v", cooldown)
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
			if err := o.articleQ.RefreshArticles(o.cfg.Domains); err != nil {
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
