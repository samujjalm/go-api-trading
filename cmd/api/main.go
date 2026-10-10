// Command api is the composition root: the only place that knows about every
// concrete adapter and wires them into the application services.
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

	"golang.org/x/crypto/bcrypt"

	httpadapter "github.com/samujjal/go-service/internal/adapters/http"
	"github.com/samujjal/go-service/internal/adapters/postgres"
	"github.com/samujjal/go-service/internal/adapters/security"
	"github.com/samujjal/go-service/internal/app"
	"github.com/samujjal/go-service/internal/config"
)

func main() {
	log := slog.New(slog.NewJSONHandler(os.Stdout, nil))
	if err := run(log); err != nil {
		log.Error("fatal", "err", err)
		os.Exit(1)
	}
}

func run(log *slog.Logger) error {
	cfg, err := config.Load()
	if err != nil {
		return err
	}

	// ctx is cancelled on SIGINT/SIGTERM (Ctrl-C, `docker stop`, Kubernetes).
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	pool, err := postgres.Connect(ctx, cfg.DatabaseURL)
	if err != nil {
		return err
	}
	defer pool.Close()
	if err := postgres.Migrate(ctx, pool); err != nil {
		return err
	}

	store := postgres.NewStore(pool)
	authSvc := app.NewAuthService(store,
		security.NewBcryptHasher(bcrypt.DefaultCost),
		security.NewJWTIssuer(cfg.JWTSecret, cfg.JWTIssuer, cfg.AccessTTL),
		cfg.RefreshTTL)
	tradingSvc := app.NewTradingService(store)

	srv := &http.Server{
		Addr:              cfg.HTTPAddr,
		Handler:           httpadapter.NewServer(authSvc, tradingSvc, app.NewAccountService(store), pool, log).Handler(),
		ReadHeaderTimeout: 5 * time.Second, // mitigates slowloris
		ReadTimeout:       10 * time.Second,
		WriteTimeout:      15 * time.Second,
		IdleTimeout:       60 * time.Second,
	}

	errCh := make(chan error, 1)
	go func() {
		log.Info("listening", "addr", cfg.HTTPAddr)
		errCh <- srv.ListenAndServe()
	}()

	select {
	case err := <-errCh:
		if !errors.Is(err, http.ErrServerClosed) {
			return err
		}
	case <-ctx.Done():
		log.Info("shutting down")
	}

	// Stop accepting new connections and let in-flight requests (and their
	// DB transactions) finish before the pool is closed.
	shutdownCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	return srv.Shutdown(shutdownCtx)
}
