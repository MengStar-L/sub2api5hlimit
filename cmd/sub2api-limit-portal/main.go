package main

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"runtime"
	"syscall"
	"time"

	"github.com/MengStar-L/sub2api5hlimit/internal/config"
	"github.com/MengStar-L/sub2api5hlimit/internal/httpapi"
	"github.com/MengStar-L/sub2api5hlimit/internal/releasecheck"
	"github.com/MengStar-L/sub2api5hlimit/internal/secure"
	"github.com/MengStar-L/sub2api5hlimit/internal/store"
	"github.com/MengStar-L/sub2api5hlimit/internal/syncer"
	"github.com/MengStar-L/sub2api5hlimit/internal/webui"
)

var version = "dev"

func main() {
	if err := run(os.Args[1:]); err != nil {
		fmt.Fprintln(os.Stderr, "sub2api-limit-portal:", secure.Redact(err.Error()))
		os.Exit(1)
	}
}

func run(args []string) error {
	command := "serve"
	if len(args) > 0 {
		command = args[0]
	}
	switch command {
	case "serve":
		if len(args) > 1 {
			return fmt.Errorf("serve does not accept arguments")
		}
		return serve()
	case "keygen":
		if len(args) > 1 {
			return fmt.Errorf("keygen does not accept arguments")
		}
		key := make([]byte, 32)
		if _, err := rand.Read(key); err != nil {
			return fmt.Errorf("generate master key: %w", err)
		}
		fmt.Printf("%s=%s\n", config.EnvMasterKey, base64.StdEncoding.EncodeToString(key))
		return nil
	case "version", "--version", "-version":
		fmt.Printf("sub2api-limit-portal %s (%s/%s)\n", version, runtime.GOOS, runtime.GOARCH)
		return nil
	case "help", "--help", "-h":
		fmt.Println("Usage: sub2api-limit-portal [serve|keygen|version]")
		return nil
	default:
		return fmt.Errorf("unknown command %q; use serve, keygen, or version", command)
	}
}

func serve() error {
	cfg, err := config.Load()
	if err != nil {
		return err
	}
	box, err := secure.NewBox(cfg.MasterKey)
	if err != nil {
		return err
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	data, err := store.Open(ctx, cfg.DBPath, box)
	if err != nil {
		return fmt.Errorf("open database: %w", err)
	}
	defer data.Close()

	logger := slog.New(slog.NewJSONHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelInfo}))
	upstream := syncer.New(data, logger)
	updates, err := releasecheck.New(data, logger, releasecheck.Config{
		CurrentVersion: version,
		DataDir:        filepath.Dir(cfg.DBPath),
		StatusPath:     cfg.UpdateStatusPath,
		UpdaterPath:    cfg.UpdaterPath,
	})
	if err != nil {
		return fmt.Errorf("initialize release checker: %w", err)
	}
	api, err := httpapi.New(data, upstream, logger, cfg.CookieSecure)
	if err != nil {
		return fmt.Errorf("initialize HTTP API: %w", err)
	}
	api.SetUpdateManager(updates)
	api.MountFrontend(webui.Handler())

	listener, err := net.Listen("tcp", cfg.Listen)
	if err != nil {
		return fmt.Errorf("listen on %s: %w", cfg.Listen, err)
	}
	defer listener.Close()

	server := &http.Server{
		Handler:           api.Handler(),
		ReadHeaderTimeout: 5 * time.Second,
		ReadTimeout:       15 * time.Second,
		WriteTimeout:      60 * time.Second,
		IdleTimeout:       90 * time.Second,
		MaxHeaderBytes:    32 << 10,
	}
	if token := api.SetupToken(); token != "" {
		logger.Info("first-run setup is available for 30 minutes", "setup_token", token)
	}
	logger.Info("Sub2API quota center started", "listen", listener.Addr().String(), "version", version)
	go upstream.Run(ctx)
	go updates.Run(ctx)

	serveErr := make(chan error, 1)
	go func() { serveErr <- server.Serve(listener) }()
	select {
	case err := <-serveErr:
		if !errors.Is(err, http.ErrServerClosed) {
			return err
		}
		return nil
	case <-ctx.Done():
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		if err := server.Shutdown(shutdownCtx); err != nil {
			return fmt.Errorf("shut down HTTP server: %w", err)
		}
		if err := <-serveErr; !errors.Is(err, http.ErrServerClosed) {
			return err
		}
		return nil
	}
}
