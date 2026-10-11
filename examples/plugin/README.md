# Standard Dynamic Library Plugin Examples

This directory contains standard dynamic library plugin examples for the CLIProxyAPI C ABI.

## Layout

- `simple/`: full provider-native skeleton that declares every supported capability.
- `model/`: model capability only.
- `auth/`: auth provider capability only.
- `frontend-auth/`: frontend auth provider capability only.
- `frontend-auth-exclusive/`: frontend auth provider that becomes the only request authentication provider when selected.
- `executor/`: executor capability only.
- `protocol-format/`: minimal executor focused on input/output format declarations.
- `request-translator/`: request translation capability only.
- `request-normalizer/`: request normalization capability only.
- `codex-service-tier/`: Go-only request normalizer that sets Codex `gpt-5.5` requests to the priority service tier when enabled.
- `request-lifecycle/`: Go-only request admission example with concurrency control, active HTTP termination, and terminal callbacks.
- `scheduler/`: Go-only scheduler that can select a configured auth ID, delegate to a built-in scheduler, or deny picks.
- `claude-web-search-router/`: ModelRouter + executor for Claude Code built-in `web_search` (antigravity / codex / xai / Tavily). See `claude-web-search-router/README.md`.
- `response-translator/`: response translation capability only.
- `response-normalizer/`: response normalization capability only.
- `thinking/`: thinking applier capability only.
- `usage/`: usage observer capability only.
- `cli/`: command-line capability only.
- `management-api/`: Management API and resource capability only.
- `host-callback/`: minimal plugin resource that demonstrates host callbacks.
- `host-callback-auth-files/`: Go-only plugin resource that calls host auth file callbacks.
- `host-model-callback/`: Go-only plugin resource that calls the host model execution callbacks.

Most standard capability examples contain `go/`, `c/`, and `rust/` subdirectories. Specialized examples may provide only the implementation language they need.

## Codex Service Tier

`codex-service-tier` declares the request normalization capability. When `fast` is `true`, it sets `service_tier` to `priority` for requests where `req.ToFormat` is `codex` and `req.Model` is `gpt-5.5`.

```yaml
plugins:
  configs:
    codex-service-tier:
      enabled: true
      priority: 1
      fast: false
```

## Request Lifecycle

`request-lifecycle` combines `request_interceptor` with `request_lifecycle_plugin`. It acquires a concurrency slot before auth selection, can return a custom `403` or `429` response without contacting an upstream model, and releases admitted slots from `request.complete` on success, failure, rejection, or cancellation.

```yaml
plugins:
  configs:
    request-lifecycle:
      enabled: true
      priority: 100
      max_concurrency: 2
      reject_keyword: "blocked"
```

See `request-lifecycle/README.md` for build instructions and lifecycle semantics.

## Host Auth Files Callback

`host-callback-auth-files` declares the Management API capability and exposes a browser resource named `Host Auth Files`. The resource demonstrates `host.auth.list`, `host.auth.get` (physical JSON file), `host.auth.get_runtime`, and `host.auth.save`.

```yaml
plugins:
  configs:
    host-callback-auth-files:
      enabled: true
      priority: 1
```

See `host-callback-auth-files/README.md` for URL examples.

## Host Model Callback

`host-model-callback` declares the Management API capability and exposes a browser resource named `Host Model Callback`. The resource calls `host.model.execute` for non-streaming requests and `host.model.execute_stream` plus `host.model.stream_read` for streaming requests. It demonstrates explicit stream close with `host.model.stream_close` and an `implicit_close=true` option for RPC-scope host cleanup.

When the resource forwards its `host_callback_id`, CPA identifies the plugin that initiated the host model callback and skips that same plugin's interceptors for the nested execution. This makes host model callbacks non-recursive for the caller while allowing other plugins to intercept the nested request.

```yaml
plugins:
  configs:
    host-model-callback:
      enabled: true
      priority: 1
```

The default example model is `gpt-5.5`, but the request succeeds only when the current CPA model and auth configuration can route that model.

## Host HTTP Callback Cancellation

Shared-library plugins can make `host.http.do` and `host.http.do_stream` cancelable before a response or `stream_id` is returned:

1. Call `host.http.operation_open` with the same live `host_callback_id` that the HTTP request will use, if any. The callback ID must belong to the calling plugin. The response contains a host-generated `operation_id` scoped to that plugin.
2. Include that `operation_id` at the top level of the `host.http.do` or `host.http.do_stream` request, and forward the same `host_callback_id` used when opening the operation.
3. Call `host.http.cancel` with the same `operation_id` to cancel the in-flight request. For an opened stream, cancellation also closes its host-side stream entry; `host.http.stream_close` remains supported.

Each operation ID can be claimed by one HTTP callback. HTTP stream IDs are also scoped to the owning plugin. Cancel an opened operation if it will not be used, and close streams when finished. This API provides explicit cancellation, not a host-managed per-call timeout; a plugin that wants a deadline must arrange its own cancellation call.

Schema 8 plugins can also observe cancellation of non-HTTP work (for example,
owned Chromium/CDP sessions) using `host.operation.wait`. Forward the live
`host_callback_id` and an optional fresh `operation_id` from
`host.http.operation_open`. The blocking callback returns an envelope containing
`{"canceled":true}` when the request, callback scope, operation, or plugin instance
closes. A scope or operation already removed before the waiter starts returns an
error; treat that as cancellation. IDs belong to the exact calling plugin instance.
The waiter claims its operation, so do not share that ID with an HTTP callback.
Release an opened operation with `host.http.cancel` when the owned work finishes.

