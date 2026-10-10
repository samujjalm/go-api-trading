package http

import (
	"net/http"
	"time"

	"github.com/samujjal/go-service/internal/app"
)

type credentialsRequest struct {
	Email    string `json:"email"`
	Password string `json:"password"`
}

type refreshRequest struct {
	RefreshToken string `json:"refresh_token"`
}

type tokenResponse struct {
	AccessToken  string `json:"access_token"`
	RefreshToken string `json:"refresh_token"`
	TokenType    string `json:"token_type"`
	ExpiresIn    int    `json:"expires_in"` // seconds, as in OAuth 2.0
}

type userResponse struct {
	ID        string    `json:"id"`
	Email     string    `json:"email"`
	CreatedAt time.Time `json:"created_at"`
}

func toTokenResponse(p app.TokenPair) tokenResponse {
	return tokenResponse{
		AccessToken:  p.AccessToken,
		RefreshToken: p.RefreshToken,
		TokenType:    "Bearer",
		ExpiresIn:    int(p.ExpiresIn.Seconds()),
	}
}

func (s *Server) handleRegister(w http.ResponseWriter, r *http.Request) {
	var req credentialsRequest
	if err := decodeJSON(w, r, &req); err != nil {
		s.fail(w, r, err)
		return
	}
	u, err := s.auth.Register(r.Context(), req.Email, req.Password)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	writeJSON(w, http.StatusCreated, userResponse{ID: u.ID, Email: u.Email, CreatedAt: u.CreatedAt})
}

func (s *Server) handleLogin(w http.ResponseWriter, r *http.Request) {
	var req credentialsRequest
	if err := decodeJSON(w, r, &req); err != nil {
		s.fail(w, r, err)
		return
	}
	pair, err := s.auth.Login(r.Context(), req.Email, req.Password)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	w.Header().Set("Cache-Control", "no-store")
	writeJSON(w, http.StatusOK, toTokenResponse(pair))
}

func (s *Server) handleRefresh(w http.ResponseWriter, r *http.Request) {
	var req refreshRequest
	if err := decodeJSON(w, r, &req); err != nil {
		s.fail(w, r, err)
		return
	}
	pair, err := s.auth.Refresh(r.Context(), req.RefreshToken)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	w.Header().Set("Cache-Control", "no-store")
	writeJSON(w, http.StatusOK, toTokenResponse(pair))
}

func (s *Server) handleLogout(w http.ResponseWriter, r *http.Request) {
	var req refreshRequest
	if err := decodeJSON(w, r, &req); err != nil {
		s.fail(w, r, err)
		return
	}
	if err := s.auth.Logout(r.Context(), req.RefreshToken); err != nil {
		s.fail(w, r, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) handleMe(w http.ResponseWriter, r *http.Request) {
	u, err := s.auth.GetUser(r.Context(), userID(r.Context()))
	if err != nil {
		s.fail(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, userResponse{ID: u.ID, Email: u.Email, CreatedAt: u.CreatedAt})
}
