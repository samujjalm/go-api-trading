package http

import (
	"net/http"
	"strings"
	"time"

	"github.com/samujjal/go-service/internal/app"
	"github.com/samujjal/go-service/internal/domain"
)

// Request/response DTOs live in the adapter so the JSON contract can evolve
// independently of the domain model (and vice versa).

type placeOrderRequest struct {
	ClientOrderID string `json:"client_order_id"`
	Symbol        string `json:"symbol"`
	Side          string `json:"side"`
	Type          string `json:"type"`
	Price         int64  `json:"price"`
	Quantity      int64  `json:"quantity"`
}

type orderResponse struct {
	ID                string    `json:"id"`
	ClientOrderID     string    `json:"client_order_id,omitempty"`
	Symbol            string    `json:"symbol"`
	Side              string    `json:"side"`
	Type              string    `json:"type"`
	Price             int64     `json:"price,omitempty"`
	Quantity          int64     `json:"quantity"`
	FilledQuantity    int64     `json:"filled_quantity"`
	RemainingQuantity int64     `json:"remaining_quantity"`
	Reserved          int64     `json:"reserved"`
	ReservedAsset     string    `json:"reserved_asset"`
	Status            string    `json:"status"`
	CreatedAt         time.Time `json:"created_at"`
	UpdatedAt         time.Time `json:"updated_at"`
}

type tradeResponse struct {
	ID         string    `json:"id"`
	Symbol     string    `json:"symbol"`
	Price      int64     `json:"price"`
	Quantity   int64     `json:"quantity"`
	TakerSide  string    `json:"taker_side"`
	ExecutedAt time.Time `json:"executed_at"`
}

type placeOrderResponse struct {
	Order  orderResponse   `json:"order"`
	Trades []tradeResponse `json:"trades"`
}

type levelResponse struct {
	Price    int64 `json:"price"`
	Quantity int64 `json:"quantity"`
	Orders   int   `json:"orders"`
}

type bookResponse struct {
	Symbol string          `json:"symbol"`
	Bids   []levelResponse `json:"bids"`
	Asks   []levelResponse `json:"asks"`
}

func toOrderResponse(o *domain.Order) orderResponse {
	return orderResponse{
		ID: o.ID, ClientOrderID: o.ClientOrderID, Symbol: o.Symbol,
		Side: string(o.Side), Type: string(o.Type), Price: o.Price,
		Quantity: o.Quantity, FilledQuantity: o.FilledQuantity, RemainingQuantity: o.Remaining(),
		Reserved: o.Reserved, ReservedAsset: o.ReserveAsset(),
		Status: string(o.Status), CreatedAt: o.CreatedAt, UpdatedAt: o.UpdatedAt,
	}
}

func toTradeResponses(ts []*domain.Trade) []tradeResponse {
	out := make([]tradeResponse, 0, len(ts)) // non-nil so JSON renders [] not null
	for _, t := range ts {
		out = append(out, tradeResponse{
			ID: t.ID, Symbol: t.Symbol, Price: t.Price, Quantity: t.Quantity,
			TakerSide: string(t.TakerSide), ExecutedAt: t.ExecutedAt,
		})
	}
	return out
}

func toLevels(ls []domain.PriceLevel) []levelResponse {
	out := make([]levelResponse, 0, len(ls))
	for _, l := range ls {
		out = append(out, levelResponse{Price: l.Price, Quantity: l.Quantity, Orders: l.Orders})
	}
	return out
}

func (s *Server) handlePlaceOrder(w http.ResponseWriter, r *http.Request) {
	var req placeOrderRequest
	if err := decodeJSON(w, r, &req); err != nil {
		s.fail(w, r, err)
		return
	}
	res, err := s.trading.PlaceOrder(r.Context(), domain.NewOrderParams{
		UserID:        userID(r.Context()),
		ClientOrderID: req.ClientOrderID,
		Symbol:        req.Symbol,
		Side:          domain.Side(strings.ToUpper(req.Side)),
		Type:          domain.OrderType(strings.ToUpper(req.Type)),
		Price:         req.Price,
		Quantity:      req.Quantity,
	})
	if err != nil {
		s.fail(w, r, err)
		return
	}

	status := http.StatusCreated
	if res.Replayed {
		status = http.StatusOK
		w.Header().Set("Idempotent-Replayed", "true")
	}
	writeJSON(w, status, placeOrderResponse{Order: toOrderResponse(res.Order), Trades: toTradeResponses(res.Trades)})
}

func (s *Server) handleListOrders(w http.ResponseWriter, r *http.Request) {
	limit, err := queryInt(r, "limit")
	if err != nil {
		s.fail(w, r, err)
		return
	}
	offset, err := queryInt(r, "offset")
	if err != nil {
		s.fail(w, r, err)
		return
	}
	q := r.URL.Query()
	orders, err := s.trading.ListOrders(r.Context(), userID(r.Context()), app.OrderFilter{
		Symbol: q.Get("symbol"),
		Status: domain.OrderStatus(strings.ToUpper(q.Get("status"))),
		Limit:  limit,
		Offset: offset,
	})
	if err != nil {
		s.fail(w, r, err)
		return
	}
	out := make([]orderResponse, 0, len(orders))
	for _, o := range orders {
		out = append(out, toOrderResponse(o))
	}
	writeJSON(w, http.StatusOK, map[string]any{"orders": out})
}

func (s *Server) handleGetOrder(w http.ResponseWriter, r *http.Request) {
	o, err := s.trading.GetOrder(r.Context(), userID(r.Context()), r.PathValue("id"))
	if err != nil {
		s.fail(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, toOrderResponse(o))
}

func (s *Server) handleCancelOrder(w http.ResponseWriter, r *http.Request) {
	o, err := s.trading.CancelOrder(r.Context(), userID(r.Context()), r.PathValue("id"))
	if err != nil {
		s.fail(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, toOrderResponse(o))
}

func (s *Server) handleOrderBook(w http.ResponseWriter, r *http.Request) {
	depth, err := queryInt(r, "depth")
	if err != nil {
		s.fail(w, r, err)
		return
	}
	book, err := s.trading.OrderBook(r.Context(), r.PathValue("symbol"), depth)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, bookResponse{Symbol: book.Symbol, Bids: toLevels(book.Bids), Asks: toLevels(book.Asks)})
}

func (s *Server) handleRecentTrades(w http.ResponseWriter, r *http.Request) {
	limit, err := queryInt(r, "limit")
	if err != nil {
		s.fail(w, r, err)
		return
	}
	trades, err := s.trading.RecentTrades(r.Context(), r.PathValue("symbol"), limit)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"trades": toTradeResponses(trades)})
}
