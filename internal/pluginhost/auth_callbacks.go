package pluginhost

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strconv"
	"strings"
	"time"

	coreauth "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/auth"
	"github.com/router-for-me/CLIProxyAPI/v7/sdk/pluginapi"
)

type rpcHostAuthGetRequest struct {
	AuthIndex string `json:"auth_index"`
}

type rpcHostAuthListResponse struct {
	Files []pluginapi.HostAuthFileEntry `json:"files"`
}

type rpcHostAuthGetResponse struct {
	AuthIndex string          `json:"auth_index"`
	Name      string          `json:"name,omitempty"`
	Path      string          `json:"path,omitempty"`
	JSON      json.RawMessage `json:"json"`
}

func (h *Host) SetAuthManager(manager *coreauth.Manager) {
	if h == nil {
		return
	}
	h.mu.Lock()
	h.authManager = manager
	h.mu.Unlock()
}

func (h *Host) currentAuthManager() *coreauth.Manager {
	if h == nil {
		return nil
	}
	h.mu.Lock()
	manager := h.authManager
	h.mu.Unlock()
	return manager
}

func (h *Host) callHostAuthList(ctx context.Context, request []byte) ([]byte, error) {
	_ = ctx
	if len(bytesTrimSpace(request)) > 0 {
		var req map[string]any
		if errUnmarshal := json.Unmarshal(request, &req); errUnmarshal != nil {
			return nil, fmt.Errorf("decode host auth list request: %w", errUnmarshal)
		}
	}
	entries, errList := h.listAuthFiles()
	if errList != nil {
		return nil, errList
	}
	return marshalRPCResult(rpcHostAuthListResponse{Files: entries})
}

func (h *Host) callHostAuthGet(ctx context.Context, request []byte) ([]byte, error) {
	_ = ctx
	var req rpcHostAuthGetRequest
	if errUnmarshal := json.Unmarshal(request, &req); errUnmarshal != nil {
		return nil, fmt.Errorf("decode host auth get request: %w", errUnmarshal)
	}
	authIndex := strings.TrimSpace(req.AuthIndex)
	if authIndex == "" {
		return nil, fmt.Errorf("auth_index is required")
	}
	auth, rawJSON, errGet := h.authPhysicalJSONByIndex(authIndex)
	if errGet != nil {
		return nil, errGet
	}
	name := strings.TrimSpace(auth.FileName)
	if name == "" {
		name = strings.TrimSpace(auth.ID)
	}
	path := strings.TrimSpace(authAttribute(auth, "path"))
	return marshalRPCResult(rpcHostAuthGetResponse{
		AuthIndex: authIndex,
		Name:      name,
		Path:      path,
		JSON:      json.RawMessage(rawJSON),
	})
}

func (h *Host) callHostAuthGetRuntime(ctx context.Context, request []byte) ([]byte, error) {
	_ = ctx
	var req rpcHostAuthGetRequest
	if errUnmarshal := json.Unmarshal(request, &req); errUnmarshal != nil {
		return nil, fmt.Errorf("decode host auth get runtime request: %w", errUnmarshal)
	}
	authIndex := strings.TrimSpace(req.AuthIndex)
	if authIndex == "" {
		return nil, fmt.Errorf("auth_index is required")
	}
	auth, errGet := h.authByIndex(authIndex)
	if errGet != nil {
		return nil, errGet
	}
	entry := h.buildHostAuthFileEntry(auth)
	if entry == nil {
		return nil, fmt.Errorf("auth runtime info not found for auth_index %s", authIndex)
	}
	return marshalRPCResult(pluginapi.HostAuthGetRuntimeResponse{Auth: *entry})
}

func (h *Host) callHostAuthSave(ctx context.Context, request []byte) ([]byte, error) {
	var req pluginapi.HostAuthSaveRequest
	if errUnmarshal := json.Unmarshal(request, &req); errUnmarshal != nil {
		return nil, fmt.Errorf("decode host auth save request: %w", errUnmarshal)
	}
	name, rawJSON, errValidate := validateHostAuthSaveRequest(req)
	if errValidate != nil {
		return nil, errValidate
	}
	rawJSON, errOwner := h.prepareHostAuthSave(ctx, name, rawJSON)
	if errOwner != nil {
		return nil, errOwner
	}
	path, errSave := h.saveAuthFile(ctx, name, rawJSON)
	if errSave != nil {
		return nil, errSave
	}
	return marshalRPCResult(pluginapi.HostAuthSaveResponse{
		Name: name,
		Path: path,
	})
}

