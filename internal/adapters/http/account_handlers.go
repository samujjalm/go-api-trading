package http

import (
	"context"
	"net/http"
	"time"

	"github.com/samujjal/go-service/internal/domain"
)

type transferRequest struct {
	Asset  string `json:"asset"`
	Amount int64  `json:"amount"`
}

type balanceResponse struct {
	Asset     string `json:"asset"`
	Available int64  `json:"available"`
	Reserved  int64  `json:"reserved"`
	Total     int64  `json:"total"`
}

type movementResponse struct {
	ID             string    `json:"id"`
	Asset          string    `json:"asset"`
	Kind           string    `json:"kind"`
	DeltaAvailable int64     `json:"delta_available"`
	DeltaReserved  int64     `json:"delta_reserved"`
	OrderID        string    `json:"order_id,omitempty"`
	TradeID        string    `json:"trade_id,omitempty"`
	CreatedAt      time.Time `json:"created_at"`
}

func toBalanceResponse(b *domain.Balance) balanceResponse {
	return balanceResponse{Asset: b.Asset, Available: b.Available, Reserved: b.Reserved, Total: b.Total()}
}

func (s *Server) handleBalances(w http.ResponseWriter, r *http.Request) {
	bs, err := s.account.Balances(r.Context(), userID(r.Context()))
	if err != nil {
		s.fail(w, r, err)
		return
	}
	out := make([]balanceResponse, 0, len(bs))
	for _, b := range bs {
		out = append(out, toBalanceResponse(b))
	}
	writeJSON(w, http.StatusOK, map[string]any{"balances": out})
}

func (s *Server) handleDeposit(w http.ResponseWriter, r *http.Request) {
	s.transfer(w, r, s.account.Deposit)
}

func (s *Server) handleWithdraw(w http.ResponseWriter, r *http.Request) {
	s.transfer(w, r, s.account.Withdraw)
}

type transferFunc func(ctx context.Context, userID, asset string, amount int64) (*domain.Balance, error)

func (s *Server) transfer(w http.ResponseWriter, r *http.Request, op transferFunc) {
	var req transferRequest
	if err := decodeJSON(w, r, &req); err != nil {
		s.fail(w, r, err)
		return
	}
	b, err := op(r.Context(), userID(r.Context()), req.Asset, req.Amount)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, toBalanceResponse(b))
}

func (s *Server) handleMovements(w http.ResponseWriter, r *http.Request) {
	limit, err := queryInt(r, "limit")
	if err != nil {
		s.fail(w, r, err)
		return
	}
	ms, err := s.account.Movements(r.Context(), userID(r.Context()), limit)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	out := make([]movementResponse, 0, len(ms))
	for _, m := range ms {
		out = append(out, movementResponse{
			ID: m.ID, Asset: m.Asset, Kind: string(m.Kind), DeltaAvailable: m.DeltaAvailable,
			DeltaReserved: m.DeltaReserved, OrderID: m.OrderID, TradeID: m.TradeID, CreatedAt: m.CreatedAt,
		})
	}
	writeJSON(w, http.StatusOK, map[string]any{"movements": out})
}
