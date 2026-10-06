// Command miner-fleet serves the miner dashboard: it polls each configured
// Bitaxe/NerdQAxe++ (AxeOS), CYD, Avalon Nano 3 or BraiinsOS+ on an interval,
// stores samples in SQLite, serves an HTMX dashboard over HTTP, and
// optionally emails an alert on a block found or a high-difficulty share.
package main

import (
	"context"
	"embed"
	"flag"
	"fmt"
	"log"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"time"
	_ "time/tzdata"

	"github.com/yorgof/miner-fleet/internal/alert"
	"github.com/yorgof/miner-fleet/internal/fleet"
	"github.com/yorgof/miner-fleet/internal/miners"
	"github.com/yorgof/miner-fleet/internal/store"
	"github.com/yorgof/miner-fleet/internal/web"
)

//go:embed templates/*.html
var templatesFS embed.FS

//go:embed static/*
var staticFS embed.FS

// version is set by the release build; source builds identify themselves as dev.
var version = "dev"

func main() {
	configDir, err := os.UserConfigDir()
	if err != nil {
		configDir = "."
	}
	addr := flag.String("addr", "127.0.0.1:8080", "listen address")
	dbPath := flag.String("db", filepath.Join(configDir, "miner-fleet", "miner-fleet.db"), "sqlite database path")
	pollInterval := flag.Duration("poll-interval", 15*time.Second, "how often to poll each miner")
	retention := flag.Duration("retention", 90*24*time.Hour, "how long to keep samples")
	healthcheck := flag.Bool("healthcheck", false,
		"check /healthz on the local server and exit 0/1, instead of running the server (used as the Docker HEALTHCHECK, since the distroless image has no shell/wget)")
	alertTest := flag.Bool("alert-test", false,
		"send one test ntfy notification and exit")
	showVersion := flag.Bool("version", false, "print the application version and exit")
	flag.Parse()
	if *showVersion {
		fmt.Printf("Miner Fleet %s\n", version)
		return
	}
	if *pollInterval < time.Second || *retention < time.Hour {
		log.Fatal("poll interval must be at least one second and retention at least one hour")
	}

	if *healthcheck {
		os.Exit(runHealthcheck(*addr))
	}

	alertCfg := alertConfigFromEnv()

	if *alertTest {
		ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
		defer cancel()
		notifier := alert.NewNotifier(alertCfg, nil) // SendTest never touches the database
		if err := notifier.SendTest(ctx); err != nil {
			log.Printf("alert test FAILED: %v", err)
			os.Exit(1)
		}
		log.Printf("alert test notification sent")
		return
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	st, err := store.Open(ctx, *dbPath)
	if err != nil {
		log.Fatalf("open store: %v", err)
	}
	defer st.Close()

	notifier := alert.NewNotifier(alertCfg, st)
	if notifier.Enabled() {
		log.Printf("alerting enabled: block found or best difficulty >= %.0f", alertCfg.DiffThreshold)
	} else {
		log.Printf("alerting disabled (set MINER_FLEET_NTFY_URL to enable)")
	}

	reg := miners.NewRegistry(st, notifier, *pollInterval)
	if err := reg.Start(ctx); err != nil {
		log.Fatalf("start registry: %v", err)
	}
	go reg.StartPruner(ctx, *retention)
	manager := miners.NewManager(st, reg)
	go fleet.New(st, reg, manager, notifier).Run(ctx)

	srv, err := web.NewServer(reg, st, templatesFS, staticFS)
	if err != nil {
		log.Fatalf("new server: %v", err)
	}
	srv.Configure(manager, *pollInterval)

	httpServer := &http.Server{
		Addr:              *addr,
		Handler:           srv.Routes(),
		ReadHeaderTimeout: 5 * time.Second,
	}

	go func() {
		log.Printf("miner-fleet listening on %s (db=%s, poll=%s, retention=%s)", *addr, *dbPath, *pollInterval, *retention)
		if err := httpServer.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			log.Fatalf("listen: %v", err)
		}
	}()

	<-ctx.Done()
	log.Println("shutting down")
	shutdownCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := httpServer.Shutdown(shutdownCtx); err != nil {
		log.Printf("shutdown: %v", err)
	}
}

// alertConfigFromEnv reads alert settings from the environment rather than
// flags, to keep secrets out of command-line arguments. An ntfy topic URL
// is private because anyone who knows it may be able to publish to or read
// the topic.
func alertConfigFromEnv() alert.Config {
	threshold, err := strconv.ParseFloat(os.Getenv("MINER_FLEET_DIFF_THRESHOLD"), 64)
	if err != nil {
		threshold = 1e12
	}
	return alert.Config{
		NtfyURL:       os.Getenv("MINER_FLEET_NTFY_URL"),
		NtfyEmail:     os.Getenv("MINER_FLEET_NTFY_EMAIL"),
		NtfyToken:     os.Getenv("MINER_FLEET_NTFY_TOKEN"),
		DiffThreshold: threshold,
	}
}

func runHealthcheck(addr string) int {
	port := addr
	if idx := strings.LastIndex(addr, ":"); idx >= 0 {
		port = addr[idx+1:]
	}
	url := fmt.Sprintf("http://127.0.0.1:%s/healthz", port)
	client := http.Client{Timeout: 3 * time.Second}
	resp, err := client.Get(url)
	if err != nil {
		return 1
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return 1
	}
	return 0
}
