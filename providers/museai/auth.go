package main

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"cpa-plugin-museai/internal/browser"
	"github.com/router-for-me/CLIProxyAPI/v8/sdk/pluginabi"
	"github.com/router-for-me/CLIProxyAPI/v8/sdk/pluginapi"
)

var essentialCookies = []string{"hatch_sess", "hatch_gw", "hatch_vml", "hatch_native_auth_device"}

type credential struct {
	Type        string                     `json:"type"`
	PluginOwner string                     `json:"_plugin_owner,omitempty"`
	Label       string                     `json:"label,omitempty"`
	Disabled    bool                       `json:"disabled,omitempty"`
	ProxyURL    string                     `json:"proxy_url,omitempty"`
	Cookies     map[string]string          `json:"cookies"`
	Expires     map[string]int64           `json:"cookies_exp,omitempty"`
	Extra       map[string]json.RawMessage `json:"-"`
}

func decodeCredential(raw []byte) (credential, error) {
	var c credential
	if errDecode := json.Unmarshal(raw, &c); errDecode != nil {
		return c, requestError("invalid_credential", "Invalid Muse credential JSON")
	}
	if c.Type != providerID {
		return c, requestError("invalid_credential", "Credential does not belong to Muse.ai")
	}
	if errValidate := validateCookies(c.Cookies); errValidate != nil {
		return c, errValidate
	}
	if errDecode := json.Unmarshal(raw, &c.Extra); errDecode != nil {
		return c, errDecode
	}
	if c.Expires == nil {
		c.Expires = make(map[string]int64)
	}
	return c, nil
}

func validateCookies(cookies map[string]string) error {
	for name, value := range cookies {
		if strings.ContainsAny(name, " \t\r\n;=,") || name == "" || strings.ContainsAny(value, "\r\n;") {
			return requestError("invalid_cookie", "Cookie contains an invalid name or value")
		}
	}
	for _, name := range essentialCookies {
		if cookies[name] == "" {
			return requestError("missing_cookie", "Missing essential Muse Cookie: "+name)
		}
	}
	return nil
}

func encodeCredential(c credential) []byte {
	out := c.Extra
	if out == nil {
		out = make(map[string]json.RawMessage)
	}
	for key, value := range map[string]any{"type": providerID, "label": c.Label, "disabled": c.Disabled, "proxy_url": c.ProxyURL, "cookies": c.Cookies, "cookies_exp": c.Expires} {
		out[key] = jsonBytes(value)
	}
	return jsonBytes(out)
}

func parseAuth(raw []byte) (pluginapi.AuthParseResponse, error) {
	var req pluginapi.AuthParseRequest
	if errDecode := json.Unmarshal(raw, &req); errDecode != nil {
		return pluginapi.AuthParseResponse{}, errDecode
	}
	var kind struct {
		Type string `json:"type"`
	}
	if errDecode := json.Unmarshal(req.RawJSON, &kind); errDecode != nil || kind.Type != providerID {
		return pluginapi.AuthParseResponse{}, nil
	}
	c, errCredential := decodeCredential(req.RawJSON)
	if errCredential != nil {
		return pluginapi.AuthParseResponse{}, errCredential
	}
	return pluginapi.AuthParseResponse{Handled: true, Auth: credentialAuth(c, "", req.FileName)}, nil
}

func credentialAuth(c credential, id, name string) pluginapi.AuthData {
	return pluginapi.AuthData{Provider: providerID, ID: id, FileName: name, Label: c.Label, Disabled: c.Disabled, ProxyURL: c.ProxyURL, StorageJSON: encodeCredential(c), Metadata: map[string]any{"type": providerID, "_plugin_owner": c.PluginOwner, "label": c.Label, "disabled": c.Disabled, "cookies": c.Cookies, "cookies_exp": c.Expires}, NextRefreshAfter: nextRefresh(c)}
}