When a schema 8 plugin is disabled, the host removes its routable capabilities,
cancels its existing callback scopes and HTTP operations, and invokes
`plugin.quiesce`. Its library stays loaded for reconfiguration on reenable;
the plugin must stop its owned work on quiesce without shutting down unrelated
processes. Older plugin schemas keep their existing disable behavior.

Executor streams belong to their host callback scope. Disabling or unloading
the owning plugin closes in-flight streams with a request-scoped, non-retriable
cancellation error, even if the plugin can no longer call `host.stream.close`.
Accepted chunks remain ordered before that error; incomplete output is not
reported as a successful completion.

Executors receive `browser_transport` with the host-resolved `proxy_url`. An
explicit `direct` overrides the global proxy. Unsupported custom transports,
destination-dependent context proxy functions, and environment proxies with
`NO_PROXY` bypass rules set `error`; configure an explicit request/account/global
proxy or `direct` rather than silently changing the browser's routing policy.
Browser executors must reject an error. Browser traffic is not a `HostHTTPClient`
request and is not covered by
the host's raw upstream HTTP request logs.

## Scheduler

`scheduler` declares the scheduler capability. It can select a configured auth ID from the candidate list, delegate to the built-in `fill-first` or `round-robin` scheduler, or reject picks when `deny` is `true`.

```yaml
plugins:
  configs:
    scheduler:
      enabled: true
      priority: 1
      auth_id: ""
      delegate: ""
      deny: false
```

`auth_id` selects a matching candidate when `delegate` is empty. `delegate` accepts `""`, `fill-first`, or `round-robin`; other non-empty values leave the pick unhandled. `deny` returns a scheduler error.

## Plugin Executor Error Handling and HTTP Status

When a plugin executor encounters an upstream failure (such as `401 Unauthorized` for invalid credentials, `403 Forbidden` for model permission/quota limits, or `429 Too Many Requests` for rate limits), it should report the HTTP status code in the error envelope:

- In the JSON RPC error envelope, set the `http_status` field inside the `error` object (`pluginabi.Error.HTTPStatus`).
- If `http_status` is omitted or `0`, CPA defaults to returning HTTP `500 Internal Server Error` (`server_error` / `internal_server_error`), which clients typically treat as a temporary gateway outage and retry with backoff.
- When `http_status` is set, CPA maps the status into client-visible error responses:
  - `401` -> HTTP 401 with `type: "authentication_error"`, `code: "invalid_api_key"`
  - `403` -> HTTP 403 with `type: "permission_error"`, `code: "insufficient_quota"`
  - `429` -> HTTP 429 with `type: "rate_limit_error"`, `code: "rate_limit_exceeded"`
  - `404` -> HTTP 404 with `type: "invalid_request_error"`, `code: "model_not_found"`
  - `>=500` -> HTTP 5xx with `type: "server_error"`, `code: "internal_server_error"`
- **Important**: Both non-streaming (`executor.execute`) and streaming (`executor.execute_stream`) call sites must include `http_status` so failures are classified consistently.
- Because native dynamic library plugins communicate across the C ABI via serialized JSON buffers, the status code must be encoded in the serialized JSON envelope (e.g. using `pluginabi.NewErrorEnvelope` or a custom envelope struct with an `http_status` field). Returning an unmarshaled Go error does not traverse the C ABI boundary.
- `request_scoped: true` prevents credential-wide cooldown for request-specific failures.
- `no_retry: true` prohibits replay, credential rotation, and refresh for that operation. Use it after an ambiguous or confirmed upstream submission. Stream errors carry these fields in `error_detail` on `host.stream.emit` and `host.stream.close`.

Go plugins can import `github.com/router-for-me/CLIProxyAPI/v8/sdk/pluginabi` and use `pluginabi.NewErrorEnvelope(code, message, httpStatus)`:

```go
// Recommended: construct an error envelope directly using sdk/pluginabi
rawEnvelope, errMarshal := pluginabi.NewErrorEnvelope("insufficient_quota", "plan limit reached", http.StatusForbidden)
```

Alternatively, plugins defining a custom envelope struct can declare an `HTTPStatus` field (`json:"http_status,omitempty"`):

```go
type envelopeError struct {
	Code       string `json:"code"`
	Message    string `json:"message"`
	HTTPStatus int    `json:"http_status,omitempty"`
}

func errorEnvelope(code, message string, httpStatus ...int) []byte {
	status := 0
	if len(httpStatus) > 0 {
		status = httpStatus[0]
	}
	raw, _ := json.Marshal(envelope{
		OK: false,
		Error: &envelopeError{
			Code:       code,
			Message:    message,
			HTTPStatus: status,
		},
	})
	return raw
}
```

## Build All Examples

```bash
make -C examples/plugin list
make -C examples/plugin build
```

Artifacts are written to `examples/plugin/bin`.

## Notes

`protocol-format` uses a minimal executor because format declarations belong to executor capabilities.

`host-callback` uses a minimal plugin resource because host callbacks are invoked from plugin methods and are not standalone capabilities.

Schema 8 menu resources returned by `management.register` are exposed under `/v8/resource/plugins/<pluginID>/...`; their authenticated Management API routes use `/v8/management/...`. Earlier schemas retain their declared `/v0` paths. Resource HTML can be public, so it must not embed credentials or perform unauthenticated privileged operations.