func (h *Host) callHostAuthDelete(ctx context.Context, request []byte) ([]byte, error) {
	var req pluginapi.HostAuthDeleteRequest
	if errUnmarshal := json.Unmarshal(request, &req); errUnmarshal != nil {
		return nil, fmt.Errorf("decode host auth delete request: %w", errUnmarshal)
	}
	name := strings.TrimSpace(req.Name)
	if isUnsafeAuthFileName(name) || !strings.HasSuffix(strings.ToLower(name), ".json") {
		return nil, fmt.Errorf("invalid auth file name")
	}
	authDir := h.resolvedAuthDir()
	if authDir == "" {
		return nil, fmt.Errorf("auth directory is unavailable")
	}
	path := filepath.Join(authDir, filepath.Base(name))
	if errOwner := validateHostAuthDeleteOwner(ctx, path); errOwner != nil {
		return nil, errOwner
	}
	if errRemove := os.Remove(path); errRemove != nil && !os.IsNotExist(errRemove) {
		return nil, fmt.Errorf("failed to delete auth file: %w", errRemove)
	}
	if manager := h.currentAuthManager(); manager != nil {
		cleanPath := filepath.Clean(path)
		for _, auth := range manager.List() {
			if auth == nil {
				continue
			}
			authPath := strings.TrimSpace(authAttribute(auth, coreauth.AttributePath))
			if authPath != "" && filepath.Clean(authPath) == cleanPath {
				manager.Remove(ctx, auth.ID)
			}
		}
	}
	return marshalRPCResult(pluginapi.HostAuthDeleteResponse{Name: filepath.Base(name), Path: path})
}

func (h *Host) listAuthFiles() ([]pluginapi.HostAuthFileEntry, error) {
	manager := h.currentAuthManager()
	if manager != nil {
		auths := manager.List()
		entries := make([]pluginapi.HostAuthFileEntry, 0, len(auths))
		for _, auth := range auths {
			if entry := h.buildHostAuthFileEntry(auth); entry != nil {
				entries = append(entries, *entry)
			}
		}
		sort.Slice(entries, func(i, j int) bool {
			return strings.ToLower(entries[i].Name) < strings.ToLower(entries[j].Name)
		})
		return entries, nil
	}
	return h.listAuthFilesFromDisk()
}

func (h *Host) listAuthFilesFromDisk() ([]pluginapi.HostAuthFileEntry, error) {
	authDir := h.resolvedAuthDir()
	if authDir == "" {
		return nil, fmt.Errorf("auth directory is unavailable")
	}
	entries, errReadDir := os.ReadDir(authDir)
	if errReadDir != nil {
		return nil, fmt.Errorf("failed to read auth dir: %w", errReadDir)
	}
	files := make([]pluginapi.HostAuthFileEntry, 0)
	for _, entry := range entries {
		if entry.IsDir() {
			continue
		}
		name := entry.Name()
		if !strings.HasSuffix(strings.ToLower(name), ".json") {
			continue
		}
		full := filepath.Join(authDir, name)
		fileEntry := pluginapi.HostAuthFileEntry{
			Name:   name,
			Source: "file",
			Path:   full,
		}
		if info, errInfo := entry.Info(); errInfo == nil {
			fileEntry.Size = info.Size()
			fileEntry.ModTime = info.ModTime()
		}
		if data, errRead := os.ReadFile(full); errRead == nil {
			var metadata map[string]any
			if errUnmarshal := json.Unmarshal(data, &metadata); errUnmarshal == nil {
				if provider, ok := metadata["type"].(string); ok {
					fileEntry.Type = strings.TrimSpace(provider)
					fileEntry.Provider = fileEntry.Type
				}
				if email, ok := metadata["email"].(string); ok {
					fileEntry.Email = strings.TrimSpace(email)
				}
				if projectID, ok := metadata["project_id"].(string); ok {
					fileEntry.ProjectID = strings.TrimSpace(projectID)
				}
				if rawPriority, ok := metadata["priority"]; ok {
					if priority, okPriority := parsePriorityValue(rawPriority); okPriority {
						fileEntry.Priority = priority
					}
				}
				if note, ok := metadata["note"].(string); ok {
					fileEntry.Note = strings.TrimSpace(note)
				}
				if baseURL, ok := metadata["base_url"].(string); ok {
					fileEntry.BaseURL = strings.TrimSpace(baseURL)
				}
				if websockets, okWebsockets := parseWebsocketsValue(metadata["websockets"]); okWebsockets {
					fileEntry.Websockets = websockets
				}
			}
		}
		files = append(files, fileEntry)
	}
	sort.Slice(files, func(i, j int) bool {
		return strings.ToLower(files[i].Name) < strings.ToLower(files[j].Name)
	})
	return files, nil
}