func (c credential) browserCookies() []browser.Cookie {
	out := make([]browser.Cookie, 0, len(c.Cookies))
	for name, value := range c.Cookies {
		out = append(out, browser.Cookie{Name: name, Value: value, Expires: float64(c.Expires[name])})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out
}

func (c credential) cookieHeader() string {
	names := make([]string, 0, len(c.Cookies))
	for name := range c.Cookies {
		names = append(names, name)
	}
	sort.Strings(names)
	var parts []string
	for _, name := range names {
		parts = append(parts, name+"="+c.Cookies[name])
	}
	return strings.Join(parts, "; ")
}

func (p *provider) renew(ctx context.Context, c credential, scope, authIndex string) (credential, error) {
	raw, errOpen := p.call(pluginabi.MethodHostHTTPOperationOpen, map[string]any{"host_callback_id": scope})
	if errOpen != nil {
		return c, errOpen
	}
	var operation struct {
		ID string `json:"operation_id"`
	}
	if errDecode := json.Unmarshal(raw, &operation); errDecode != nil || operation.ID == "" {
		return c, requestError("host_scope_missing", "Host did not open a cancellable session probe")
	}
	closed := make(chan struct{})
	stop := context.AfterFunc(ctx, func() {
		_, _ = p.call(pluginabi.MethodHostHTTPCancel, map[string]any{"host_callback_id": scope, "operation_id": operation.ID})
		close(closed)
	})
	defer func() {
		if !stop() {
			<-closed
		}
		_, _ = p.call(pluginabi.MethodHostHTTPCancel, map[string]any{"host_callback_id": scope, "operation_id": operation.ID})
	}()
	result, errCall := p.call(pluginabi.MethodHostHTTPDo, map[string]any{
		"host_callback_id": scope,
		"auth_index":       authIndex,
		"operation_id":     operation.ID,
		"request":          map[string]any{"method": "GET", "url": "https://muse.ai/api/session", "headers": http.Header{"Cookie": {c.cookieHeader()}, "Origin": {"https://muse.ai"}, "Referer": {"https://muse.ai/thread/new"}, "Accept": {"application/json"}}},
	})
	if errCall != nil {
		return c, errCall
	}
	var response pluginapi.HTTPResponse
	if errDecode := json.Unmarshal(result, &response); errDecode != nil {
		return c, requestError("session_unconfirmed", "Muse session response could not be decoded")
	}
	if response.StatusCode != 200 {
		message := "Muse session could not be confirmed"
		code := "session_unconfirmed"
		switch response.StatusCode {
		case 401:
			code, message = "cookie_expired", "Muse session is not authenticated; import fresh Cookies"
		case 403:
			code, message = "access_denied", "Muse denied access; check egress/region. Cookie expiry is not established"
		case 429:
			code, message = "quota_exhausted", "Muse session request was rate limited"
		}
		return c, &pluginabi.Error{Code: code, Message: message, HTTPStatus: response.StatusCode, NoRetry: true}
	}
	var session struct {
		Status string `json:"status"`
	}
	if errDecode := json.Unmarshal(response.Body, &session); errDecode != nil || session.Status != "assigned" {
		return c, &pluginabi.Error{Code: "workspace_unassigned", Message: "Muse did not confirm an assigned workspace; inspect the account on muse.ai", HTTPStatus: 502, NoRetry: true}
	}
	for _, cookie := range (&http.Response{Header: response.Headers}).Cookies() {
		if cookie.MaxAge < 0 || (!cookie.Expires.IsZero() && cookie.Expires.Before(time.Now())) {
			delete(c.Cookies, cookie.Name)
			delete(c.Expires, cookie.Name)
			continue
		}
		if cookie.Value == "" {
			continue
		}
		c.Cookies[cookie.Name] = cookie.Value
		if cookie.MaxAge > 0 {
			c.Expires[cookie.Name] = time.Now().Unix() + int64(cookie.MaxAge)
		} else if !cookie.Expires.IsZero() {
			c.Expires[cookie.Name] = cookie.Expires.Unix()
		} else {
			delete(c.Expires, cookie.Name)
		}
	}
	if errValidate := validateCookies(c.Cookies); errValidate != nil {
		return c, errValidate
	}
	return c, nil
}

func (p *provider) refreshAuth(raw []byte) (pluginapi.AuthRefreshResponse, error) {
	var req struct {
		pluginapi.AuthRefreshRequest
		scopedRequest
	}
	if errDecode := json.Unmarshal(raw, &req); errDecode != nil {
		return pluginapi.AuthRefreshResponse{}, errDecode
	}
	ctx, cancel, errCtx := p.scopedContext(req.CallbackID)
	if errCtx != nil {
		return pluginapi.AuthRefreshResponse{}, errCtx
	}
	defer cancel()
	unlock, errLock := p.lockAccount(ctx, req.AuthID)
	if errLock != nil {
		return pluginapi.AuthRefreshResponse{}, errLock
	}
	defer unlock()
	c, errCredential := p.credentialByID(req.AuthID)
	if errCredential != nil {
		return pluginapi.AuthRefreshResponse{}, errCredential
	}
	c, errRenew := p.renew(ctx, c, req.CallbackID, "")
	if errRenew != nil {
		return pluginapi.AuthRefreshResponse{}, errRenew
	}
	return pluginapi.AuthRefreshResponse{Auth: credentialAuth(c, req.AuthID, ""), NextRefreshAfter: nextRefresh(c)}, nil
}

func (p *provider) authEntries() ([]pluginapi.HostAuthFileEntry, error) {
	raw, errCall := p.call(pluginabi.MethodHostAuthList, struct{}{})
	if errCall != nil {
		return nil, errCall
	}
	var response struct {
		Files []pluginapi.HostAuthFileEntry `json:"files"`
	}
	if errDecode := json.Unmarshal(raw, &response); errDecode != nil {
		return nil, errDecode
	}
	out := make([]pluginapi.HostAuthFileEntry, 0)
	for _, entry := range response.Files {
		if (entry.Provider == providerID || entry.Type == providerID) && !entry.RuntimeOnly {
			out = append(out, entry)
		}
	}
	return out, nil
}

func (p *provider) getCredential(index string) (credential, pluginapi.HostAuthGetResponse, error) {
	entries, errList := p.authEntries()
	if errList != nil {
		return credential{}, pluginapi.HostAuthGetResponse{}, errList
	}
	found := false
	for _, entry := range entries {
		if entry.AuthIndex == index {
			found = true
			break
		}
	}
	if index == "" || !found {
		return credential{}, pluginapi.HostAuthGetResponse{}, requestError("auth_not_found", "Muse account not found")
	}
	raw, errCall := p.call(pluginabi.MethodHostAuthGet, pluginapi.HostAuthGetRequest{AuthIndex: index})
	if errCall != nil {
		return credential{}, pluginapi.HostAuthGetResponse{}, errCall
	}
	var response pluginapi.HostAuthGetResponse
	if errDecode := json.Unmarshal(raw, &response); errDecode != nil {
		return credential{}, response, errDecode
	}
	c, errCredential := decodeCredential(response.JSON)
	return c, response, errCredential
}

func (p *provider) credentialByID(id string) (credential, error) {
	entries, errList := p.authEntries()
	if errList != nil {
		return credential{}, errList
	}
	for _, entry := range entries {
		if entry.ID != id {
			continue
		}
		c, _, errGet := p.getCredential(entry.AuthIndex)
		if errGet != nil {
			return credential{}, errGet
		}
		if c.Disabled {
			return credential{}, requestError("auth_disabled", "Muse account is disabled")
		}
		return c, nil
	}
	return credential{}, requestError("auth_not_found", "Selected Muse account no longer exists")
}

func (p *provider) saveCredential(name string, c credential) error {
	if name == "" || filepath.Base(name) != name || !strings.HasSuffix(name, ".json") {
		return requestError("invalid_name", "Credential name must be a base .json filename")
	}
	_, errCall := p.call(pluginabi.MethodHostAuthSave, pluginapi.HostAuthSaveRequest{Name: name, JSON: encodeCredential(c)})
	return errCall
}

func (p *provider) syncCookies(id string, c credential, cookies []browser.Cookie) {
	if len(cookies) == 0 {
		return
	}
	entries, errList := p.authEntries()
	if errList != nil {
		p.log("warn", "Muse refreshed Cookies could not be persisted")
		return
	}
	for _, entry := range entries {
		if entry.ID != id {
			continue
		}
		latest, saved, errGet := p.getCredential(entry.AuthIndex)
		if errGet != nil {
			p.log("warn", "Muse refreshed Cookies could not be read")
			return
		}
		// A concurrent user re-import wins over this older browser session.
		if latest.Cookies["hatch_native_auth_device"] != c.Cookies["hatch_native_auth_device"] || latest.Cookies["hatch_sess"] != c.Cookies["hatch_sess"] {
			return
		}
		latest.Cookies = make(map[string]string, len(cookies))
		latest.Expires = make(map[string]int64, len(cookies))
		for _, cookie := range cookies {
			latest.Cookies[cookie.Name] = cookie.Value
			if cookie.Expires > 0 {
				latest.Expires[cookie.Name] = int64(cookie.Expires)
			}
		}
		if errValidate := validateCookies(latest.Cookies); errValidate != nil {
			p.log("warn", "Muse browser returned incomplete Cookies; stored credential unchanged")
			return
		}
		if errSave := p.saveCredential(saved.Name, latest); errSave != nil {
			p.log("warn", "Muse refreshed Cookies could not be persisted")
		}
		return
	}
}

func parseImportedCookies(raw json.RawMessage) (map[string]string, error) {
	var out map[string]string
	if json.Unmarshal(raw, &out) == nil && out != nil {
		return out, validateCookies(out)
	}
	var header string
	if errDecode := json.Unmarshal(raw, &header); errDecode != nil {
		return nil, requestError("invalid_cookie", "cookies must be an object or Cookie header string")
	}
	out = make(map[string]string)
	for _, part := range strings.Split(strings.TrimPrefix(strings.TrimSpace(header), "Cookie:"), ";") {
		name, value, found := strings.Cut(strings.TrimSpace(part), "=")
		if found && name != "" {
			out[name] = value
		}
	}
	return out, validateCookies(out)
}

func credentialExpiry(c credential) int64 {
	var earliest int64
	for _, name := range essentialCookies {
		if exp := c.Expires[name]; exp > 0 && (earliest == 0 || exp < earliest) {
			earliest = exp
		}
	}
	return earliest
}

func authIDByIndex(entries []pluginapi.HostAuthFileEntry, index string) (string, error) {
	for _, entry := range entries {
		if entry.AuthIndex == index {
			return entry.ID, nil
		}
	}
	return "", fmt.Errorf("Muse credential not found")
}
