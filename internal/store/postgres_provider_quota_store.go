package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	cliproxyauth "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/auth"
)

var _ cliproxyauth.ProviderQuotaStoreProvider = (*PostgresStore)(nil)
var _ cliproxyauth.ProviderQuotaStore = (*postgresProviderQuotaStore)(nil)

type postgresProviderQuotaStore struct {
	store *PostgresStore
}

// ProviderQuotaStore returns the PostgreSQL-backed provider quota store.
func (s *PostgresStore) ProviderQuotaStore() cliproxyauth.ProviderQuotaStore {
	if s == nil {
		return nil
	}
	return s.providerQuotaStore
}

func (s *postgresProviderQuotaStore) LoadProviderQuotas(ctx context.Context) ([]cliproxyauth.ProviderQuotaSnapshot, error) {
	if s == nil || s.store == nil || s.store.db == nil {
		return nil, fmt.Errorf("postgres provider quota store: not initialized")
	}
	if ctx == nil {
		ctx = context.Background()
	}
	query := fmt.Sprintf("SELECT content FROM %s ORDER BY provider", s.store.fullTableName(s.store.cfg.ProviderQuotaTable))
	rows, errQuery := s.store.db.QueryContext(ctx, query)
	if errQuery != nil {
		return nil, fmt.Errorf("postgres provider quota store: load snapshots: %w", errQuery)
	}
	defer rows.Close()

	out := make([]cliproxyauth.ProviderQuotaSnapshot, 0)
	for rows.Next() {
		var content []byte
		if errScan := rows.Scan(&content); errScan != nil {
			return nil, fmt.Errorf("postgres provider quota store: scan snapshot: %w", errScan)
		}
		var snapshot cliproxyauth.ProviderQuotaSnapshot
		if errDecode := json.Unmarshal(content, &snapshot); errDecode != nil {
			return nil, fmt.Errorf("postgres provider quota store: decode snapshot: %w", errDecode)
		}
		out = append(out, snapshot)
	}
	if errRows := rows.Err(); errRows != nil {
		return nil, fmt.Errorf("postgres provider quota store: iterate snapshots: %w", errRows)
	}
	return out, nil
}

func (s *postgresProviderQuotaStore) SaveProviderQuota(ctx context.Context, snapshot cliproxyauth.ProviderQuotaSnapshot) error {
	if s == nil || s.store == nil || s.store.db == nil {
		return fmt.Errorf("postgres provider quota store: not initialized")
	}
	provider := strings.ToLower(strings.TrimSpace(snapshot.Provider))
	if provider == "" {
		return fmt.Errorf("postgres provider quota store: provider is empty")
	}
	if ctx == nil {
		ctx = context.Background()
	}
	content, errEncode := json.Marshal(snapshot)
	if errEncode != nil {
		return fmt.Errorf("postgres provider quota store: encode snapshot: %w", errEncode)
	}
	version := snapshot.LastAttemptAt
	if version.IsZero() {
		version = snapshot.ObservedAt
	}
	if version.IsZero() {
		version = time.Now()
	}
	version = version.UTC().Truncate(time.Microsecond)
	query := fmt.Sprintf(`
		INSERT INTO %s AS target (provider, content, created_at, updated_at)
		VALUES ($1, $2, NOW(), $3)
		ON CONFLICT (provider) DO UPDATE SET
			content = EXCLUDED.content,
			updated_at = EXCLUDED.updated_at
		WHERE target.updated_at <= EXCLUDED.updated_at
	`, s.store.fullTableName(s.store.cfg.ProviderQuotaTable))
	if _, errExec := s.store.db.ExecContext(ctx, query, provider, content, version); errExec != nil {
		if errors.Is(errExec, sql.ErrNoRows) {
			return nil
		}
		return fmt.Errorf("postgres provider quota store: save snapshot: %w", errExec)
	}
	return nil
}
