// Package storeaccess provides a file-backed access-key store and the
// corresponding access provider. Unlike the inline config api-keys list,
// entries here carry metadata (name, disabled, expiry, model allowlist) so
// issued keys can be managed as entities.
package storeaccess

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	sdkaccess "github.com/router-for-me/CLIProxyAPI/v7/sdk/access"
	log "github.com/sirupsen/logrus"
)

// StoreFileName is the file used to persist access keys under the auth dir.
// The non-.json suffix keeps it out of the credential file scan.
const StoreFileName = "access-keys.store"

// legacyStoreFileName is the original on-disk name; Configure migrates it.
const legacyStoreFileName = "access-keys.json"

// KeyPrefix is the prefix applied to generated keys.
const KeyPrefix = "sk-cpa-"

// AccessKey is a single issued key with management metadata.
type AccessKey struct {
	ID            string    `json:"id"`
	KeyHash       string    `json:"key_hash"`
	KeyPrefix     string    `json:"key_prefix"`
	Name          string    `json:"name,omitempty"`
	Notes         string    `json:"notes,omitempty"`
	Group         string    `json:"group,omitempty"`
	Disabled      bool      `json:"disabled,omitempty"`
	ExpiresAt     string    `json:"expires_at,omitempty"`
	AllowedModels []string  `json:"allowed_models,omitempty"`
	Quota         Quota     `json:"quota,omitempty"`
	RateLimit     RateLimit `json:"rate_limit,omitempty"`
	Usage         Usage     `json:"usage,omitempty"`
	CreatedAt     string    `json:"created_at"`
	UpdatedAt     string    `json:"updated_at"`
}

// Group scopes a set of keys to a pool of upstream credentials and shared
// limits. AllowedAuths lists upstream auth IDs/labels the group may consume;
// empty means all credentials. AllowedModels applies on top of each key's own
// allowlist. MaxConcurrency caps simultaneous in-flight requests and
// RateLimitRPM caps requests per minute; <= 0 means unlimited. PerKeyLimits
// supplies default per-member-key limits; each member key may override
// individual fields via its own RateLimit.
type Group struct {
	Name           string    `json:"name"`
	AllowedAuths   []string  `json:"allowed_auths,omitempty"`
	AllowedModels  []string  `json:"allowed_models,omitempty"`
	MaxConcurrency int       `json:"max_concurrency,omitempty"`
	RateLimitRPM   int       `json:"rate_limit_rpm,omitempty"`
	PerKeyLimits   RateLimit `json:"per_key_limits,omitempty"`
	Usage          Usage     `json:"usage,omitempty"`
	CreatedAt      string    `json:"created_at"`
	UpdatedAt      string    `json:"updated_at"`
}

// storeFile is the on-disk schema. Legacy files contained a bare key array;
// loadLocked upgrades them transparently.
type storeFile struct {
	Keys   []AccessKey `json:"keys"`
	Groups []Group     `json:"groups,omitempty"`
}

// Quota limits token consumption. TokenLimit <= 0 means unlimited. Period
// selects the reset window: "daily", "monthly", or empty for all-time.
type Quota struct {
	TokenLimit int64  `json:"token_limit,omitempty"`
	Period     string `json:"period,omitempty"`
}

// Usage accumulates counters for a key. PeriodKey scopes PeriodTokens to the
// current quota window (e.g. "2026-09" for monthly, "2026-09-14" for daily).
// DayKey/DayRequests implement the RPD admission counter so the daily request
// budget survives restarts.
type Usage struct {
	TotalTokens  int64  `json:"total_tokens,omitempty"`
	PeriodTokens int64  `json:"period_tokens,omitempty"`
	PeriodKey    string `json:"period_key,omitempty"`
	Requests     int64  `json:"requests,omitempty"`
	Failed       int64  `json:"failed,omitempty"`
	LastUsedAt   string `json:"last_used_at,omitempty"`
	DayKey       string `json:"day_key,omitempty"`
	DayRequests  int64  `json:"day_requests,omitempty"`
}

