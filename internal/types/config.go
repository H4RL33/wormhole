package types

import "os"

type Config struct {
	ListenAddr string
	DatabaseURL string
}

func LoadConfig() Config {
	return Config{
		ListenAddr:  getEnv("WORMHOLE_LISTEN_ADDR", ":8080"),
		DatabaseURL: getEnv("WORMHOLE_DATABASE_URL", "postgres://wormhole:wormhole@localhost:5432/wormhole?sslmode=disable"),
	}
}

func getEnv(key, fallback string) string {
	if v, ok := os.LookupEnv(key); ok {
		return v
	}
	return fallback
}
