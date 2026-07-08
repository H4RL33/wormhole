package types

import (
	"os"
	"strconv"
)

type Config struct {
	ListenAddr       string
	DatabaseURL      string
	KBDedupThreshold float64
}

func LoadConfig() Config {
	threshold := 0.85
	if val, ok := os.LookupEnv("WORMHOLE_KB_DEDUP_THRESHOLD"); ok {
		if parsed, err := strconv.ParseFloat(val, 64); err == nil {
			threshold = parsed
		}
	}
	return Config{
		ListenAddr:       getEnv("WORMHOLE_LISTEN_ADDR", ":8080"),
		DatabaseURL:      getEnv("WORMHOLE_DATABASE_URL", "postgres://wormhole:wormhole@localhost:5432/wormhole?sslmode=prefer"),
		KBDedupThreshold: threshold,
	}
}

func getEnv(key, fallback string) string {
	if v, ok := os.LookupEnv(key); ok {
		return v
	}
	return fallback
}
