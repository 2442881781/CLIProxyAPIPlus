package main

import (
	"encoding/json"
	"net/http"
	"path/filepath"
	"strings"

	"github.com/router-for-me/CLIProxyAPI/v8/sdk/pluginabi"
	"github.com/router-for-me/CLIProxyAPI/v8/sdk/pluginapi"
	"github.com/router-for-me/CLIProxyAPI/v8/sdk/proxyutil"
)

type managementRequest struct {
	pluginapi.ManagementRequest
	scopedRequest
}

func managementRegistration() any {
	var routes []any
	for path, methods := range map[string][]string{
		"/status": {"GET"}, "/accounts": {"GET", "POST", "PATCH", "DELETE"}, "/accounts/probe": {"POST"}, "/accounts/refresh": {"POST"},
		"/tasks": {"GET", "DELETE"}, "/media": {"GET", "DELETE"}, "/media/content": {"GET"}, "/extension": {"GET"},
	} {
		for _, method := range methods {
			routes = append(routes, map[string]string{"Method": method, "Path": managementPrefix + path})
		}
	}
	return map[string]any{"routes": routes, "resources": []any{map[string]string{"Path": "/index", "Menu": "Muse.ai", "Description": "Muse.ai personal agent Cookies, browser, video tasks and media; not Meta Muse Code"}}}
}

func managementJSON(status int, value any) pluginapi.ManagementResponse {
	return pluginapi.ManagementResponse{StatusCode: status, Headers: http.Header{"Content-Type": {"application/json"}, "Cache-Control": {"no-store"}}, Body: jsonBytes(value)}
}

func (p *provider) manage(raw []byte) (pluginapi.ManagementResponse, error) {
	var req managementRequest
	if errDecode := json.Unmarshal(raw, &req); errDecode != nil {
		return pluginapi.ManagementResponse{}, errDecode
	}
	if req.Path == "/v8/resource/plugins/museai/index" && req.Method == "GET" {
		return pluginapi.ManagementResponse{StatusCode: 200, Headers: http.Header{"Content-Type": {"text/html; charset=utf-8"}, "Cache-Control": {"no-store"}, "X-Content-Type-Options": {"nosniff"}, "Referrer-Policy": {"no-referrer"}, "Content-Security-Policy": {"default-src 'none'; script-src 'unsafe-inline'; style-src 'unsafe-inline'; connect-src 'self'; img-src 'self' blob: data:; base-uri 'none'; form-action 'none'; frame-ancestors 'self'"}}, Body: managementPage()}, nil
	}
	if !strings.HasPrefix(req.Path, managementPrefix+"/") {
		return managementJSON(404, map[string]string{"error": "Route not found"}), nil
	}
	response, errHandle := p.managementOperation(req)
	if errHandle != nil {
		pe := asPluginError(errHandle)
		status := pe.HTTPStatus
		if status == 0 {
			status = 502
		}
		return managementJSON(status, map[string]any{"error": map[string]string{"code": pe.Code, "message": pe.Message}}), nil
	}
	return response, nil
}

