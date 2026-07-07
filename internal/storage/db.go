package storage

import (
	"database/sql"

	_ "github.com/lib/pq"

	"github.com/H4RL33/wormhole/internal/types"
)

// Open connects to Postgres using the configured DSN. Callers own the
// returned *sql.DB and are responsible for closing it.
func Open(cfg types.Config) (*sql.DB, error) {
	db, err := sql.Open("postgres", cfg.DatabaseURL)
	if err != nil {
		return nil, err
	}
	return db, nil
}
