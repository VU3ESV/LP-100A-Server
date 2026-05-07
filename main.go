package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"lp100a-server/internal/config"
	"lp100a-server/internal/hub"
	"lp100a-server/internal/meter"
	"lp100a-server/internal/web"
)

func main() {
	configPath := flag.String("config", "/etc/lp100a-server/config.toml", "config file path (missing file is fine; defaults are used)")
	verbose := flag.Bool("v", false, "verbose logging at start (debug-level); the level can also be changed at runtime via /api/log-level or the web UI's Setup overlay")
	flag.Parse()

	// LevelVar is thread-safe and lets us flip the log level at runtime
	// (via the /api/log-level endpoint below). Default is error-only;
	// -v starts at debug.
	var levelVar slog.LevelVar
	levelVar.Set(slog.LevelError)
	if *verbose {
		levelVar.Set(slog.LevelDebug)
	}
	logger := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: &levelVar}))

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
	mux.HandleFunc("/api/config", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("Cache-Control", "no-store")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"views":         cfg.UI.EnabledViews,
			"allow_control": cfg.Server.AllowControl,
		})
	})
	mux.HandleFunc("/api/log-level", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("Cache-Control", "no-store")
		switch r.Method {
		case http.MethodGet:
			_ = json.NewEncoder(w).Encode(map[string]string{"level": slogLevelName(levelVar.Level())})
		case http.MethodPost:
			var req struct {
				Level string `json:"level"`
			}
			if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
				http.Error(w, `{"error":"bad json"}`, http.StatusBadRequest)
				return
			}
			lvl, ok := parseSlogLevel(req.Level)
			if !ok {
				http.Error(w, `{"error":"level must be one of: error, warn, info, debug"}`, http.StatusBadRequest)
				return
			}
			prev := levelVar.Level()
			levelVar.Set(lvl)
			// Force-log the change at error level so it lands in the
			// journal regardless of the current level setting.
			logger.Error("log level changed", "from", slogLevelName(prev), "to", slogLevelName(lvl), "remote", r.RemoteAddr)
			_ = json.NewEncoder(w).Encode(map[string]string{"level": slogLevelName(lvl)})
		default:
			w.Header().Set("Allow", "GET, POST")
			http.Error(w, `{"error":"method not allowed"}`, http.StatusMethodNotAllowed)
		}
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

func slogLevelName(l slog.Level) string {
	switch {
	case l <= slog.LevelDebug:
		return "debug"
	case l <= slog.LevelInfo:
		return "info"
	case l <= slog.LevelWarn:
		return "warn"
	default:
		return "error"
	}
}

func parseSlogLevel(s string) (slog.Level, bool) {
	switch strings.ToLower(strings.TrimSpace(s)) {
	case "debug":
		return slog.LevelDebug, true
	case "info":
		return slog.LevelInfo, true
	case "warn", "warning":
		return slog.LevelWarn, true
	case "error", "err":
		return slog.LevelError, true
	}
	return 0, false
}