func (h *Host) authByIndex(authIndex string) (*coreauth.Auth, error) {
	authIndex = strings.TrimSpace(authIndex)
	if authIndex == "" {
		return nil, fmt.Errorf("auth_index is required")
	}
	manager := h.currentAuthManager()
	if manager == nil {
		return nil, fmt.Errorf("core auth manager unavailable")
	}
	for _, auth := range manager.List() {
		if auth == nil {
			continue
		}
		auth.EnsureIndex()
		if auth.Index == authIndex {
			return auth, nil
		}
	}
	return nil, fmt.Errorf("auth not found for auth_index %s", authIndex)
}

func (h *Host) authPhysicalJSONByIndex(authIndex string) (*coreauth.Auth, []byte, error) {
	auth, errGet := h.authByIndex(authIndex)
	if errGet != nil {
		return nil, nil, errGet
	}
	path := strings.TrimSpace(authAttribute(auth, "path"))
	if path == "" {
		return nil, nil, fmt.Errorf("auth file path not found for auth_index %s", authIndex)
	}
	data, errRead := os.ReadFile(path)
	if errRead != nil {
		if os.IsNotExist(errRead) {
			return nil, nil, fmt.Errorf("auth file not found for auth_index %s", authIndex)
		}
		return nil, nil, fmt.Errorf("failed to read auth file: %w", errRead)
	}
	if len(bytesTrimSpace(data)) == 0 {
		return nil, nil, fmt.Errorf("auth file is empty for auth_index %s", authIndex)
	}
	var metadata map[string]any
	if errUnmarshal := json.Unmarshal(data, &metadata); errUnmarshal != nil {
		return nil, nil, fmt.Errorf("invalid auth file for auth_index %s: %w", authIndex, errUnmarshal)
	}
	return auth, data, nil
}

func validateHostAuthSaveRequest(req pluginapi.HostAuthSaveRequest) (string, []byte, error) {
	name := strings.TrimSpace(req.Name)
	if isUnsafeAuthFileName(name) {
		return "", nil, fmt.Errorf("invalid auth file name")
	}
	if !strings.HasSuffix(strings.ToLower(name), ".json") {
		return "", nil, fmt.Errorf("auth file name must end with .json")
	}
	rawJSON := bytesTrimSpace(req.JSON)
	if len(rawJSON) == 0 {
		return "", nil, fmt.Errorf("json is required")
	}
	var metadata map[string]any
	if errUnmarshal := json.Unmarshal(rawJSON, &metadata); errUnmarshal != nil {
		return "", nil, fmt.Errorf("invalid auth json: %w", errUnmarshal)
	}
	return filepath.Base(name), rawJSON, nil
}

const hostAuthPluginOwnerField = "_plugin_owner"

