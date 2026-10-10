package http

import (
	"encoding/json"
	"errors"
	"net/http"
	"strconv"

	"github.com/samujjal/go-service/internal/domain"
)

type errorBody struct {
	Error errorDetail `json:"error"`
}

type errorDetail struct {
	Code    string `json:"code"`
	Message string `json:"message"`
	Field   string `json:"field,omitempty"`
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	json.NewEncoder(w).Encode(v)
}

func writeError(w http.ResponseWriter, status int, code, msg string) {
	writeJSON(w, status, errorBody{Error: errorDetail{Code: code, Message: msg}})
}

// fail maps domain errors to HTTP. Anything unrecognised is a 500 whose
// details are logged but never leaked to the client.
func (s *Server) fail(w http.ResponseWriter, r *http.Request, err error) {
	var ve *domain.ValidationError
	switch {
	case errors.As(err, &ve):
		writeJSON(w, http.StatusBadRequest, errorBody{Error: errorDetail{
			Code: "validation_failed", Message: ve.Message, Field: ve.Field,
		}})
	case errors.Is(err, domain.ErrNotFound):
		writeError(w, http.StatusNotFound, "not_found", "resource not found")
	case errors.Is(err, domain.ErrConflict):
		writeError(w, http.StatusConflict, "conflict", err.Error())
	case errors.Is(err, domain.ErrInsufficientFunds):
		writeError(w, http.StatusUnprocessableEntity, "insufficient_funds", err.Error())
	case errors.Is(err, domain.ErrOrderNotOpen):
		writeError(w, http.StatusConflict, "order_not_open", err.Error())
	case errors.Is(err, domain.ErrInvalidCredentials):
		writeError(w, http.StatusUnauthorized, "invalid_credentials", err.Error())
	case errors.Is(err, domain.ErrRefreshTokenReused):
		logger(r.Context(), s.log).Warn("refresh token reuse detected; all sessions revoked")
		writeError(w, http.StatusUnauthorized, "invalid_token", domain.ErrInvalidToken.Error())
	case errors.Is(err, domain.ErrInvalidToken):
		writeError(w, http.StatusUnauthorized, "invalid_token", err.Error())
	default:
		logger(r.Context(), s.log).Error("internal error", "err", err)
		writeError(w, http.StatusInternalServerError, "internal", "internal server error")
	}
}

const maxBodyBytes = 1 << 20

func decodeJSON(w http.ResponseWriter, r *http.Request, dst any) error {
	r.Body = http.MaxBytesReader(w, r.Body, maxBodyBytes)
	dec := json.NewDecoder(r.Body)
	dec.DisallowUnknownFields()
	if err := dec.Decode(dst); err != nil {
		return &domain.ValidationError{Field: "body", Message: "invalid JSON: " + err.Error()}
	}
	if dec.More() {
		return &domain.ValidationError{Field: "body", Message: "must contain a single JSON object"}
	}
	return nil
}

func queryInt(r *http.Request, name string) (int, error) {
	v := r.URL.Query().Get(name)
	if v == "" {
		return 0, nil
	}
	n, err := strconv.Atoi(v)
	if err != nil || n < 0 {
		return 0, &domain.ValidationError{Field: name, Message: "must be a non-negative integer"}
	}
	return n, nil
}
