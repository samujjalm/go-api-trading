// Package http is the driving adapter: it turns HTTP requests into calls on
// the app's inbound ports and maps results and domain errors back to JSON.
package http

import (
	"context"
	"log/slog"
	"net/http"

	"github.com/samujjal/go-service/internal/app"
)

type HealthChecker interface {
	Ping(ctx context.Context) error
}

type Server struct {
	auth    app.AuthUseCase
	trading app.TradingUseCase
	account app.AccountUseCase
	health  HealthChecker
	log     *slog.Logger
}

func NewServer(auth app.AuthUseCase, trading app.TradingUseCase, account app.AccountUseCase, health HealthChecker, log *slog.Logger) *Server {
	return &Server{auth: auth, trading: trading, account: account, health: health, log: log}
}

// Handler wires routes using the Go 1.22+ ServeMux patterns ("METHOD /path/{param}").
func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()

	mux.HandleFunc("GET /healthz", s.handleHealth)

	mux.HandleFunc("POST /api/v1/auth/register", s.handleRegister)
	mux.HandleFunc("POST /api/v1/auth/login", s.handleLogin)
	mux.HandleFunc("POST /api/v1/auth/refresh", s.handleRefresh)
	mux.HandleFunc("POST /api/v1/auth/logout", s.handleLogout)
	mux.Handle("GET /api/v1/me", s.requireAuth(s.handleMe))

	mux.Handle("GET /api/v1/account/balances", s.requireAuth(s.handleBalances))
	mux.Handle("GET /api/v1/account/movements", s.requireAuth(s.handleMovements))
	mux.Handle("POST /api/v1/account/deposits", s.requireAuth(s.handleDeposit))
	mux.Handle("POST /api/v1/account/withdrawals", s.requireAuth(s.handleWithdraw))

	mux.Handle("POST /api/v1/orders", s.requireAuth(s.handlePlaceOrder))
	mux.Handle("GET /api/v1/orders", s.requireAuth(s.handleListOrders))
	mux.Handle("GET /api/v1/orders/{id}", s.requireAuth(s.handleGetOrder))
	mux.Handle("DELETE /api/v1/orders/{id}", s.requireAuth(s.handleCancelOrder))

	mux.HandleFunc("GET /api/v1/markets/{symbol}/book", s.handleOrderBook)
	mux.HandleFunc("GET /api/v1/markets/{symbol}/trades", s.handleRecentTrades)

	// Outermost first: request ID → access log → panic recovery → routes.
	return s.withRequestID(s.withLogging(s.withRecover(mux)))
}

func (s *Server) handleHealth(w http.ResponseWriter, r *http.Request) {
	if err := s.health.Ping(r.Context()); err != nil {
		logger(r.Context(), s.log).Error("health check failed", "err", err)
		writeError(w, http.StatusServiceUnavailable, "unavailable", "database unreachable")
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
}
