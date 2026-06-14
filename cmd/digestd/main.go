package main

import (
	_ "time/tzdata" // embed timezone database for distroless/scratch images

	"log/slog"
	"os"

	"github.com/gbourcier/suzie/internal/config"
)

func main() {
	cfg, err := config.Load()
	if err != nil {
		slog.Error("configuration error", "err", err)
		os.Exit(1)
	}

	slog.SetDefault(slog.New(slog.NewJSONHandler(os.Stdout, &slog.HandlerOptions{
		Level: cfg.LogLevel,
	})))

	slog.Info("digestd starting", "config", cfg.Redacted())
	slog.Error("not yet implemented — build M4–M11 first")
	os.Exit(1)
}