func (h *Host) prepareHostAuthSave(ctx context.Context, name string, rawJSON []byte) ([]byte, error) {
	pluginID := hostCallbackPluginIDFromContext(ctx)
	if pluginID == "" {
		return rawJSON, nil
	}
	authDir := h.resolvedAuthDir()
	if authDir == "" {
		return nil, fmt.Errorf("auth directory is unavailable")
	}
	path := filepath.Join(authDir, filepath.Base(name))
	if existing, errRead := os.ReadFile(path); errRead == nil {
		owner, errOwner := hostAuthPluginOwner(existing)
		if errOwner != nil {
			return nil, fmt.Errorf("auth file is not owned by calling plugin")
		}
		if owner == "" {
			if !hostAuthFileMatchesPluginPrefix(pluginID, filepath.Base(name)) {
				return nil, fmt.Errorf("auth file is not owned by calling plugin")
			}
		} else if owner != pluginID {
			return nil, fmt.Errorf("auth file is not owned by calling plugin")
		}
	} else if !os.IsNotExist(errRead) {
		return nil, fmt.Errorf("failed to inspect auth file: %w", errRead)
	}
	var metadata map[string]any
	if errUnmarshal := json.Unmarshal(rawJSON, &metadata); errUnmarshal != nil {
		return nil, fmt.Errorf("invalid auth json: %w", errUnmarshal)
	}
	if owner, _ := metadata[hostAuthPluginOwnerField].(string); strings.TrimSpace(owner) != "" && strings.TrimSpace(owner) != pluginID {
		return nil, fmt.Errorf("auth file owner does not match calling plugin")
	}
	metadata[hostAuthPluginOwnerField] = pluginID
	ownedJSON, errMarshal := json.Marshal(metadata)
	if errMarshal != nil {
		return nil, fmt.Errorf("failed to encode auth json: %w", errMarshal)
	}
	return ownedJSON, nil
}

func validateHostAuthDeleteOwner(ctx context.Context, path string) error {
	pluginID := hostCallbackPluginIDFromContext(ctx)
	if pluginID == "" {
		return fmt.Errorf("plugin identity is required")
	}
	data, errRead := os.ReadFile(path)
	if os.IsNotExist(errRead) {
		return nil
	}
	if errRead != nil {
		return fmt.Errorf("failed to inspect auth file: %w", errRead)
	}
	owner, errOwner := hostAuthPluginOwner(data)
	if errOwner != nil {
		return fmt.Errorf("auth file is not owned by calling plugin")
	}
	if owner == "" {
		if !hostAuthFileMatchesPluginPrefix(pluginID, filepath.Base(path)) {
			return fmt.Errorf("auth file is not owned by calling plugin")
		}
		return nil
	}
	if owner != pluginID {
		return fmt.Errorf("auth file is not owned by calling plugin")
	}
	return nil
}

func hostAuthFileMatchesPluginPrefix(pluginID, name string) bool {
	pluginID = strings.TrimSpace(pluginID)
	name = strings.TrimSpace(name)
	return pluginID != "" && strings.HasPrefix(name, pluginID+"-key-") && strings.HasSuffix(strings.ToLower(name), ".json")
}

func hostAuthPluginOwner(rawJSON []byte) (string, error) {
	var metadata map[string]any
	if errUnmarshal := json.Unmarshal(rawJSON, &metadata); errUnmarshal != nil {
		return "", errUnmarshal
	}
	owner, _ := metadata[hostAuthPluginOwnerField].(string)
	return strings.TrimSpace(owner), nil
}

func (h *Host) saveAuthFile(ctx context.Context, name string, data []byte) (string, error) {
	authDir := h.resolvedAuthDir()
	if authDir == "" {
		return "", fmt.Errorf("auth directory is unavailable")
	}
	dst := filepath.Join(authDir, filepath.Base(name))
	if !filepath.IsAbs(dst) {
		if abs, errAbs := filepath.Abs(dst); errAbs == nil {
			dst = abs
		}
	}
	pluginID := hostCallbackPluginIDFromContext(ctx)
	pluginActive := pluginID == "" || h.hasActiveAuthProviderPlugin(pluginID)
	var auth *coreauth.Auth
	if pluginActive {
		var errBuild error
		auth, errBuild = h.buildAuthFromFileData(ctx, dst, data)
		if errBuild != nil {
			return "", errBuild
		}
	}
	if errWrite := os.WriteFile(dst, data, 0o600); errWrite != nil {
		return "", fmt.Errorf("failed to write auth file: %w", errWrite)
	}
	if !pluginActive {
		return dst, nil
	}
	if errUpsert := h.upsertAuthRecord(ctx, auth); errUpsert != nil {
		return "", errUpsert
	}
	return dst, nil
}

