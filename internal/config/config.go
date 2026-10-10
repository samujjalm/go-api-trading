package config

import (
	"errors"
	"fmt"
	"os"
	"time"
)

type Config struct {
	HTTPAddr    string
	DatabaseURL string
	JWTSecret   []byte
	JWTIssuer   string
	AccessTTL   time.Duration
	RefreshTTL  time.Duration
}

func Load() (Config, error) {
	cfg := Config{
		HTTPAddr:    getenv("HTTP_ADDR", ":8080"),
		DatabaseURL: os.Getenv("DATABASE_URL"),
		JWTSecret:   []byte(os.Getenv("JWT_SECRET")),
		JWTIssuer:   getenv("JWT_ISSUER", "go-service"),
	}

	if cfg.DatabaseURL == "" {
		return Config{}, errors.New("DATABASE_URL is required")
	}
	if len(cfg.JWTSecret) < 32 {
		return Config{}, errors.New("JWT_SECRET must be at least 32 bytes")
	}

	var err error
	if cfg.AccessTTL, err = durationEnv("ACCESS_TOKEN_TTL", 15*time.Minute); err != nil {
		return Config{}, err
	}
	if cfg.RefreshTTL, err = durationEnv("REFRESH_TOKEN_TTL", 7*24*time.Hour); err != nil {
		return Config{}, err
	}
	return cfg, nil
}

func getenv(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}

func durationEnv(key string, fallback time.Duration) (time.Duration, error) {
	v := os.Getenv(key)
	if v == "" {
		return fallback, nil
	}
	d, err := time.ParseDuration(v)
	if err != nil || d <= 0 {
		return 0, fmt.Errorf("%s must be a positive duration (e.g. 15m, 168h): %q", key, v)
	}
	return d, nil
}
