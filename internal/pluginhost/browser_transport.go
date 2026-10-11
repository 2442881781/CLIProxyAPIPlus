package pluginhost

import (
	"context"
	"fmt"
	"net/http"
	"net/url"
	"os"
	"strings"

	coreauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
	coreexecutor "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/executor"
	"github.com/router-for-me/CLIProxyAPI/v8/sdk/pluginapi"
	"github.com/router-for-me/CLIProxyAPI/v8/sdk/proxyutil"
)

// Browser transports cannot reproduce arbitrary Go RoundTrippers. Fail closed when
// no explicit proxy policy supersedes a transport that cannot be represented.
func (h *Host) browserTransport(ctx context.Context, auth *coreauth.Auth, requestProxy string) *pluginapi.BrowserTransport {
	policy := &pluginapi.BrowserTransport{}
	raw := strings.TrimSpace(requestProxy)
	if raw == "" {
		raw = coreexecutor.RequestProxyURL(ctx)
	}
	if raw == "" && auth != nil {
		raw = strings.TrimSpace(auth.ProxyURL)
	}
	if raw == "" {
		if cfg := h.currentRuntimeConfig(); cfg != nil {
			raw = strings.TrimSpace(cfg.ProxyURL)
		}
	}
	if raw != "" {
		setting, err := proxyutil.Parse(raw)
		if err != nil {
			policy.Error = "unsupported browser proxy policy"
			return policy
		}
		if setting.Mode == proxyutil.ModeDirect {
			policy.ProxyURL = "direct"
			return policy
		}
		if setting.Mode == proxyutil.ModeProxy {
			policy.ProxyURL = raw
			return policy
		}
	}
	// A single browser proxy cannot represent destination-dependent bypass rules.
	if ctx != nil {
		if rt, ok := ctx.Value("cliproxy.roundtripper").(http.RoundTripper); ok && rt != nil {
			transport, ok := rt.(*http.Transport)
			if !ok || transport == nil || transport.DialContext != nil || transport.Dial != nil || transport.DialTLSContext != nil || transport.DialTLS != nil || transport.TLSClientConfig != nil {
				policy.Error = fmt.Sprintf("browser cannot represent context RoundTripper %T without an explicit proxy policy", rt)
				return policy
			}
			if transport.Proxy == nil {
				policy.ProxyURL = "direct"
				return policy
			}
			policy.Error = "browser cannot represent a destination-dependent context proxy without an explicit proxy policy"
			return policy
		}
	}
	if (strings.TrimSpace(os.Getenv("NO_PROXY")) != "" || strings.TrimSpace(os.Getenv("no_proxy")) != "") &&
		(strings.TrimSpace(os.Getenv("HTTPS_PROXY")) != "" || strings.TrimSpace(os.Getenv("https_proxy")) != "") {
		policy.Error = "browser requires an explicit proxy policy when environment proxy bypass rules are configured"
		return policy
	}
	request := &http.Request{URL: &url.URL{Scheme: "https", Host: "browser.invalid"}}
	proxy, err := http.ProxyFromEnvironment(request)
	if err != nil {
		policy.Error = "browser cannot resolve environment proxy policy"
		return policy
	}
	if proxy != nil {
		policy.ProxyURL = proxy.String()
	} else {
		policy.ProxyURL = "direct"
	}
	return policy
}
