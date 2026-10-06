package main

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"roomcade/internal/app"
)

func main() {
	cfg := app.Config{
		DemoPassword:         os.Getenv("DEMO_PASSWORD"),
		DemoPasswordRequired: env("DEMO_PASSWORD_REQUIRED", "false") == "true",
		Addr:                 env("ADDR", ":"+env("PORT", "8080")),
		EmbeddingEnabled:     env("EMBEDDING_ENABLED", "false") == "true",
		DatabasePath:         env("DATABASE_PATH", "roomcade.db"),
		StaticDir:            env("STATIC_DIR", "dist"),
		AllowedOrigins:       split(env("ALLOWED_ORIGINS", "http://localhost:5173,http://localhost:8080")),
		SecureCookies:        env("SECURE_COOKIES", "false") == "true",
		LiveKitURL:           os.Getenv("LIVEKIT_URL"),
		LiveKitAPIKey:        os.Getenv("LIVEKIT_API_KEY"),
		LiveKitSecret:        os.Getenv("LIVEKIT_API_SECRET"),
		MetricsToken:         os.Getenv("METRICS_TOKEN"),
	}
	application, err := app.New(cfg)
	if err != nil {
		slog.Error("roomcade startup failed", "error", err)
		os.Exit(1)
	}
	defer application.Close()
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()
	application.StartCleanup(ctx)
	server := &http.Server{Addr: cfg.Addr, Handler: application.Handler(), ReadHeaderTimeout: 5 * time.Second, IdleTimeout: 60 * time.Second}
	go func() {
		slog.Info("roomcade listening", "addr", cfg.Addr)
		if err := server.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			slog.Error("server stopped", "error", err)
			stop()
		}
	}()
	<-ctx.Done()
	shutdown, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	_ = server.Shutdown(shutdown)
}

func env(key, fallback string) string {
	if value := os.Getenv(key); value != "" {
		return value
	}
	return fallback
}
func split(value string) []string {
	values := strings.Split(value, ",")
	for i := range values {
		values[i] = strings.TrimSpace(values[i])
	}
	return values
}
