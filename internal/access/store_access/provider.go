package storeaccess

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v7/internal/util"
	sdkaccess "github.com/router-for-me/CLIProxyAPI/v7/sdk/access"
)

// ProviderType identifies the store-backed access provider.
const ProviderType = "store-api-key"

// provider authenticates requests against the access-key store. Keys not
// found in the store are reported as not handled so other providers can try.
type provider struct {
	store *Store
}

// Register loads the shared store under authDir and registers the provider.
// When the store cannot be loaded the provider is left unregistered so the
// process does not silently authenticate nothing.
func Register(authDir string) (*Store, error) {
	resolved, err := util.ResolveAuthDir(authDir)
	if err != nil {
		sdkaccess.UnregisterProvider(ProviderType)
		return nil, err
	}
	store, err := Configure(resolved)
	if err != nil {
		sdkaccess.UnregisterProvider(ProviderType)
		return nil, err
	}
	// The provider self-registers via syncProviderLocked whenever the store is
	// non-empty, keeping "no keys configured" equivalent to provider absent.
	startUsageFeed(store)
	return store, nil
}

// Unregister removes the provider from the global registry.
func Unregister() {
	sdkaccess.UnregisterProvider(ProviderType)
}

func (p *provider) Identifier() string { return ProviderType }

func (p *provider) Authenticate(_ context.Context, r *http.Request) (*sdkaccess.Result, *sdkaccess.AuthError) {
	if p == nil || p.store == nil {
		return nil, sdkaccess.NewNotHandledError()
	}
	candidates := candidateKeys(r)
	if len(candidates) == 0 {
		return nil, sdkaccess.NewNotHandledError()
	}
	var entry *AccessKey
	presented := ""
	for _, candidate := range candidates {
		if candidate == "" {
			continue
		}
		if found := p.store.Lookup(candidate); found != nil {
			entry = found
			presented = candidate
			break
		}
	}
	if entry == nil {
		return nil, sdkaccess.NewNotHandledError()
	}
	if entry.Disabled {
		return nil, forbiddenError("API key is disabled")
	}
	if entry.Expired(time.Now()) {
		return nil, forbiddenError("API key is expired")
	}
	if entry.QuotaExceeded(time.Now()) {
		return nil, &sdkaccess.AuthError{
			Code:       sdkaccess.AuthErrorCode("quota_exceeded"),
			Message:    "API key quota exceeded",
			StatusCode: http.StatusTooManyRequests,
		}
	}
	model := requestModel(r)
	if model != "" && !entry.ModelAllowed(model) {
		return nil, forbiddenError("API key is not allowed to use model: " + model)
	}
	if grp := p.store.GroupFor(entry); grp != nil {
		if model != "" && !grp.ModelAllowed(model) {
			return nil, forbiddenError("API key group is not allowed to use model: " + model)
		}
	}
	return &sdkaccess.Result{
		Provider:  p.Identifier(),
		Principal: presented,
		Metadata: map[string]string{
			"key_id":   entry.ID,
			"key_name": entry.Name,
			"group":    entry.Group,
		},
	}, nil
}

// forbiddenError aborts authentication with a hard 403 instead of letting the
// request fall through to other providers as an invalid credential.
func forbiddenError(message string) *sdkaccess.AuthError {
	return &sdkaccess.AuthError{
		Code:       sdkaccess.AuthErrorCode("forbidden"),
		Message:    message,
		StatusCode: http.StatusForbidden,
	}
}

// candidateKeys mirrors the credential sources used by the config provider so
// store keys work from the same headers and query parameters.
func candidateKeys(r *http.Request) []string {
	if r == nil {
		return nil
	}
	out := make([]string, 0, 5)
	if value := extractBearerToken(r.Header.Get("Authorization")); value != "" {
		out = append(out, value)
	}
	if value := strings.TrimSpace(r.Header.Get("X-Goog-Api-Key")); value != "" {
		out = append(out, value)
	}
	if value := strings.TrimSpace(r.Header.Get("X-Api-Key")); value != "" {
		out = append(out, value)
	}
	if r.URL != nil {
		if value := strings.TrimSpace(r.URL.Query().Get("key")); value != "" {
			out = append(out, value)
		}
		if value := strings.TrimSpace(r.URL.Query().Get("auth_token")); value != "" {
			out = append(out, value)
		}
	}
	return out
}

// LookupRequest extracts credential candidates from the request (same sources
// as Authenticate) and returns the matching store entry, or nil when none of
// them resolve to an issued key.
func (s *Store) LookupRequest(r *http.Request) *AccessKey {
	if s == nil {
		return nil
	}
	for _, candidate := range candidateKeys(r) {
		if candidate == "" {
			continue
		}
		if entry := s.Lookup(candidate); entry != nil {
			return entry
		}
	}
	return nil
}

func extractBearerToken(header string) string {
	header = strings.TrimSpace(header)
	if header == "" {
		return ""
	}
	parts := strings.SplitN(header, " ", 2)
	if len(parts) != 2 {
		return header
	}
	if !strings.EqualFold(strings.TrimSpace(parts[0]), "bearer") {
		return header
	}
	return strings.TrimSpace(parts[1])
}

// requestModel peeks at the JSON body for the top-level model field, restoring
// the body for downstream handlers. Returns "" when no model can be found.
func requestModel(r *http.Request) string {
	if r == nil || r.Body == nil || r.Method != http.MethodPost {
		return ""
	}
	body, err := io.ReadAll(io.LimitReader(r.Body, 16<<20))
	r.Body = io.NopCloser(bytes.NewReader(body))
	if err != nil || len(body) == 0 {
		return ""
	}
	var payload struct {
		Model json.RawMessage `json:"model"`
	}
	if errUnmarshal := json.Unmarshal(body, &payload); errUnmarshal != nil || len(payload.Model) == 0 {
		return ""
	}
	var model string
	if errUnmarshal := json.Unmarshal(payload.Model, &model); errUnmarshal != nil {
		return ""
	}
	return strings.TrimSpace(model)
}