// Store is a file-backed, goroutine-safe collection of access keys.
type Store struct {
	mu        sync.RWMutex
	path      string
	keys      map[string]*AccessKey    // by id
	byHash    map[string]*AccessKey    // by sha256 hex
	groups    map[string]*Group        // by name
	inflight  map[string]int64         // in-flight requests per group (memory only)
	groupRate map[string]*tokenBucket  // shared rpm bucket per group (memory only)
	keyRate   map[string]*keyRateState // per-key buckets + in-flight (memory only)
	loaded    bool
	managed   bool // true only for the Configure()-installed shared store
	fileMod   time.Time
	lastStat  time.Time
	dirty     bool
	now       func() time.Time // injectable clock; nil means time.Now
}

var (
	defaultStoreMu sync.Mutex
	defaultStore   *Store

	providersRefresherMu sync.Mutex
	providersRefresher   func()
)

// SetProvidersRefresher installs a hook invoked whenever the store-backed auth
// provider's registration state changes, so a running access manager can
// refresh its provider snapshot.
func SetProvidersRefresher(f func()) {
	providersRefresherMu.Lock()
	providersRefresher = f
	providersRefresherMu.Unlock()
}

// Configure initializes the shared store rooted at the auth directory. Calling
// it again with the same directory is a no-op.
func Configure(authDir string) (*Store, error) {
	defaultStoreMu.Lock()
	defer defaultStoreMu.Unlock()
	dir := strings.TrimSpace(authDir)
	path := filepath.Join(dir, StoreFileName)
	if defaultStore != nil && defaultStore.path == path {
		return defaultStore, nil
	}
	if legacy := filepath.Join(dir, legacyStoreFileName); dir != "" {
		if _, errStat := os.Stat(path); os.IsNotExist(errStat) {
			if _, errLegacy := os.Stat(legacy); errLegacy == nil {
				if errRename := os.Rename(legacy, path); errRename == nil {
					log.Infof("access keys: migrated %s -> %s", legacy, path)
				}
			}
		}
	}
	store := &Store{path: path, managed: true}
	if err := store.loadLocked(); err != nil {
		return nil, err
	}
	store.syncProviderLocked()
	defaultStore = store
	return defaultStore, nil
}

// DefaultStore returns the configured shared store, or nil when Configure has
// not been called yet.
func DefaultStore() *Store {
	defaultStoreMu.Lock()
	defer defaultStoreMu.Unlock()
	return defaultStore
}

// Path returns the backing file path.
func (s *Store) Path() string {
	if s == nil {
		return ""
	}
	return s.path
}

// GenerateKey returns a new random key string with the KeyPrefix prefix.
func GenerateKey() (string, error) {
	buf := make([]byte, 24)
	if _, err := rand.Read(buf); err != nil {
		return "", fmt.Errorf("generate access key: %w", err)
	}
	return KeyPrefix + hex.EncodeToString(buf), nil
}

// HashKey returns the sha256 hex digest used to store a key.
func HashKey(key string) string {
	sum := sha256.Sum256([]byte(strings.TrimSpace(key)))
	return hex.EncodeToString(sum[:])
}

func newID() string {
	buf := make([]byte, 8)
	if _, err := rand.Read(buf); err != nil {
		return fmt.Sprintf("key-%d", time.Now().UnixNano())
	}
	return "key-" + hex.EncodeToString(buf)
}

func nowRFC3339() string { return time.Now().UTC().Format(time.RFC3339) }

