package canonical

import (
	"context"
	"errors"

	"github.com/jackc/pgx/v5"
)

func (s *Service) StakeholderRegistryStale(ctx context.Context, projectID int64, assignmentsDigest string) (bool, error) {
	var confirmed *string
	var registryDigest string
	err := s.pool.QueryRow(ctx, `
		SELECT confirmed_digest, registry_digest FROM blink_stakeholder_registry WHERE project_id=$1
	`, projectID).Scan(&confirmed, &registryDigest)
	if errors.Is(err, pgx.ErrNoRows) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	if confirmed == nil || *confirmed == "" {
		return false, nil
	}
	if assignmentsDigest == "" {
		return false, nil
	}
	return registryDigest != "" && registryDigest != assignmentsDigest, nil
}
