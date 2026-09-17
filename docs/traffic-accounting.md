# Traffic accounting per access key

CLIProxyAPI records token usage per access key; it also records **payload bytes**
so an operator can see what each key costs in bandwidth. This page documents the
measurement contract, where the figures are exposed, and how to validate them
against the Nginx front door.

## What is measured

One request produces four counters on the proxy:

| Counter | Meaning | Measurement point |
| --- | --- | --- |
| `client_in_bytes` | request payload read from the client | access-key authentication (`requestModel`) |
| `client_out_bytes` | response payload written to the client | access-key middleware (`trafficWriter`) |
| `upstream_in_bytes` | response payload read from the provider | executor transport wrapper (`UsageReporter`) |
| `upstream_out_bytes` | request payload sent to the provider | executor transport wrapper (`UsageReporter`) |

Derived values:

- **Cost figure** — `client_in + client_out + upstream_in + upstream_out`. This
  approximates the host interface traffic caused by the key, which is what a
  bandwidth bill charges.
- **Client figure** — `client_in + client_out`. The leg between the customer and
  this proxy.

Only payload bytes are counted: HTTP status lines, headers, TLS records, TCP
retransmits and keep-alive traffic are excluded. Nginx totals (see below) are
therefore slightly higher for the same traffic.

Websocket traffic is included for the client leg: the middleware wraps the
hijacked connection so frames count in both directions. Provider-leg websocket
traffic (Codex/xAI websocket executors), gRPC transports and plugin-provided
executors never pass through the tracked HTTP transport, so their
`upstream_*` counters stay zero; `client_*` still covers every access-key
request.

## Where the figures live

The counters persist with the access-key store (`auths/access-keys.store`, or the
configured Postgres backend) and appear in:

- `GET /v0/management/access-keys` → each key's `usage` block, including a
  `bytes` summary and per-model/per-day/per-auth `in_bytes`/`out_bytes`.
- `GET /v0/management/access-keys/usage?id=<id>` → operator detail, including
  `usage.bytes` (client, upstream, total, period).
- `GET /v0/management/access-keys/usage-top?by=bytes&period=month` → bandwidth
  leaderboard. Values are summed from daily rows for `month`/`day`.
- `GET /v0/management/access-groups/usage?name=<group>` → group roll-up.
- Management center → Usage monitor → **Keys** tab: traffic column, rank by
  traffic, CSV export, and an optional CNY/GB price (stored in the browser under
  `akTrafficPrice`) that converts traffic into cost.
- Access-key page (`/access-keys.html`): traffic and cost column, and a traffic
  quota field when issuing or editing a key.
- The usage queue (`/v0/management/usage-queue`) carries `request_bytes` and
  `response_bytes` for external collectors.

Self-service endpoints never expose traffic: `/v0/usage/me` returns a summary
with every byte counter stripped (totals and dimension rows), and byte limits
are hidden too.

## Quotas

`quota.byte_limit` (bytes, `0` = unlimited) is evaluated against the cost figure
for the configured `quota.period` (`daily`, `monthly`, or empty for all-time).
Admission is checked at authentication time, exactly like `token_limit`, so a key
can overshoot its limit by at most one in-flight request.

## Reconciling with Nginx

The JP deployment serves the proxy behind Nginx, which independently counts
bytes for every request. Reconciling the two proves the per-key figures are sane.

### 1. Log format

Add a dedicated format that ends with the contract tail (the prefix is the first
16 characters of the presented key, the same value the management API reports as
`key_prefix`, so full keys never reach the log):

```nginx
map $http_authorization $cpa_key_prefix {
    default          "-";
    "~^Bearer\s+(.{16})"  $1;
    "~^(.{16})"           $1;
}

log_format cpa_traffic '$remote_addr - $remote_user [$time_local] "$request" '
                       '$status $request_length $bytes_sent $cpa_key_prefix';

access_log /var/log/nginx/cpa-traffic.log cpa_traffic;
```

The tool counts the last three fields from the right, so other fields may contain
spaces safely.

### 2. Run the reconciliation

```sh
go run ./cmd/traffic-reconcile \
  -log /var/log/nginx/cpa-traffic.log \
  -api http://127.0.0.1:8317 \
  -key "$MANAGEMENT_SECRET"

# month-to-date, failing when any key deviates by more than 10%
go run ./cmd/traffic-reconcile -log /var/log/nginx/cpa-traffic.log \
  -since 2026-09-01 -max-diff 10 ...
```

The output lists `PROXY` (client-leg bytes recorded by the proxy), `NGINX`
(`$request_length + $bytes_sent`) and the percentage difference per key, plus
any log prefixes that do not match a known key (revoked or invalid credentials).

Expected results:

- `NGINX` should exceed `PROXY` by roughly 1–5% for normal traffic (HTTP headers
  and keep-alive framing).
- A large positive gap points at a client path the proxy does not count (for
  example another service on the same Nginx vhost).
- `PROXY` above `NGINX` is a bug: report it with the log excerpt.

`-since` compares the log's local timestamps with the store's UTC daily rows, so
a day boundary can shift by the host's UTC offset.

### 3. Reconciling with vnstat

`GET /v0/management/server-stats` reports the host's monthly interface traffic
(vnstat or the builtin counter). `sum(cost figure of all keys)` will always be
lower than that number because it excludes:

- non-key traffic: OAuth refreshes, model catalogue fetches, plugin quota
  polling, management/panel traffic, health checks;
- protocol overhead: TLS, TCP, retransmits, keep-alive;
- when Nginx and the proxy share a host, the loopback leg between them.

A month-to-date ratio between 90% and 100% is expected; the remainder is the
traffic that cannot be attributed to a key.

## Accuracy caveats

- **Transparent decompression.** Go's HTTP transport removes `Accept-Encoding`
  handling from the caller when it added the header itself: such responses are
  decompressed before the counter sees them, so `upstream_in_bytes` reports the
  expanded size. Executors that set their own `Accept-Encoding`, or that build
  their transport through `NewProxyAwareHTTPClient` with a configured proxy,
  are unaffected. Treat `upstream_in_bytes` as an upper bound when a provider
  compresses responses.
- **Front-door compression.** If Nginx gzips responses for the API vhost, its
  `$bytes_sent` is smaller than the proxy's `client_out_bytes` and the
  reconciliation difference on those keys turns negative. Disable gzip for the
  API location, or read the sign accordingly.
- **Oversized request bodies.** Authentication reads at most 16 MiB of the
  request body for model inspection, and the handler sees the same truncated
  body. `client_in_bytes` therefore stops at 16 MiB for such requests; this cap
  predates traffic accounting.
- **Hijacked handshakes.** After a websocket upgrade the connection is counted
  directly, so pipelined bytes sent before the handshake completes are read as
  frame data (a protocol error) instead of being rejected during the upgrade.
- **Frame and protocol overhead.** The client leg counts what the proxy reads
  and writes on hijacked connections, including websocket frame headers, but
  never TLS records or TCP-level retransmissions.