func (h *Host) hasActiveAuthProviderPlugin(pluginID string) bool {
	pluginID = strings.TrimSpace(pluginID)
	if h == nil || pluginID == "" {
		return false
	}
	for _, record := range h.activeRecords() {
		if record.id == pluginID && record.plugin.Capabilities.AuthProvider != nil && !h.isPluginFused(record.id) {
			return true
		}
	}
	return false
}

func (h *Host) buildAuthFromFileData(ctx context.Context, path string, data []byte) (*coreauth.Auth, error) {
	if strings.TrimSpace(path) == "" {
		return nil, fmt.Errorf("auth path is empty")
	}
	if data == nil {
		var errRead error
		data, errRead = os.ReadFile(path)
		if errRead != nil {
			return nil, fmt.Errorf("failed to read auth file: %w", errRead)
		}
	}
	metadata := make(map[string]any)
	if errUnmarshal := json.Unmarshal(data, &metadata); errUnmarshal != nil {
		return nil, fmt.Errorf("invalid auth file: %w", errUnmarshal)
	}
	coreauth.NormalizeCredentialMetadata(metadata)
	if h != nil {
		providerHint, _ := metadata["type"].(string)
		var auths []*coreauth.Auth
		var handled bool
		var errParse error
		if pluginID := hostCallbackPluginIDFromContext(ctx); pluginID != "" {
			for _, record := range h.activeRecords() {
				if record.id != pluginID || record.plugin.Capabilities.AuthProvider == nil {
					continue
				}
				auths, handled, errParse = h.callParseAuths(ctx, record, pluginapi.AuthParseRequest{
					Provider: strings.TrimSpace(providerHint), Path: path, FileName: filepath.Base(path), RawJSON: data,
				})
				break
			}
		} else {
			auths, handled, errParse = h.ParseAuths(ctx, pluginapi.AuthParseRequest{
				Provider: strings.TrimSpace(providerHint), Path: path, FileName: filepath.Base(path), RawJSON: data,
			})
		}
		if errParse != nil {
			return nil, errParse
		}
		if handled {
			if len(auths) != 1 || auths[0] == nil {
				return nil, fmt.Errorf("host auth save requires exactly one auth per file")
			}
			parsed := auths[0].Clone()
			parsed.FileName = filepath.Base(path)
			return parsed, nil
		}
	}
	provider, _ := metadata["type"].(string)
	if strings.TrimSpace(provider) == "" {
		provider = "unknown"
	}
	label := provider
	if email, ok := metadata["email"].(string); ok && strings.TrimSpace(email) != "" {
		label = strings.TrimSpace(email)
	}
	authID := h.authIDForPath(path)
	if authID == "" {
		authID = path
	}
	auth := &coreauth.Auth{
		ID:       authID,
		Provider: provider,
		FileName: filepath.Base(path),
		Label:    label,
		Status:   coreauth.StatusActive,
		Attributes: map[string]string{
			"path":   path,
			"source": path,
		},
		Metadata:  metadata,
		CreatedAt: time.Now().UTC(),
		UpdatedAt: time.Now().UTC(),
	}
	if manager := h.currentAuthManager(); manager != nil {
		if existing, ok := manager.GetByID(authID); ok {
			auth.CreatedAt = existing.CreatedAt
			auth.LastRefreshedAt = existing.LastRefreshedAt
			auth.NextRetryAfter = existing.NextRetryAfter
			auth.Runtime = existing.Runtime
		}
	}
	if errWeight := coreauth.ValidateAuthWeight(auth); errWeight != nil {
		return nil, fmt.Errorf("invalid auth weight: %w", errWeight)
	}
	coreauth.ApplyCustomHeadersFromMetadata(auth)
	return auth, nil
}

func (h *Host) upsertAuthRecord(ctx context.Context, auth *coreauth.Auth) error {
	manager := h.currentAuthManager()
	if manager == nil || auth == nil {
		return nil
	}
	if existing, ok := manager.GetByID(auth.ID); ok {
		auth.CreatedAt = existing.CreatedAt
		_, errUpdate := manager.Update(ctx, auth)
		return errUpdate
	}
	_, errRegister := manager.Register(ctx, auth)
	return errRegister
}

func isUnsafeAuthFileName(name string) bool {
	if strings.TrimSpace(name) == "" {
		return true
	}
	if strings.ContainsAny(name, "/\\") {
		return true
	}
	if filepath.VolumeName(name) != "" {
		return true
	}
	return false
}

