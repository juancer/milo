package main

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"net/http"
	"os"
	"time"

	"github.com/juancer/nexo/internal/audit"
	"github.com/juancer/nexo/internal/features"
	"github.com/juancer/nexo/internal/secrets"
	"github.com/juancer/nexo/internal/store"
	"github.com/juancer/nexo/internal/system"
	"github.com/juancer/nexo/internal/web"
	"github.com/juancer/nexo/web/admin"
)

func runServer(ctx context.Context, cfg system.Config) error {
	log, err := system.NewLogger(os.Stderr, cfg.LogLevel, cfg.LogFormat)
	if err != nil {
		return err
	}
	log.Info("starting nexo", "version", version, "hardware", cfg.Hardware, "listen", cfg.Listen)

	st, err := store.Open(ctx, cfg.DBPath())
	if err != nil {
		return err
	}
	defer st.Close()

	key, err := secrets.LoadKey(cfg.KeyPath)
	if err != nil {
		return err
	}
	vault, err := secrets.New(key, st)
	if err != nil {
		return err
	}
	if err := vault.CheckKey(ctx); err != nil {
		return err
	}

	fm := features.NewManager(st, log)
	if err := fm.Sync(ctx); err != nil {
		return err
	}
	defer func() {
		stopCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		fm.Shutdown(stopCtx)
	}()

	dist, err := fs.Sub(admin.Dist, "dist")
	if err != nil {
		return err
	}
	srv := &http.Server{
		Addr: cfg.Listen,
		Handler: web.New(web.Deps{
			Store: st, Features: fm, Audit: audit.New(st, log), Log: log,
			Version: version, Hardware: cfg.Hardware,
		}, dist),
		ReadHeaderTimeout: 10 * time.Second,
		ReadTimeout:       30 * time.Second,
		WriteTimeout:      30 * time.Second,
		IdleTimeout:       60 * time.Second,
	}

	errCh := make(chan error, 1)
	go func() { errCh <- srv.ListenAndServe() }()

	select {
	case err := <-errCh:
		return fmt.Errorf("http server: %w", err)
	case <-ctx.Done():
		log.Info("shutting down")
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		if err := srv.Shutdown(shutdownCtx); err != nil && !errors.Is(err, http.ErrServerClosed) {
			return err
		}
		return nil
	}
}
