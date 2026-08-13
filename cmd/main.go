// Command google-automation is the entry point for the Go search-automation orchestrator.
// It loads config, opens the SQLite database, connects to the Python gRPC worker,
// and runs the orchestrator loop with graceful shutdown support.
package main

import (
	"context"
	"flag"
	"log"
	"os"
	"os/signal"
	"syscall"
	"time"

	"google-automation/internal/config"
	grpcclient "google-automation/internal/grpc"
	"google-automation/internal/orchestrator"
	"google-automation/internal/storage"
)

func main() {
	configPath := flag.String("config", "config/config.yaml", "path to config.yaml")
	dbPath := flag.String("db", "search_automation.db", "path to SQLite database file")
	workerHost := flag.String("worker-host", "localhost", "Python worker gRPC host")
	shutdownTimeout := flag.Duration("shutdown-timeout", 30*time.Second, "graceful shutdown timeout")
	flag.Parse()

	log.SetFlags(log.LstdFlags | log.Lmicroseconds)
	log.Println("╔══════════════════════════════════════════╗")
	log.Println("║  Search Automation — Go Orchestrator     ║")
	log.Println("║  Proxy + Humanized Search SEO Engine     ║")
	log.Println("╚══════════════════════════════════════════╝")

	// 1. Load configuration.
	cfg, err := config.Load(*configPath)
	if err != nil {
		log.Fatalf("Failed to load config: %v", err)
	}
	log.Printf("Config loaded: %d domains, engine ratio Google:%d/Bing:%d",
		len(cfg.Domains), cfg.EngineRatio.Google, cfg.EngineRatio.Bing)

	// 2. Open SQLite database (auto-migrates schema).
	db, err := storage.New(*dbPath)
	if err != nil {
		log.Fatalf("Failed to open database: %v", err)
	}
	defer func() {
		if err := db.Close(); err != nil {
			log.Printf("DB close error: %v", err)
		}
		log.Println("Database closed.")
	}()
	log.Printf("Database ready: %s", *dbPath)

	// 3. Connect to the Python worker via gRPC.
	grpcClient, err := grpcclient.NewClient(*workerHost, cfg.GRPC.Port, cfg.GRPC.WorkerTimeout)
	if err != nil {
		log.Fatalf("Failed to connect to Python worker: %v", err)
	}

	// 4. Create root context for graceful shutdown.
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	// 5. Create and start the orchestrator.
	orch := orchestrator.New(cfg, db, grpcClient)

	// 6. Handle OS signals for graceful shutdown.
	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, syscall.SIGINT, syscall.SIGTERM)

	go func() {
		sig := <-sigCh
		log.Printf("[shutdown] received signal %v — initiating graceful shutdown…", sig)

		// Give the orchestrator a deadline to finish in-flight work.
		shutdownCtx, shutdownCancel := context.WithTimeout(context.Background(), *shutdownTimeout)
		defer shutdownCancel()

		// Signal the orchestrator to stop.
		orch.Stop()

		// Wait for shutdown deadline or completion.
		<-shutdownCtx.Done()
		log.Println("[shutdown] shutdown context done — proceeding to cleanup")
		cancel()
	}()

	// 7. Run the orchestrator (blocks until Stop or error).
	if err := orch.Run(ctx); err != nil {
		log.Printf("Orchestrator error: %v", err)
	}

	// 8. Final report.
	orch.PrintReports()

	// 9. Close gRPC connection.
	if err := grpcClient.Close(); err != nil {
		log.Printf("gRPC close error: %v", err)
	}
	log.Println("[shutdown] gRPC connection closed.")

	// 10. Flush any pending analytics.
	log.Println("[shutdown] flushing analytics…")
	log.Println("Orchestrator stopped. Goodbye!")
}