func (p *provider) managementOperation(req managementRequest) (pluginapi.ManagementResponse, error) {
	path := strings.TrimPrefix(req.Path, managementPrefix)
	var authID string
	if index := req.Query.Get("auth_index"); index != "" && (path == "/tasks" || path == "/media" || path == "/media/content") {
		entries, errList := p.authEntries()
		if errList != nil {
			return pluginapi.ManagementResponse{}, errList
		}
		var errID error
		authID, errID = authIDByIndex(entries, index)
		if errID != nil {
			return pluginapi.ManagementResponse{}, requestError("auth_not_found", "Muse account not found")
		}
	}
	switch path {
	case "/status":
		return managementJSON(200, map[string]any{"provider": providerID, "browser": p.engine.Status(), "tasks": len(p.store.list("")), "note": "Muse.ai personal agent, not Meta Muse Code OAuth"}), nil
	case "/extension":
		data, errZip := extensionArchive()
		if errZip != nil {
			return pluginapi.ManagementResponse{}, errZip
		}
		return pluginapi.ManagementResponse{StatusCode: 200, Headers: http.Header{"Content-Type": {"application/zip"}, "Content-Disposition": {`attachment; filename="museai-cookie-import.zip"`}, "Cache-Control": {"no-store"}}, Body: data}, nil
	case "/tasks":
		if req.Method == "DELETE" {
			if errRemove := p.store.removeTask(req.Query.Get("id"), authID); errRemove != nil {
				return pluginapi.ManagementResponse{}, errRemove
			}
			return managementJSON(200, map[string]bool{"deleted": true}), nil
		}
		return managementJSON(200, map[string]any{"tasks": p.store.list(authID)}), nil
	case "/media":
		if req.Method == "DELETE" {
			if errRemove := p.store.removeMedia(req.Query.Get("name"), authID); errRemove != nil {
				return pluginapi.ManagementResponse{}, errRemove
			}
			return managementJSON(200, map[string]bool{"deleted": true}), nil
		}
		return managementJSON(200, map[string]any{"media": p.store.mediaList(authID)}), nil
	case "/media/content":
		data, record, errRead := p.store.readMedia(req.Query.Get("name"), authID)
		if errRead != nil {
			return pluginapi.ManagementResponse{}, errRead
		}
		return pluginapi.ManagementResponse{StatusCode: 200, Headers: http.Header{"Content-Type": {record.MIME}, "Content-Disposition": {`attachment; filename="` + record.Name + `"`}, "Cache-Control": {"no-store"}, "X-Content-Type-Options": {"nosniff"}}, Body: data}, nil
	case "/accounts":
		switch req.Method {
		case "GET":
			return p.accountList()
		case "POST":
			return p.importAccount(req.Body)
		case "PATCH":
			return p.patchAccount(req)
		case "DELETE":
			_, saved, errGet := p.getCredential(req.Query.Get("auth_index"))
			if errGet != nil {
				return pluginapi.ManagementResponse{}, errGet
			}
			_, errDelete := p.call(pluginabi.MethodHostAuthDelete, pluginapi.HostAuthDeleteRequest{Name: saved.Name})
			if errDelete != nil {
				return pluginapi.ManagementResponse{}, errDelete
			}
			return managementJSON(200, map[string]bool{"deleted": true}), nil
		}
	case "/accounts/probe", "/accounts/refresh":
		return p.probeAccount(req)
	}
	return managementJSON(404, map[string]string{"error": "Route not found"}), nil
}

func (p *provider) accountList() (pluginapi.ManagementResponse, error) {
	entries, errList := p.authEntries()
	if errList != nil {
		return pluginapi.ManagementResponse{}, errList
	}
	accounts := make([]any, 0, len(entries))
	for _, entry := range entries {
		c, saved, errGet := p.getCredential(entry.AuthIndex)
		if errGet != nil {
			continue
		}
		accounts = append(accounts, map[string]any{"auth_index": entry.AuthIndex, "name": saved.Name, "label": c.Label, "disabled": entry.Disabled, "status": entry.Status, "cookie_count": len(c.Cookies), "expires_at": credentialExpiry(c), "unavailable": entry.Unavailable})
	}
	return managementJSON(200, map[string]any{"accounts": accounts}), nil
}