func (h *Host) buildHostAuthFileEntry(auth *coreauth.Auth) *pluginapi.HostAuthFileEntry {
	if auth == nil {
		return nil
	}
	auth.EnsureIndex()
	runtimeOnly := isRuntimeOnlyAuth(auth)
	if runtimeOnly && (auth.Disabled || auth.Status == coreauth.StatusDisabled) {
		return nil
	}
	path := strings.TrimSpace(authAttribute(auth, "path"))
	if path == "" && !runtimeOnly {
		return nil
	}
	name := strings.TrimSpace(auth.FileName)
	if name == "" {
		name = auth.ID
	}
	entry := &pluginapi.HostAuthFileEntry{
		ID:             auth.ID,
		AuthIndex:      auth.Index,
		Name:           name,
		Type:           strings.TrimSpace(auth.Provider),
		Provider:       strings.TrimSpace(auth.Provider),
		Label:          auth.Label,
		Status:         string(auth.Status),
		StatusMessage:  auth.StatusMessage,
		Disabled:       auth.Disabled,
		Unavailable:    auth.Unavailable,
		RuntimeOnly:    runtimeOnly,
		Source:         "memory",
		Success:        auth.Success,
		Failed:         auth.Failed,
		RecentRequests: hostRecentRequests(auth),
	}
	if email := authEmail(auth); email != "" {
		entry.Email = email
	}
	if projectID := authProjectID(auth); projectID != "" {
		entry.ProjectID = projectID
	}
	if accountType, account := auth.AccountInfo(); accountType != "" || account != "" {
		entry.AccountType = accountType
		entry.Account = account
	}
	if !auth.CreatedAt.IsZero() {
		entry.CreatedAt = auth.CreatedAt
	}
	if !auth.UpdatedAt.IsZero() {
		entry.ModTime = auth.UpdatedAt
		entry.UpdatedAt = auth.UpdatedAt
	}
	if !auth.LastRefreshedAt.IsZero() {
		entry.LastRefresh = auth.LastRefreshedAt
	}
	if !auth.NextRetryAfter.IsZero() {
		entry.NextRetryAfter = auth.NextRetryAfter
	}
	if path != "" {
		entry.Path = path
		entry.Source = "file"
		if info, err := os.Stat(path); err == nil {
			entry.Size = info.Size()
			entry.ModTime = info.ModTime()
		} else if os.IsNotExist(err) {
			if !runtimeOnly && (auth.Disabled || auth.Status == coreauth.StatusDisabled || strings.EqualFold(strings.TrimSpace(auth.StatusMessage), "removed via management api")) {
				return nil
			}
			entry.Source = "memory"
		}
	}
	if p := strings.TrimSpace(authAttribute(auth, "priority")); p != "" {
		if parsed, err := strconv.Atoi(p); err == nil {
			entry.Priority = parsed
		}
	} else if auth.Metadata != nil {
		if rawPriority, ok := auth.Metadata["priority"]; ok {
			if priority, okPriority := parsePriorityValue(rawPriority); okPriority {
				entry.Priority = priority
			}
		}
	}
	if note := strings.TrimSpace(authAttribute(auth, "note")); note != "" {
		entry.Note = note
	} else if auth.Metadata != nil {
		if rawNote, ok := auth.Metadata["note"].(string); ok {
			entry.Note = strings.TrimSpace(rawNote)
		}
	}
	if baseURL := strings.TrimSpace(authAttribute(auth, "base_url")); baseURL != "" {
		entry.BaseURL = baseURL
	} else if auth.Metadata != nil {
		if rawBaseURL, ok := auth.Metadata["base_url"].(string); ok {
			entry.BaseURL = strings.TrimSpace(rawBaseURL)
		}
	}
	if websockets, ok := authWebsocketsValue(auth); ok {
		entry.Websockets = websockets
	}
	return entry
}

func (h *Host) resolvedAuthDir() string {
	if h == nil {
		return ""
	}
	h.mu.Lock()
	authDir := ""
	if h.runtimeConfig != nil {
		authDir = strings.TrimSpace(h.runtimeConfig.AuthDir)
	}
	h.mu.Unlock()
	if authDir == "" {
		return ""
	}
	authDir = filepath.Clean(authDir)
	if !filepath.IsAbs(authDir) {
		if abs, errAbs := filepath.Abs(authDir); errAbs == nil {
			authDir = abs
		}
	}
	return authDir
}

