package db

import (
	"context"
	"fmt"
	"os"
	"strings"

	"github.com/jackc/pgx/v5/pgxpool"
)

func Connect(ctx context.Context, databaseURL string) (*pgxpool.Pool, error) {
	dsn := normalizeDSN(databaseURL)
	pool, err := pgxpool.New(ctx, dsn)
	if err != nil {
		return nil, fmt.Errorf("connect postgres: %w", err)
	}
	if err := pool.Ping(ctx); err != nil {
		pool.Close()
		return nil, fmt.Errorf("ping postgres: %w", err)
	}
	return pool, nil
}

func Migrate(ctx context.Context, pool *pgxpool.Pool) error {
	files := [][]string{
		{"schema.sql", "src/main/resources/schema.sql", "./schema.sql", "../schema.sql"},
		{"migrations/002_auth_sessions.sql", "./migrations/002_auth_sessions.sql"},
		{"migrations/003_user_integrations.sql", "./migrations/003_user_integrations.sql"},
		{"migrations/004_chat.sql", "./migrations/004_chat.sql"},
		{"migrations/005_figma_design.sql", "./migrations/005_figma_design.sql"},
		{"migrations/006_canonical_foundation.sql", "./migrations/006_canonical_foundation.sql"},
		{"migrations/007_canonical_gates.sql", "./migrations/007_canonical_gates.sql"},
		{"migrations/008_grooming_requirements.sql", "./migrations/008_grooming_requirements.sql"},
		{"migrations/009_repos_graph_ship.sql", "./migrations/009_repos_graph_ship.sql"},
	}
	for _, candidates := range files {
		body, path, err := readFirst(candidates)
		if err != nil {
			return fmt.Errorf("read migration %s: %w", candidates[0], err)
		}
		if _, err := pool.Exec(ctx, string(body)); err != nil {
			return fmt.Errorf("exec %s: %w", path, err)
		}
	}
	return nil
}

func readFirst(paths []string) ([]byte, string, error) {
	var last error
	for _, p := range paths {
		body, err := os.ReadFile(p)
		if err == nil {
			return body, p, nil
		}
		last = err
	}
	return nil, "", last
}

func normalizeDSN(raw string) string {
	s := strings.TrimSpace(raw)
	s = strings.TrimPrefix(s, "jdbc:")
	if strings.HasPrefix(s, "postgresql://") {
		s = "postgres://" + strings.TrimPrefix(s, "postgresql://")
	}
	return s
}