func (p *provider) importAccount(raw []byte) (pluginapi.ManagementResponse, error) {
	var req struct {
		Label    string           `json:"label"`
		Cookies  json.RawMessage  `json:"cookies"`
		Expires  map[string]int64 `json:"cookies_exp"`
		Name     string           `json:"name"`
		ProxyURL string           `json:"proxy_url"`
	}
	if errDecode := json.Unmarshal(raw, &req); errDecode != nil {
		return pluginapi.ManagementResponse{}, requestError("invalid_request", "Invalid account import JSON")
	}
	cookies, errCookies := parseImportedCookies(req.Cookies)
	if errCookies != nil {
		return pluginapi.ManagementResponse{}, errCookies
	}
	if _, errProxy := proxyutil.Parse(req.ProxyURL); errProxy != nil {
		return pluginapi.ManagementResponse{}, requestError("invalid_proxy", "Invalid account proxy configuration")
	}
	name := req.Name
	if name == "" {
		name = "museai-key-" + newID() + ".json"
	}
	if filepath.Base(name) != name || !strings.HasPrefix(name, "museai-key-") || !strings.HasSuffix(name, ".json") {
		return pluginapi.ManagementResponse{}, requestError("invalid_name", "Muse credential name must match museai-key-*.json")
	}
	c := credential{Type: providerID, Label: req.Label, ProxyURL: req.ProxyURL, Cookies: cookies, Expires: req.Expires}
	if c.Expires == nil {
		c.Expires = make(map[string]int64)
	}
	if errSave := p.saveCredential(name, c); errSave != nil {
		return pluginapi.ManagementResponse{}, errSave
	}
	return managementJSON(200, map[string]any{"imported": true, "name": name, "cookie_count": len(cookies)}), nil
}

func (p *provider) patchAccount(req managementRequest) (pluginapi.ManagementResponse, error) {
	var patch struct {
		Index    string  `json:"auth_index"`
		Label    *string `json:"label"`
		Disabled *bool   `json:"disabled"`
	}
	if errDecode := json.Unmarshal(req.Body, &patch); errDecode != nil {
		return pluginapi.ManagementResponse{}, requestError("invalid_request", "Invalid account update JSON")
	}
	if patch.Index == "" {
		patch.Index = req.Query.Get("auth_index")
	}
	c, saved, errGet := p.getCredential(patch.Index)
	if errGet != nil {
		return pluginapi.ManagementResponse{}, errGet
	}
	if patch.Label != nil {
		c.Label = *patch.Label
	}
	if patch.Disabled != nil {
		c.Disabled = *patch.Disabled
	}
	if errSave := p.saveCredential(saved.Name, c); errSave != nil {
		return pluginapi.ManagementResponse{}, errSave
	}
	return managementJSON(200, map[string]bool{"updated": true}), nil
}

func (p *provider) probeAccount(req managementRequest) (pluginapi.ManagementResponse, error) {
	index := req.Query.Get("auth_index")
	if index == "" {
		var body struct {
			Index string `json:"auth_index"`
		}
		_ = json.Unmarshal(req.Body, &body)
		index = body.Index
	}
	ctx, cancel, errCtx := p.scopedContext(req.CallbackID)
	if errCtx != nil {
		return pluginapi.ManagementResponse{}, errCtx
	}
	defer cancel()
	entries, errList := p.authEntries()
	if errList != nil {
		return pluginapi.ManagementResponse{}, errList
	}
	id, errID := authIDByIndex(entries, index)
	if errID != nil {
		return pluginapi.ManagementResponse{}, errID
	}
	unlock, errLock := p.lockAccount(ctx, id)
	if errLock != nil {
		return pluginapi.ManagementResponse{}, errLock
	}
	defer unlock()
	c, saved, errGet := p.getCredential(index)
	if errGet != nil {
		return pluginapi.ManagementResponse{}, errGet
	}
	// Bind the session probe to the account's proxy, even from a management scope.
	c, errRenew := p.renew(ctx, c, req.CallbackID, index)
	if errRenew != nil {
		return pluginapi.ManagementResponse{}, errRenew
	}
	if errSave := p.saveCredential(saved.Name, c); errSave != nil {
		return pluginapi.ManagementResponse{}, errSave
	}
	return managementJSON(200, map[string]any{"ok": true, "auth_index": index, "expires_at": credentialExpiry(c), "cookie_count": len(c.Cookies)}), nil
}