func (s *Store) loadLocked() error {
	s.keys = make(map[string]*AccessKey)
	s.byHash = make(map[string]*AccessKey)
	s.groups = make(map[string]*Group)
	data, err := os.ReadFile(s.path)
	if err != nil {
		if os.IsNotExist(err) {
			s.loaded = true
			return nil
		}
		return fmt.Errorf("read access key store: %w", err)
	}
	var file storeFile
	if errUnmarshal := json.Unmarshal(data, &file); errUnmarshal != nil {
		// Legacy schema: the file was a bare []AccessKey array.
		var list []AccessKey
		if errLegacy := json.Unmarshal(data, &list); errLegacy != nil {
			return fmt.Errorf("parse access key store %s: %w", s.path, errUnmarshal)
		}
		file.Keys = list
	}
	for i := range file.Keys {
		entry := file.Keys[i]
		if entry.ID == "" || entry.KeyHash == "" {
			continue
		}
		copyEntry := entry
		s.keys[copyEntry.ID] = &copyEntry
		s.byHash[copyEntry.KeyHash] = &copyEntry
	}
	for i := range file.Groups {
		grp := file.Groups[i]
		if grp.Name == "" {
			continue
		}
		copyGrp := grp
		s.groups[copyGrp.Name] = &copyGrp
	}
	if info, err := os.Stat(s.path); err == nil {
		s.fileMod = info.ModTime()
	}
	s.loaded = true
	s.syncProviderLocked()
	return nil
}

// syncProviderLocked keeps the auth provider registered only while the store
// holds at least one key, mirroring the config provider's "empty means open"
// semantics. Callers must hold s.mu.
func (s *Store) syncProviderLocked() {
	if !s.managed {
		return
	}
	if len(s.keys) == 0 {
		sdkaccess.UnregisterProvider(ProviderType)
	} else {
		sdkaccess.RegisterProvider(ProviderType, &provider{store: s})
	}
	providersRefresherMu.Lock()
	refresh := providersRefresher
	providersRefresherMu.Unlock()
	if refresh != nil {
		refresh()
	}
}

// maybeReload reloads the file when its mtime changed, so manual edits to
// access-keys.json take effect without a restart. Stated at most once per
// second to avoid a syscall on every request.
func (s *Store) maybeReload() {
	s.mu.Lock()
	defer s.mu.Unlock()
	if time.Since(s.lastStat) < time.Second {
		return
	}
	s.lastStat = time.Now()
	info, err := os.Stat(s.path)
	if err != nil {
		return
	}
	if !info.ModTime().After(s.fileMod) {
		return
	}
	_ = s.loadLocked()
}

// persistLocked writes the store atomically (tmp file + rename).
func (s *Store) persistLocked() error {
	list := make([]AccessKey, 0, len(s.keys))
	for _, entry := range s.keys {
		list = append(list, *entry)
	}
	sort.Slice(list, func(i, j int) bool { return list[i].CreatedAt < list[j].CreatedAt })
	groups := make([]Group, 0, len(s.groups))
	for _, grp := range s.groups {
		groups = append(groups, *grp)
	}
	sort.Slice(groups, func(i, j int) bool { return groups[i].CreatedAt < groups[j].CreatedAt })
	data, err := json.MarshalIndent(storeFile{Keys: list, Groups: groups}, "", "  ")
	if err != nil {
		return fmt.Errorf("marshal access key store: %w", err)
	}
	if errDir := os.MkdirAll(filepath.Dir(s.path), 0o700); errDir != nil {
		return fmt.Errorf("create access key store dir: %w", errDir)
	}
	tmp := s.path + ".tmp"
	if errWrite := os.WriteFile(tmp, data, 0o600); errWrite != nil {
		return fmt.Errorf("write access key store: %w", errWrite)
	}
	if errRename := os.Rename(tmp, s.path); errRename != nil {
		_ = os.Remove(tmp)
		return fmt.Errorf("rename access key store: %w", errRename)
	}
	if info, err := os.Stat(s.path); err == nil {
		s.fileMod = info.ModTime()
	}
	s.syncProviderLocked()
	return nil
}

// Lookup returns the key entry matching the presented plaintext key.
func (s *Store) Lookup(key string) *AccessKey {
	if s == nil || !s.loaded {
		return nil
	}
	hash := HashKey(key)
	s.maybeReload()
	s.mu.RLock()
	entry, ok := s.byHash[hash]
	s.mu.RUnlock()
	if !ok {
		return nil
	}
	copyEntry := *entry
	return &copyEntry
}

