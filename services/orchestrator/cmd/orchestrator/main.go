package main

import (
	"context"
	"log/slog"
	"os"
	"os/signal"
	"syscall"

	"github.com/IvanKuzyshyn/temporal-k8s-orchestration-demo/internal/process"
	"github.com/IvanKuzyshyn/temporal-k8s-orchestration-demo/internal/runtimeconfig"
)

func main() {
	logger := slog.New(slog.NewJSONHandler(os.Stdout, nil))
	c, err := runtimeconfig.Load("orchestrator", os.Getenv)
	if err != nil {
		logger.Error("invalid configuration", "error", err)
		os.Exit(1)
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	if err = process.Run(ctx, "orchestrator", c, logger); err != nil {
		logger.Error("process stopped", "error", err)
		os.Exit(1)
	}
}