func (h *Host) authIDForPath(path string) string {
	path = strings.TrimSpace(path)
	if path == "" {
		return ""
	}
	path = filepath.Clean(path)
	if !filepath.IsAbs(path) {
		if abs, errAbs := filepath.Abs(path); errAbs == nil {
			path = abs
		}
	}
	id := path
	if authDir := h.resolvedAuthDir(); authDir != "" {
		if rel, errRel := filepath.Rel(authDir, path); errRel == nil && rel != "" {
			id = rel
		}
	}
	if runtime.GOOS == "windows" {
		id = strings.ToLower(id)
	}
	return id
}

func authEmail(auth *coreauth.Auth) string {
	if auth == nil {
		return ""
	}
	if auth.Metadata != nil {
		if v, ok := auth.Metadata["email"].(string); ok {
			return strings.TrimSpace(v)
		}
	}
	if auth.Attributes != nil {
		if v := strings.TrimSpace(auth.Attributes["email"]); v != "" {
			return v
		}
		if v := strings.TrimSpace(auth.Attributes["account_email"]); v != "" {
			return v
		}
	}
	return ""
}

func authProjectID(auth *coreauth.Auth) string {
	if auth == nil {
		return ""
	}
	if auth.Metadata != nil {
		if v, ok := auth.Metadata["project_id"].(string); ok {
			if projectID := strings.TrimSpace(v); projectID != "" {
				return projectID
			}
		}
	}
	if auth.Attributes != nil {
		if projectID := strings.TrimSpace(auth.Attributes["project_id"]); projectID != "" {
			return projectID
		}
	}
	return ""
}

func authAttribute(auth *coreauth.Auth, key string) string {
	if auth == nil || len(auth.Attributes) == 0 {
		return ""
	}
	return auth.Attributes[key]
}

func isRuntimeOnlyAuth(auth *coreauth.Auth) bool {
	if auth == nil || len(auth.Attributes) == 0 {
		return false
	}
	return strings.EqualFold(strings.TrimSpace(auth.Attributes["runtime_only"]), "true")
}

func authWebsocketsValue(auth *coreauth.Auth) (bool, bool) {
	if auth == nil {
		return false, false
	}
	if auth.Attributes != nil {
		if raw := strings.TrimSpace(auth.Attributes["websockets"]); raw != "" {
			parsed, errParse := strconv.ParseBool(raw)
			if errParse == nil {
				return parsed, true
			}
		}
	}
	if auth.Metadata == nil {
		return false, false
	}
	return parseWebsocketsValue(auth.Metadata["websockets"])
}

func parsePriorityValue(raw any) (int, bool) {
	switch v := raw.(type) {
	case int:
		return v, true
	case int32:
		return int(v), true
	case int64:
		return int(v), true
	case float64:
		return int(v), true
	case string:
		parsed, err := strconv.Atoi(strings.TrimSpace(v))
		if err == nil {
			return parsed, true
		}
	}
	return 0, false
}

func parseWebsocketsValue(raw any) (bool, bool) {
	switch v := raw.(type) {
	case bool:
		return v, true
	case string:
		parsed, errParse := strconv.ParseBool(strings.TrimSpace(v))
		if errParse == nil {
			return parsed, true
		}
	}
	return false, false
}

func bytesTrimSpace(raw []byte) []byte {
	return []byte(strings.TrimSpace(string(raw)))
}

func hostRecentRequests(auth *coreauth.Auth) []pluginapi.HostRecentRequestEntry {
	if auth == nil {
		return nil
	}
	snapshot := auth.RecentRequestsSnapshot(time.Now())
	if len(snapshot) == 0 {
		return nil
	}
	out := make([]pluginapi.HostRecentRequestEntry, 0, len(snapshot))
	for _, entry := range snapshot {
		out = append(out, pluginapi.HostRecentRequestEntry{
			Time:    entry.Time,
			Success: entry.Success,
			Failed:  entry.Failed,
		})
	}
	return out
}
