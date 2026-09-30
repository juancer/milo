package main

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"log/slog"
	"net/http"
	"os"
	"path/filepath"
	"time"

	"github.com/juancer/nexo/internal/app"
	"github.com/juancer/nexo/internal/auth"
	"github.com/juancer/nexo/internal/backup"
	"github.com/juancer/nexo/internal/secrets"
	"github.com/juancer/nexo/internal/store"
	"github.com/juancer/nexo/internal/system"
	"github.com/juancer/nexo/internal/web"
	"github.com/juancer/nexo/web/admin"
)

func runServer(ctx context.Context, cfg system.Config) error {
	base, err := system.NewLogger(os.Stderr, cfg.LogLevel, cfg.LogFormat)
	if err != nil {
		return err
	}
	logs := system.NewRing(300)
	log := slog.New(system.WithRing(base.Handler(), logs))
	ctx, restart := context.WithCancel(ctx)
	defer restart()
	log.Info("starting nexo", "version", version, "hardware", cfg.Hardware, "listen", cfg.Listen)

	// A restore chosen in the admin UI waits for this moment: the database is
	// not open yet, so it can be swapped safely.
	// Leftovers of a crash or kill (the restore and backup workspaces, half-written downloads).
	backup.SweepTemp(cfg.DataDir, time.Minute)
	if applied, previous, err := backup.ApplyPending(cfg.DataDir); err != nil {
		// Under Restart=always a restore that keeps failing (a full disk, say) would stop the
		// service from ever starting: set it aside and carry on with the current database.
		dest, qerr := backup.Quarantine(cfg.DataDir)
		log.Error("cannot apply the pending restore; continuing on the current database", "error", err,
			"set_aside_as", filepath.Base(dest), "quarantine_error", qerr)
	} else if applied {
		log.Info("restored from a backup", "previous_database_kept_as", filepath.Base(previous))
	}

	st, err := store.Open(ctx, cfg.DBPath())
	if err != nil {
		return err
	}
	defer st.Close()

	// Started without `nexo install` (for example by hand): still never run
	// without an administrator, and never with a well-known password.
	if created, user, pass, err := auth.NewAccounts(st).EnsureFirstAdmin(ctx); err != nil {
		return err
	} else if created {
		system.PrintFirstAdmin(os.Stderr, "http://<this-device>"+cfg.Listen, user, pass)
	}

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

	application, err := app.New(ctx, cfg, log, st, vault)
	if err != nil {
		return err
	}
	defer application.Close()
	application.Logs, application.RestartFunc = logs, func() {
		time.Sleep(500 * time.Millisecond) // let the HTTP response go out first
		restart()
	}

	dist, err := fs.Sub(admin.Dist, "dist")
	if err != nil {
		return err
	}
	srv := &http.Server{
		Addr:              cfg.Listen,
		Handler:           web.New(application, version, dist),
		ReadHeaderTimeout: 10 * time.Second,
		ReadTimeout:       30 * time.Second,
		WriteTimeout:      30 * time.Second,
		IdleTimeout:       60 * time.Second,
	}

	// Keep the audit log from growing without bound on the SD card.
	go func() {
		t := time.NewTicker(time.Hour)
		defer t.Stop()
		for {
			if err := application.Audit.Prune(ctx); err != nil {
				log.Warn("audit prune", "error", err)
			}
			select {
			case <-t.C:
			case <-ctx.Done():
				return
			}
		}
	}()

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
