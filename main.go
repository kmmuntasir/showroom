// Command democtl is the on-prem demo hoster (docs/demos.md): one static
// binary serving the control dashboard and every <name>.example.com demo
// site, multiplexed by Host on a single LAN-only listener.
//
// Configuration is env-only through internal/config; the systemd unit reads
// /etc/democtl/democtl.env. Boot performs the hygiene passes — wipe stale
// upload staging, expire stale sessions — and fails hard on
// misconfiguration: a demo hoster with a broken auth or store contract must
// not half-start.
package main

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"democtl/internal/audit"
	"democtl/internal/auth"
	"democtl/internal/config"
	"democtl/internal/server"
	"democtl/internal/store"
	"democtl/internal/upload"
	"democtl/web"
)

func main() {
	if err := run(); err != nil {
		slog.Error("democtl exiting", "err", err)
		os.Exit(1)
	}
}

func run() error {
	slog.SetDefault(slog.New(slog.NewJSONHandler(os.Stderr, nil)))

	cfg, err := config.Load(os.Getenv)
	if err != nil {
		return err
	}

	st, err := store.Open(cfg.DBPath)
	if err != nil {
		return err
	}
	defer st.Close()

	auditLog, err := audit.Open(cfg.AuditPath)
	if err != nil {
		return err
	}
	defer auditLog.Close()

	uploads := &upload.Store{DataDir: cfg.DataDir, Limits: cfg.Limits}
	if err := uploads.CleanTmp(); err != nil {
		// Staging debris from a crashed deploy must never block boot.
		slog.Warn("tmp cleanup failed", "err", err)
	}
	// Expired sessions are dead weight; sweep once per boot (the service
	// restarts rarely, so also sweep hourly while running).
	if n, err := st.DeleteExpiredSessions(time.Now().UTC().Unix()); err != nil {
		slog.Warn("session sweep failed", "err", err)
	} else if n > 0 {
		slog.Info("expired sessions swept", "count", n)
	}

	authn := auth.New(auth.Deps{
		Cfg:      cfg,
		Sessions: st,
	})

	srv := &server.Server{
		Cfg:     cfg,
		DB:      st,
		Audit:   auditLog,
		Uploads: uploads,
		Auth:    authn,
		Web:     web.Dist(),
	}
	httpSrv := &http.Server{
		Addr:              cfg.Listen,
		Handler:           srv.Handler(),
		ReadHeaderTimeout: 10 * time.Second,
	}

	errCh := make(chan error, 1)
	go func() {
		slog.Info("democtl listening", "addr", cfg.Listen, "control_host", cfg.ControlHost)
		errCh <- httpSrv.ListenAndServe()
	}()

	// Hourly hygiene while running.
	sweep := time.NewTicker(time.Hour)
	defer sweep.Stop()

	stop := make(chan os.Signal, 1)
	signal.Notify(stop, os.Interrupt, syscall.SIGTERM)

	// Run until the server fails or a signal arrives; the hourly sweep is
	// just an event inside the loop — it must never fall out of this
	// select (that used to exit the process one hour after boot).
	for {
		select {
		case err := <-errCh:
			if !errors.Is(err, http.ErrServerClosed) {
				return err
			}
			return nil
		case sig := <-stop:
			slog.Info("shutting down", "signal", sig.String())
			ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			defer cancel()
			return httpSrv.Shutdown(ctx)
		case <-sweep.C:
			// Inline, not a goroutine: the sweep must never race the
			// deferred store Close on the way out.
			if _, err := st.DeleteExpiredSessions(time.Now().UTC().Unix()); err != nil {
				slog.Warn("session sweep failed", "err", err)
			}
		}
	}
}
