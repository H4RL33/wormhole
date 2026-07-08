package types

import (
	"os"
	"strconv"
)

type Config struct {
	ListenAddr          string
	DatabaseURL         string
	KBDedupThreshold    float64
	KBMaxBodyLength     int
	KBMinLinksDecision  int
	KBMinLinksPolicy    int
	KBMinLinksProcedure int
}

func LoadConfig() Config {
	threshold := 0.85
	if val, ok := os.LookupEnv("WORMHOLE_KB_DEDUP_THRESHOLD"); ok {
		if parsed, err := strconv.ParseFloat(val, 64); err == nil {
			threshold = parsed
		}
	}
	maxBodyLength := 2000
	if val, ok := os.LookupEnv("WORMHOLE_KB_MAX_BODY_LENGTH"); ok {
		if parsed, err := strconv.Atoi(val); err == nil {
			maxBodyLength = parsed
		}
	}
	minLinksDecision := 1
	if val, ok := os.LookupEnv("WORMHOLE_KB_MIN_LINKS_DECISION"); ok {
		if parsed, err := strconv.Atoi(val); err == nil {
			minLinksDecision = parsed
		}
	}
	minLinksPolicy := 1
	if val, ok := os.LookupEnv("WORMHOLE_KB_MIN_LINKS_POLICY"); ok {
		if parsed, err := strconv.Atoi(val); err == nil {
			minLinksPolicy = parsed
		}
	}
	minLinksProcedure := 1
	if val, ok := os.LookupEnv("WORMHOLE_KB_MIN_LINKS_PROCEDURE"); ok {
		if parsed, err := strconv.Atoi(val); err == nil {
			minLinksProcedure = parsed
		}
	}
	return Config{
		ListenAddr:          getEnv("WORMHOLE_LISTEN_ADDR", ":8080"),
		DatabaseURL:         getEnv("WORMHOLE_DATABASE_URL", "postgres://wormhole:wormhole@localhost:5432/wormhole?sslmode=prefer"),
		KBDedupThreshold:    threshold,
		KBMaxBodyLength:     maxBodyLength,
		KBMinLinksDecision:  minLinksDecision,
		KBMinLinksPolicy:    minLinksPolicy,
		KBMinLinksProcedure: minLinksProcedure,
	}
}

func getEnv(key, fallback string) string {
	if v, ok := os.LookupEnv(key); ok {
		return v
	}
	return fallback
}
