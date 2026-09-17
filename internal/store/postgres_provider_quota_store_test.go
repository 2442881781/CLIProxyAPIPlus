package store

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"errors"
	"fmt"
	"io"
	"reflect"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	cliproxyauth "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/auth"
)

var providerQuotaTestDriverID atomic.Uint64

type providerQuotaTestState struct {
	mu   sync.Mutex
	rows map[string][]byte
}

type providerQuotaTestDriver struct{ state *providerQuotaTestState }
type providerQuotaTestConn struct{ state *providerQuotaTestState }
type providerQuotaTestRows struct {
	rows  [][]byte
	index int
}
type providerQuotaTestTx struct{}

func (d *providerQuotaTestDriver) Open(string) (driver.Conn, error) {
	return &providerQuotaTestConn{state: d.state}, nil
}
func (c *providerQuotaTestConn) Prepare(string) (driver.Stmt, error) {
	return nil, errors.New("prepare is not supported")
}
func (c *providerQuotaTestConn) Close() error              { return nil }
func (c *providerQuotaTestConn) Begin() (driver.Tx, error) { return &providerQuotaTestTx{}, nil }
func (*providerQuotaTestTx) Commit() error                 { return nil }
func (*providerQuotaTestTx) Rollback() error               { return nil }

func (c *providerQuotaTestConn) ExecContext(_ context.Context, query string, args []driver.NamedValue) (driver.Result, error) {
	c.state.mu.Lock()
	defer c.state.mu.Unlock()
	if !strings.Contains(query, "INSERT INTO") || len(args) != 3 {
		return driver.RowsAffected(1), nil
	}
	provider, okProvider := args[0].Value.(string)
	content, okContent := args[1].Value.([]byte)
	if !okProvider || !okContent {
		return nil, errors.New("invalid provider quota query arguments")
	}
	c.state.rows[provider] = append([]byte(nil), content...)
	return driver.RowsAffected(1), nil
}

func (c *providerQuotaTestConn) QueryContext(_ context.Context, query string, _ []driver.NamedValue) (driver.Rows, error) {
	c.state.mu.Lock()
	defer c.state.mu.Unlock()
	if !strings.Contains(query, "SELECT content") {
		return &providerQuotaTestRows{}, nil
	}
	rows := make([][]byte, 0, len(c.state.rows))
	for _, content := range c.state.rows {
		rows = append(rows, append([]byte(nil), content...))
	}
	return &providerQuotaTestRows{rows: rows}, nil
}

func (*providerQuotaTestRows) Columns() []string { return []string{"content"} }
func (*providerQuotaTestRows) Close() error      { return nil }
func (r *providerQuotaTestRows) Next(dest []driver.Value) error {
	if r.index >= len(r.rows) {
		return io.EOF
	}
	dest[0] = r.rows[r.index]
	r.index++
	return nil
}

func TestPostgresProviderQuotaStoreSaveLoad(t *testing.T) {
	state := &providerQuotaTestState{rows: make(map[string][]byte)}
	driverName := fmt.Sprintf("cliproxy_postgres_provider_quota_test_%d", providerQuotaTestDriverID.Add(1))
	sql.Register(driverName, &providerQuotaTestDriver{state: state})
	db, errOpen := sql.Open(driverName, "")
	if errOpen != nil {
		t.Fatalf("sql.Open(): %v", errOpen)
	}
	t.Cleanup(func() { _ = db.Close() })

	postgresStore := &PostgresStore{db: db, cfg: PostgresStoreConfig{
		ConfigTable: defaultConfigTable, AuthTable: defaultAuthTable,
		CooldownTable: defaultCooldownTable, AccessTable: defaultAccessTable,
		ProviderQuotaTable: defaultProviderQuotaTable,
	}}
	quotaStore := &postgresProviderQuotaStore{store: postgresStore}
	postgresStore.providerQuotaStore = quotaStore
	if errSchema := postgresStore.EnsureSchema(context.Background()); errSchema != nil {
		t.Fatalf("EnsureSchema(): %v", errSchema)
	}
	if postgresStore.ProviderQuotaStore() != quotaStore {
		t.Fatal("ProviderQuotaStore() did not return configured store")
	}

	now := time.Now().UTC().Truncate(time.Microsecond)
	want := cliproxyauth.ProviderQuotaSnapshot{
		Provider: "commandcode", RemainingPercent: 65, ObservedAt: now, LastAttemptAt: now,
		Sources: []cliproxyauth.ProviderQuotaSourceSnapshot{{ID: "source-hash", RemainingPercent: 65, ObservedAt: now}},
	}
	if errSave := quotaStore.SaveProviderQuota(context.Background(), want); errSave != nil {
		t.Fatalf("SaveProviderQuota(): %v", errSave)
	}
	got, errLoad := quotaStore.LoadProviderQuotas(context.Background())
	if errLoad != nil {
		t.Fatalf("LoadProviderQuotas(): %v", errLoad)
	}
	if len(got) != 1 || !reflect.DeepEqual(got[0], want) {
		t.Fatalf("LoadProviderQuotas() = %#v, want %#v", got, want)
	}
}