// List returns all entries sorted by creation time.
func (s *Store) List() []AccessKey {
	if s == nil {
		return nil
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	list := make([]AccessKey, 0, len(s.keys))
	for _, entry := range s.keys {
		list = append(list, *entry)
	}
	sort.Slice(list, func(i, j int) bool { return list[i].CreatedAt < list[j].CreatedAt })
	return list
}

// Get returns an entry by id.
func (s *Store) Get(id string) *AccessKey {
	if s == nil {
		return nil
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	if entry, ok := s.keys[id]; ok {
		copyEntry := *entry
		return &copyEntry
	}
	return nil
}

// Create stores a new key built from the plaintext key and metadata fields.
func (s *Store) Create(plaintext string, fields AccessKey) (*AccessKey, error) {
	if s == nil || !s.loaded {
		return nil, fmt.Errorf("access key store not initialized")
	}
	plaintext = strings.TrimSpace(plaintext)
	if plaintext == "" {
		return nil, fmt.Errorf("key must not be empty")
	}
	hash := HashKey(plaintext)
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, exists := s.byHash[hash]; exists {
		return nil, fmt.Errorf("key already exists")
	}
	now := nowRFC3339()
	entry := &AccessKey{
		ID:            newID(),
		KeyHash:       hash,
		KeyPrefix:     displayPrefix(plaintext),
		Name:          strings.TrimSpace(fields.Name),
		Notes:         fields.Notes,
		Group:         strings.TrimSpace(fields.Group),
		Disabled:      fields.Disabled,
		ExpiresAt:     strings.TrimSpace(fields.ExpiresAt),
		AllowedModels: normalizeModels(fields.AllowedModels),
		Quota: Quota{
			TokenLimit: fields.Quota.TokenLimit,
			Period:     strings.ToLower(strings.TrimSpace(fields.Quota.Period)),
		},
		RateLimit: fields.RateLimit,
		CreatedAt: now,
		UpdatedAt: now,
	}
	s.keys[entry.ID] = entry
	s.byHash[hash] = entry
	if err := s.persistLocked(); err != nil {
		delete(s.keys, entry.ID)
		delete(s.byHash, hash)
		return nil, err
	}
	copyEntry := *entry
	return &copyEntry, nil
}

// Update applies non-empty fields to an existing entry. Key material cannot be
// changed here; use Rotate for that.
func (s *Store) Update(id string, patch AccessKeyPatch) (*AccessKey, error) {
	if s == nil || !s.loaded {
		return nil, fmt.Errorf("access key store not initialized")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	entry, ok := s.keys[id]
	if !ok {
		return nil, fmt.Errorf("key not found")
	}
	if patch.Name != nil {
		entry.Name = strings.TrimSpace(*patch.Name)
	}
	if patch.Notes != nil {
		entry.Notes = *patch.Notes
	}
	if patch.Group != nil {
		entry.Group = strings.TrimSpace(*patch.Group)
	}
	if patch.Disabled != nil {
		entry.Disabled = *patch.Disabled
	}
	if patch.ExpiresAt != nil {
		entry.ExpiresAt = strings.TrimSpace(*patch.ExpiresAt)
	}
	if patch.AllowedModels != nil {
		entry.AllowedModels = normalizeModels(*patch.AllowedModels)
	}
	if patch.Quota != nil {
		entry.Quota = *patch.Quota
		entry.Quota.Period = strings.ToLower(strings.TrimSpace(entry.Quota.Period))
	}
	if patch.RateLimit != nil {
		entry.RateLimit = *patch.RateLimit
	}
	if patch.ResetUsage {
		entry.Usage = Usage{}
	}
	entry.UpdatedAt = nowRFC3339()
	if err := s.persistLocked(); err != nil {
		return nil, err
	}
	copyEntry := *entry
	return &copyEntry, nil
}

// Rotate replaces the key material of an entry and returns the updated entry
// together with the new plaintext key (generated when newPlaintext is empty).
func (s *Store) Rotate(id string, newPlaintext string) (*AccessKey, string, error) {
	if s == nil || !s.loaded {
		return nil, "", fmt.Errorf("access key store not initialized")
	}
	newPlaintext = strings.TrimSpace(newPlaintext)
	if newPlaintext == "" {
		var err error
		newPlaintext, err = GenerateKey()
		if err != nil {
			return nil, "", err
		}
	}
	newHash := HashKey(newPlaintext)
	s.mu.Lock()
	defer s.mu.Unlock()
	entry, ok := s.keys[id]
	if !ok {
		return nil, "", fmt.Errorf("key not found")
	}
	if _, exists := s.byHash[newHash]; exists {
		return nil, "", fmt.Errorf("key already exists")
	}
	delete(s.byHash, entry.KeyHash)
	entry.KeyHash = newHash
	entry.KeyPrefix = displayPrefix(newPlaintext)
	entry.UpdatedAt = nowRFC3339()
	s.byHash[newHash] = entry
	if err := s.persistLocked(); err != nil {
		return nil, "", err
	}
	copyEntry := *entry
	return &copyEntry, newPlaintext, nil
}

// Delete removes an entry by id.
func (s *Store) Delete(id string) error {
	if s == nil || !s.loaded {
		return fmt.Errorf("access key store not initialized")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	entry, ok := s.keys[id]
	if !ok {
		return fmt.Errorf("key not found")
	}
	delete(s.keys, id)
	delete(s.byHash, entry.KeyHash)
	if err := s.persistLocked(); err != nil {
		return err
	}
	return nil
}

// AccessKeyPatch carries optional updates for Store.Update.
type AccessKeyPatch struct {
	Name          *string    `json:"name"`
	Notes         *string    `json:"notes"`
	Group         *string    `json:"group"`
	Disabled      *bool      `json:"disabled"`
	ExpiresAt     *string    `json:"expires_at"`
	AllowedModels *[]string  `json:"allowed_models"`
	Quota         *Quota     `json:"quota"`
	RateLimit     *RateLimit `json:"rate_limit"`
	ResetUsage    bool       `json:"reset_usage"`
}

// displayPrefix keeps enough of the key to identify it without exposing it.
func displayPrefix(key string) string {
	key = strings.TrimSpace(key)
	if len(key) <= 16 {
		return key
	}
	return key[:16]
}

func normalizeModels(models []string) []string {
	if len(models) == 0 {
		return nil
	}
	out := make([]string, 0, len(models))
	seen := make(map[string]struct{}, len(models))
	for _, model := range models {
		model = strings.TrimSpace(model)
		if model == "" {
			continue
		}
		if _, ok := seen[model]; ok {
			continue
		}
		seen[model] = struct{}{}
		out = append(out, model)
	}
	return out
}

// ModelAllowed reports whether the entry permits the given model. An empty
// allowlist permits all models. Entries support an exact match or a
// trailing-* prefix wildcard (e.g. "zhipu/*", "*").
func (k *AccessKey) ModelAllowed(model string) bool {
	if k == nil || len(k.AllowedModels) == 0 {
		return true
	}
	model = strings.TrimSpace(model)
	for _, allowed := range k.AllowedModels {
		if allowed == "*" || allowed == model {
			return true
		}
		if strings.HasSuffix(allowed, "*") && strings.HasPrefix(model, strings.TrimSuffix(allowed, "*")) {
			return true
		}
	}
	return false
}

// Expired reports whether the entry is past its ExpiresAt instant. An empty or
// unparsable ExpiresAt is treated as non-expiring.
func (k *AccessKey) Expired(now time.Time) bool {
	if k == nil || k.ExpiresAt == "" {
		return false
	}
	expiresAt, err := time.Parse(time.RFC3339, strings.TrimSpace(k.ExpiresAt))
	if err != nil {
		return false
	}
	return !now.Before(expiresAt)
}

// periodKeyFor returns the bucket label for the quota window containing now.
func (q Quota) periodKeyFor(now time.Time) string {
	switch strings.ToLower(strings.TrimSpace(q.Period)) {
	case "daily":
		return now.UTC().Format("2006-01-02")
	case "monthly":
		return now.UTC().Format("2006-01")
	default:
		return ""
	}
}

// QuotaExceeded reports whether the key has consumed its token limit in the
// current period. A missing/empty period means the limit applies all-time.
func (k *AccessKey) QuotaExceeded(now time.Time) bool {
	if k == nil || k.Quota.TokenLimit <= 0 {
		return false
	}
	key := k.Quota.periodKeyFor(now)
	if key == "" {
		return k.Usage.TotalTokens >= k.Quota.TokenLimit
	}
	if k.Usage.PeriodKey != key {
		return false
	}
	return k.Usage.PeriodTokens >= k.Quota.TokenLimit
}

// RecordUsage adds tokens for a presented plaintext key, rolling the period
// bucket when the window changed. Returns false when the key is unknown.
func (s *Store) RecordUsage(key string, tokens int64, failed bool) bool {
	if s == nil || !s.loaded {
		return false
	}
	hash := HashKey(key)
	s.mu.Lock()
	defer s.mu.Unlock()
	entry, ok := s.byHash[hash]
	if !ok {
		return false
	}
	now := s.nowTime().UTC()
	entry.Usage.TotalTokens += tokens
	entry.Usage.Requests++
	if failed {
		entry.Usage.Failed++
	}
	periodKey := entry.Quota.periodKeyFor(now)
	if entry.Usage.PeriodKey != periodKey {
		entry.Usage.PeriodKey = periodKey
		entry.Usage.PeriodTokens = 0
	}
	entry.Usage.PeriodTokens += tokens
	entry.Usage.LastUsedAt = now.Format(time.RFC3339)
	s.recordGroupUsageLocked(entry, tokens, failed, now)
	s.chargeKeyTPMLocked(entry, tokens, now)
	s.dirty = true
	return true
}

// Flush persists pending usage counters when the store is dirty.
func (s *Store) Flush() error {
	if s == nil || !s.loaded {
		return nil
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if !s.dirty {
		return nil
	}
	if err := s.persistLocked(); err != nil {
		return err
	}
	s.dirty = false
	return nil
}

// GroupFor returns the group attached to a key entry, or nil when the key has
// no group or the group no longer exists.
func (s *Store) GroupFor(entry *AccessKey) *Group {
	if s == nil || entry == nil || entry.Group == "" {
		return nil
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	if grp, ok := s.groups[entry.Group]; ok {
		copyGrp := *grp
		return &copyGrp
	}
	return nil
}

// ListGroups returns all groups sorted by creation time.
func (s *Store) ListGroups() []Group {
	if s == nil {
		return nil
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	out := make([]Group, 0, len(s.groups))
	for _, grp := range s.groups {
		out = append(out, *grp)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].CreatedAt < out[j].CreatedAt })
	return out
}

// GetGroup returns a group by name.
func (s *Store) GetGroup(name string) *Group {
	if s == nil {
		return nil
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	if grp, ok := s.groups[name]; ok {
		copyGrp := *grp
		return &copyGrp
	}
	return nil
}

// UpsertGroup creates or replaces a group definition.
func (s *Store) UpsertGroup(grp Group) (*Group, error) {
	if s == nil || !s.loaded {
		return nil, fmt.Errorf("access key store not initialized")
	}
	grp.Name = strings.TrimSpace(grp.Name)
	if grp.Name == "" {
		return nil, fmt.Errorf("group name must not be empty")
	}
	grp.AllowedAuths = normalizeModels(grp.AllowedAuths)
	grp.AllowedModels = normalizeModels(grp.AllowedModels)
	s.mu.Lock()
	defer s.mu.Unlock()
	existing, ok := s.groups[grp.Name]
	now := nowRFC3339()
	if ok {
		existing.AllowedAuths = grp.AllowedAuths
		existing.AllowedModels = grp.AllowedModels
		existing.MaxConcurrency = grp.MaxConcurrency
		existing.RateLimitRPM = grp.RateLimitRPM
		existing.PerKeyLimits = grp.PerKeyLimits
		existing.UpdatedAt = now
	} else {
		existing = &Group{Name: grp.Name, CreatedAt: now}
		existing.AllowedAuths = grp.AllowedAuths
		existing.AllowedModels = grp.AllowedModels
		existing.MaxConcurrency = grp.MaxConcurrency
		existing.RateLimitRPM = grp.RateLimitRPM
		existing.PerKeyLimits = grp.PerKeyLimits
		existing.UpdatedAt = now
		s.groups[grp.Name] = existing
	}
	if err := s.persistLocked(); err != nil {
		return nil, err
	}
	copyGrp := *existing
	return &copyGrp, nil
}

// DeleteGroup removes a group by name. Keys referencing it keep the stale
// name, which resolves to no group (unrestricted) on lookup.
func (s *Store) DeleteGroup(name string) error {
	if s == nil || !s.loaded {
		return fmt.Errorf("access key store not initialized")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, ok := s.groups[name]; !ok {
		return fmt.Errorf("group not found")
	}
	delete(s.groups, name)
	if err := s.persistLocked(); err != nil {
		return err
	}
	return nil
}

// AuthAllowed reports whether a group permits the given upstream auth. The
// match accepts the auth ID, label, or file name so admins can reference
// whichever identifier they see in the panel.
func (g *Group) AuthAllowed(authID, label, fileName string) bool {
	if g == nil || len(g.AllowedAuths) == 0 {
		return true
	}
	for _, allowed := range g.AllowedAuths {
		if allowed == authID || allowed == label || allowed == fileName {
			return true
		}
	}
	return false
}

// ModelAllowed reports whether the group permits the given model, same
// wildcard semantics as AccessKey.ModelAllowed.
func (g *Group) ModelAllowed(model string) bool {
	if g == nil || len(g.AllowedModels) == 0 {
		return true
	}
	model = strings.TrimSpace(model)
	for _, allowed := range g.AllowedModels {
		if allowed == "*" || allowed == model {
			return true
		}
		if strings.HasSuffix(allowed, "*") && strings.HasPrefix(model, strings.TrimSuffix(allowed, "*")) {
			return true
		}
	}
	return false
}

// AcquireGroup checks group rate/concurrency limits and, when allowed,
// registers one in-flight request. The returned release func decrements the
// in-flight counter and must be called when the request finishes; it is nil
// when the request was rejected or the group does not exist. retryAfter
// reports the seconds until admission is expected to succeed again.
func (s *Store) AcquireGroup(name string) (release func(), retryAfter float64, err error) {
	if s == nil || name == "" {
		return func() {}, 0, nil
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	grp, ok := s.groups[name]
	if !ok {
		return func() {}, 0, nil
	}
	if grp.MaxConcurrency > 0 {
		if s.inflight == nil {
			s.inflight = make(map[string]int64)
		}
		if s.inflight[name] >= int64(grp.MaxConcurrency) {
			return nil, 1, fmt.Errorf("group %q concurrency limit exceeded (%d)", name, grp.MaxConcurrency)
		}
	}
	if grp.RateLimitRPM > 0 {
		if s.groupRate == nil {
			s.groupRate = make(map[string]*tokenBucket)
		}
		bucket := s.groupRate[name]
		if bucket == nil {
			bucket = &tokenBucket{}
			s.groupRate[name] = bucket
		}
		if ok, ra := bucket.take(s.nowTime(), int64(grp.RateLimitRPM), time.Minute); !ok {
			return nil, ra, fmt.Errorf("group %q rate limit exceeded (%d rpm)", name, grp.RateLimitRPM)
		}
	}
	if grp.MaxConcurrency > 0 {
		s.inflight[name]++
		var once sync.Once
		return func() {
			once.Do(func() {
				s.mu.Lock()
				defer s.mu.Unlock()
				if s.inflight[name] > 0 {
					s.inflight[name]--
				}
			})
		}, 0, nil
	}
	return func() {}, 0, nil
}

// recordGroupUsageLocked aggregates usage into the key's group counters.
// Caller must hold s.mu.
func (s *Store) recordGroupUsageLocked(entry *AccessKey, tokens int64, failed bool, now time.Time) {
	if entry.Group == "" {
		return
	}
	grp, ok := s.groups[entry.Group]
	if !ok {
		return
	}
	grp.Usage.TotalTokens += tokens
	grp.Usage.Requests++
	if failed {
		grp.Usage.Failed++
	}
	grp.Usage.LastUsedAt = now.Format(time.RFC3339)
}
