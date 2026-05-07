package main

import (
	"context"
	"errors"
	"flag"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"lp100a-server/internal/config"
	"lp100a-server/internal/hub"
	"lp100a-server/internal/meter"
	"lp100a-server/internal/web"
)

func main() {
	configPath := flag.String("config", "/etc/lp100a-server/config.toml", "config file path (missing file is fine; defaults are used)")
	verbose := flag.Bool("v", false, "verbose (debug) logging")
	flag.Parse()

	level := slog.LevelInfo
	if *verbose {
		level = slog.LevelDebug
	}
	logger := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: level}))

	cfg, err := config.Load(*configPath)
	if err != nil {
		logger.Error("config", "err", err)
		os.Exit(1)
	}
	logger.Info("config loaded",
		"path", *configPath,
		"port", cfg.Serial.Port,
		"baud", cfg.Serial.Baud,
		"poll_ms", cfg.Serial.PollIntervalMs,
		"listen", cfg.Server.Listen,
	)

	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer cancel()

	snapCh := make(chan meter.Snapshot, 8)

	owner := meter.NewOwner(meter.SerialConfig{
		Port:         cfg.Serial.Port,
		Baud:         cfg.Serial.Baud,
		PollInterval: time.Duration(cfg.Serial.PollIntervalMs) * time.Millisecond,
	}, snapCh, logger)

	h := hub.NewHub(snapCh, owner, hub.Options{
		Heartbeat:    time.Duration(cfg.Server.HeartbeatMs) * time.Millisecond,
		MaxClients:   cfg.Server.MaxClients,
		AllowControl: cfg.Server.AllowControl,
	}, logger)

	mux := http.NewServeMux()
	mux.HandleFunc("/ws", h.ServeWS)
	mux.HandleFunc("/healthz", func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte("ok\n"))
	})
	mux.Handle("/", web.StaticHandler())

	srv := &http.Server{
		Addr:              cfg.Server.Listen,
		Handler:           mux,
		ReadHeaderTimeout: 10 * time.Second,
	}

	go owner.Run(ctx)
	go h.Run(ctx)

	go func() {
		logger.Info("http listening", "addr", cfg.Server.Listen)
		if err := srv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			logger.Error("http server", "err", err)
			cancel()
		}
	}()

	<-ctx.Done()
	logger.Info("shutting down")
	shutCtx, sc := context.WithTimeout(context.Background(), 5*time.Second)
	defer sc()
	_ = srv.Shutdown(shutCtx)
}
