// Command go-bookshelf serves a personal ebook and audiobook library from a
// single binary: SQLite or Postgres for the catalog, media mounted read-only,
// and the frontend embedded in the executable.
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io/fs"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/rake-pro/go-bookshelf/internal/api"
	"github.com/rake-pro/go-bookshelf/internal/auth"
	"github.com/rake-pro/go-bookshelf/internal/config"
	"github.com/rake-pro/go-bookshelf/internal/images"
	"github.com/rake-pro/go-bookshelf/internal/library"
	"github.com/rake-pro/go-bookshelf/internal/server"
	"github.com/rake-pro/go-bookshelf/internal/settings"
	"github.com/rake-pro/go-bookshelf/internal/store"
	"github.com/rake-pro/go-bookshelf/web"
	"github.com/rs/zerolog"
	"github.com/rs/zerolog/log"
)

// version is stamped at build time via -ldflags "-X main.version=...".
var version = "dev"

func main() {
	configPath := flag.String("config", "", "path to a YAML config file (env GOBOOKSHELF_CONFIG)")
	showVersion := flag.Bool("version", false, "print the version and exit")
	flag.Parse()

	if *showVersion {
		fmt.Println("go-bookshelf", version)
		return
	}

	cfg, err := config.Load(*configPath)
	if err != nil {
		boot := bootstrapLogger()
		boot.Fatal().Err(err).Msg("load config")
	}
	initLogger(cfg.LogLevel)
	if cfg.InsecureKey {
		log.Warn().Msg("GOBOOKSHELF_DEV_INSECURE_KEY is set: stored secrets are encrypted with a " +
			"key derived from a published constant. Never use this outside local development. " +
			"Generate a real key with: openssl rand -base64 32")
	}

	if err := run(cfg); err != nil {
		log.Fatal().Err(err).Msg("go-bookshelf exited with error")
	}
}

func run(cfg config.Config) error {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	log.Info().Str("version", version).Str("listen", cfg.Listen).Msg("go-bookshelf starting")

	db, err := store.Open(ctx, cfg.DBDriver, cfg.DSN())
	if err != nil {
		return err
	}
	defer db.Close()
	// SafeDSN, not the DSN: a Postgres connection string carries a password.
	log.Info().Str("driver", cfg.DBDriver).Str("database", cfg.SafeDSN()).Msg("database ready")

	// With no data directory the cover cache is off and the process writes
	// nothing to local disk, which is what lets it be rescheduled anywhere.
	covers, err := images.NewStore(cfg.CoversDir())
	if err != nil {
		return err
	}
	if covers.Enabled() {
		log.Info().Str("dir", covers.Dir()).Msg("caching covers on local disk")
	} else {
		log.Info().Msg("no data directory configured; covers are served from the database")
	}

	authMgr := auth.New(db, cfg)

	// A database that already has accounts predates the setup wizard, so it
	// starts with setup marked complete rather than locking its owner out of
	// their own server on upgrade.
	users, err := authMgr.UserCount(ctx)
	if err != nil {
		return err
	}
	set, err := settings.New(ctx, db, cfg.SecretsKey, users > 0)
	if err != nil {
		return err
	}
	set.Register(authMgr)

	// Applying the stored settings talks to the identity provider, so bound it.
	// A provider that is briefly unreachable must not stop the server from
	// serving local sign-ins: the failure is logged and everything else applies.
	applyCtx, applyCancel := context.WithTimeout(ctx, 20*time.Second)
	if err := set.Apply(applyCtx); err != nil {
		log.Error().Err(err).Msg("OIDC discovery failed; OIDC sign-in is off until it succeeds")
	}
	applyCancel()

	if err := authMgr.PruneSessions(ctx); err != nil {
		log.Warn().Err(err).Msg("pruning expired sessions failed")
	}

	// With no account yet, print a one-time setup token. It is the only way to
	// create the first administrator, and it is never written to the database
	// in clear or logged again.
	token, err := authMgr.EnsureSetupToken(ctx)
	if err != nil {
		return err
	}
	if token != "" {
		log.Warn().Msgf("FIRST-RUN SETUP: no account exists yet. Open %s/setup and enter this one-time token: %s",
			set.Get().General.BaseURL, token)
	}

	cat := library.NewCatalog(db)
	scanner := library.NewScanner(cat, covers)
	janitor := library.NewJanitor(cat, covers)

	dist, err := fs.Sub(web.Dist, "dist")
	if err != nil {
		return err
	}
	handler := server.New(api.New(cfg, set, db, cat, authMgr, scanner, covers, version), authMgr, set, dist)

	watcher := library.NewWatcher(cat, scanner, library.DefaultDebounce)
	if err := watcher.Start(ctx); err != nil {
		log.Warn().Err(err).Msg("filesystem watching is unavailable; scans will run on the timer only")
	}
	go janitor.Start(ctx, 6*time.Hour)
	go periodicScan(ctx, scanner, set)

	srv := &http.Server{
		Addr:              cfg.Listen,
		Handler:           handler,
		ReadHeaderTimeout: 15 * time.Second,
		// No write timeout: streaming a long audiobook chapter or a large
		// download legitimately outlives any fixed deadline.
		IdleTimeout:    120 * time.Second,
		MaxHeaderBytes: 1 << 20,
	}

	errCh := make(chan error, 1)
	go func() {
		log.Info().Str("addr", cfg.Listen).Msg("listening")
		if err := srv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			errCh <- err
		}
	}()

	stop := make(chan os.Signal, 1)
	signal.Notify(stop, os.Interrupt, syscall.SIGTERM)
	select {
	case err := <-errCh:
		return err
	case sig := <-stop:
		log.Info().Str("signal", sig.String()).Msg("shutting down")
	}

	cancel()
	shutdownCtx, shutdownCancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer shutdownCancel()
	if err := srv.Shutdown(shutdownCtx); err != nil {
		return err
	}
	log.Info().Msg("go-bookshelf stopped")
	return nil
}

// periodicScan rescans every library on a timer, catching changes that the
// filesystem watcher missed (network shares often emit no events at all).
//
// The interval is read from the settings before every wait rather than captured
// once, so changing it on the admin page takes effect from the next cycle
// instead of the next restart. An interval of zero turns the timer off and
// leaves the watcher as the only trigger.
func periodicScan(ctx context.Context, scanner *library.Scanner, set *settings.Service) {
	// An initial pass at startup picks up anything that changed while the
	// server was down.
	if err := scanner.ScanAll(ctx); err != nil && ctx.Err() == nil {
		log.Error().Err(err).Msg("startup scan failed")
	}
	for {
		every := set.Get().General.ScanInterval.D()
		if every <= 0 {
			every = time.Hour
		}
		timer := time.NewTimer(every)
		select {
		case <-ctx.Done():
			timer.Stop()
			return
		case <-timer.C:
		}
		if set.Get().General.ScanInterval <= 0 {
			continue
		}
		if err := scanner.ScanAll(ctx); err != nil && ctx.Err() == nil {
			log.Error().Err(err).Msg("scheduled scan failed")
		}
	}
}

func bootstrapLogger() zerolog.Logger {
	return zerolog.New(os.Stderr).With().Timestamp().Logger()
}

func initLogger(level string) {
	parsed, err := zerolog.ParseLevel(level)
	if err != nil || level == "" {
		parsed = zerolog.InfoLevel
	}
	zerolog.SetGlobalLevel(parsed)
	zerolog.TimeFieldFormat = time.RFC3339
	log.Logger = zerolog.New(os.Stderr).With().Timestamp().Logger()
}
