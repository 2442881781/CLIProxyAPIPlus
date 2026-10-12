package main

import (
	"encoding/json"
	"math"
	"strings"
	"time"

	"cpa-plugin-museai/internal/browser"
	"github.com/router-for-me/CLIProxyAPI/v8/sdk/pluginabi"
	"github.com/router-for-me/CLIProxyAPI/v8/sdk/pluginapi"
)

func normalizeMuseQuota(q browser.Quota) (pluginapi.QuotaFetchResponse, error) {
	out := pluginapi.QuotaFetchResponse{
		Subscription: &pluginapi.QuotaSubscription{Plan: q.UsageRowLabel, TierName: q.Tier.Name, TierID: q.Tier.ID},
	}
	if q.Usage == nil {
		return out, quotaUnconfirmed()
	}
	var buckets []pluginapi.QuotaBucket
	switch q.Usage.State {
	case "METERED":
		if q.Usage.PercentUsed == nil || !finite(*q.Usage.PercentUsed) || *q.Usage.PercentUsed < 0 || *q.Usage.PercentUsed > 100 {
			return out, quotaUnconfirmed()
		}
		window := "subscription"
		if strings.HasPrefix(q.StatusSubtitle, "Weekly limit") {
			window = "weekly"
		} else if strings.HasPrefix(q.StatusSubtitle, "Monthly limit") {
			window = "monthly"
		}
		bucket := pluginapi.QuotaBucket{Window: window, RemainingFraction: 1 - *q.Usage.PercentUsed/100}
		if q.Usage.ResetsAt != nil && *q.Usage.ResetsAt > 0 {
			bucket.ResetTime = time.Unix(*q.Usage.ResetsAt, 0).UTC().Format(time.RFC3339)
		}
		buckets = append(buckets, bucket)
	case "UNLIMITED":
		out.Unlimited = true
	default:
		return out, quotaUnconfirmed()
	}
	if q.TopupTotal != nil {
		if !finite(*q.TopupTotal) || *q.TopupTotal < 0 || q.TopupBalance == nil || !finite(*q.TopupBalance) || *q.TopupBalance < 0 || *q.TopupBalance > *q.TopupTotal {
			return out, quotaUnconfirmed()
		}
	}
	if q.TopupTotal != nil && *q.TopupTotal > 0 {
		// Muse's top-up values are internal credits, not token counts. Keep only
		// their ratio and the site's display label; never invent a conversion.
		buckets = append(buckets, pluginapi.QuotaBucket{Window: "additional_tokens", RemainingFraction: *q.TopupBalance / *q.TopupTotal, Description: strings.TrimSpace(q.TopupRowValueLabel)})
	}
	if len(buckets) > 0 {
		out.Groups = []pluginapi.QuotaGroup{{DisplayName: "Muse.ai", Buckets: buckets}}
	}
	return out, nil
}

func finite(value float64) bool { return !math.IsNaN(value) && !math.IsInf(value, 0) }

func quotaUnconfirmed() *pluginabi.Error {
	return &pluginabi.Error{Code: "quota_unconfirmed", Message: "Muse did not return a valid subscription usage; no quota was estimated", HTTPStatus: 502, RequestScoped: true, NoRetry: true}
}

func (p *provider) fetchQuota(raw []byte) (pluginapi.QuotaFetchResponse, error) {
	var req struct {
		pluginapi.QuotaFetchRequest
		scopedRequest
	}
	if errDecode := json.Unmarshal(raw, &req); errDecode != nil {
		return pluginapi.QuotaFetchResponse{}, requestError("invalid_request", "Invalid Muse quota request")
	}
	if req.AuthID == "" || req.Provider != providerID {
		return pluginapi.QuotaFetchResponse{}, requestError("auth_not_found", "Muse account not found")
	}
	ctx, cancel, errCtx := p.scopedContext(req.CallbackID)
	if errCtx != nil {
		return pluginapi.QuotaFetchResponse{}, errCtx
	}
	defer cancel()
	unlock, errLock := p.lockAccount(ctx, req.AuthID)
	if errLock != nil {
		return pluginapi.QuotaFetchResponse{}, errLock
	}
	defer unlock()
	c, errCredential := decodeCredential(req.StorageJSON)
	if errCredential != nil {
		return pluginapi.QuotaFetchResponse{}, errCredential
	}
	if c.Disabled {
		return pluginapi.QuotaFetchResponse{}, requestError("auth_disabled", "Muse account is disabled")
	}
	proxy := c.ProxyURL
	if proxy == "" {
		proxy = req.Host.ProxyURL
	}
	q, errRead := p.engine.ReadQuota(ctx, browser.Input{AuthID: req.AuthID, ProxyURL: proxy, Cookies: c.browserCookies()})
	if errRead != nil {
		return pluginapi.QuotaFetchResponse{}, errRead
	}
	return normalizeMuseQuota(q)
}
