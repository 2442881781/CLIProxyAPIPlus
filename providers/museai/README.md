# Muse.ai native provider

Native C-ABI provider for the [Muse.ai personal agent](https://muse.ai/), not Meta Muse Code / Muse Spark. The provider ID is `museai`. No Python/FastAPI bridge, user Chrome profile, or externally exposed CDP service is required.

## Requirements and build

- Go 1.26+, a C compiler, and this checkout's schema 8 host extensions. Older CPA hosts do not provide the operation cancellation or registered video contracts.
- A Chromium/Google Chrome executable on the machine running CPA. The plugin creates its own ephemeral profile and process for each operation.
- An authorized Muse account, working egress, and the permissions/credits required for the requested capability. Local regression tests do not establish real Muse availability.

Run from the repository root:

```bash
# macOS ARM64; output is a standard C shared library, not Go's plugin build mode.
mkdir -p plugins/darwin/arm64
(cd providers/museai && go build -buildmode=c-shared -o ../../plugins/darwin/arm64/museai-v0.1.0.dylib .)

# On Linux AMD64, with CGO and a native C compiler.
mkdir -p plugins/linux/amd64
(cd providers/museai && CGO_ENABLED=1 go build -buildmode=c-shared -o ../../plugins/linux/amd64/museai-v0.1.0.so .)

# Build the matching host.
go build -o cli-proxy-api ./cmd/server
```

Cross-building the shared library requires a target C toolchain (or a Linux AMD64 build container); `GOOS`/`GOARCH` alone is insufficient. Ship `LICENSE.muse2api` with plugin artifacts.

Merge into CPA's existing configuration rather than replacing unrelated settings:

```yaml
plugins:
  enabled: true
  dir: "plugins"
  configs:
    museai:
      enabled: true
      chromium: "/usr/bin/chromium"
      data-dir: "/var/lib/cliproxy/museai-data"
      no-sandbox: false
```

On macOS, `chromium` may be `/Applications/Google Chrome.app/Contents/MacOS/Google Chrome`. Relative `data-dir` paths resolve against CPA's working directory. Use a dedicated private directory writable by the service account. `no-sandbox: true` is an explicit deployment exception, not the default; prefer running Chromium as an unprivileged account with its sandbox enabled.

Set the existing CPA management key and client API key separately. Open **Muse.ai** in the plugin menu, or go directly to:

```text
/v8/resource/plugins/museai/index
```

The HTML can load without authentication. All account, probe, task, media, and extension operations require the normal management key at `/v8/management/plugins/museai/...`. No Muse operation is added under deprecated `/v0/management` routes.

The manager supports Simplified Chinese and English. When embedded in the same-origin CPA management center, it follows the center's selected language without reloading local state, clearing import fields, or contacting Muse.ai. Direct navigation uses the saved `cli-proxy-language` preference, then the browser language; add `?lang=zh-CN` or `?lang=en` to explicitly select a language. Chinese locales use Simplified Chinese; other locales fall back to English. Only language preferences are read from browser storage, never management keys.

## Import and manage credentials

1. Sign in to `https://muse.ai/` in your own browser.
2. Unlock the Muse manager using the CPA **management** key.
3. Download the cookie importer ZIP; extract it and load the directory using Chrome's **Extensions → Developer mode → Load unpacked**.
4. Open the extension and enter the exact CPA origin (HTTPS, or HTTP on localhost/127.0.0.1), management key, and an optional account label/name/proxy.
5. Click Import. Permission for the chosen CPA host is requested at this explicit action. Only Muse.ai cookies are read, including HttpOnly values and actual expiry times. Cookies go directly to that CPA origin; redirects are rejected.
6. Reload the manager's account list. **Probe** / **Refresh cookies** explicitly contact Muse; import/listing alone do not.

The extension stores neither the key nor cookies. Chrome host permissions are host-wide rather than port-wide; the actual upload remains pinned to the chosen origin. Conflicting same-name cookies on different paths cannot be represented by the credential map and are rejected rather than silently dropped.

Manual import accepts a Cookie header or a JSON name/value object, plus an optional `cookies_exp` map of Unix seconds. Required names are `hatch_sess`, `hatch_gw`, `hatch_vml`, and `hatch_native_auth_device`. Cookie names/values never appear in list responses. An explicit existing name `museai-key-*.json` replaces that credential; leave the name blank to create a new one.

CPA owns credential persistence, account selection, disable/delete, and refresh scheduling. The plugin does not maintain a second account store or rotate accounts internally. Refresh calls `/api/session` through the host transport, requires an assigned workspace, and applies actual `Set-Cookie` expiry/deletion changes. Browser-returned cookies are saved after both successful and interrupted generations when available. Reimport fresh cookies when authentication fails; a 403 is reported as access denial, not assumed expiry.

The host stamps `_plugin_owner` on imported credential files. Muse preserves this field in runtime auth metadata and across refresh/reimport so account-bound Host HTTP calls retain the host's plugin ownership checks.

The management page keeps its key only in memory. Lock clears account/import views and aborts pending page requests. Listing local browser/task/media state does not launch a browser or generate content. CPA's background credential refresher is separate from page actions.

## Models and protocols

| Model | Endpoints | Behavior |
| --- | --- | --- |
| `museai/chat` | `/v1/chat/completions`, `/v1/responses` | Text, ordinary/streaming, complete role-labelled conversation history. |
| `museai/image` | `/v1/images/generations`, `/v1/images/edits` | Text-to-image or one uploaded reference image. JSON base64 data URI and multipart are supported. |
| `museai/video` | `/v1/videos`, `/openai/v1/videos` | Asynchronous text-to-video / first-frame image-to-video, retrieval and content download. |

Models appear after an enabled Muse credential is registered. They are capability names, not claims about the underlying foundation model. Multiturn chat is a role-labelled transcript in a new isolated Muse thread, not native server-side Responses conversation state.

Unsupported inputs fail explicitly: structured tools/tool history, `previous_response_id`, image/audio/file chat parts, enforced JSON schemas, masks, more than one reference image, or `n > 1`. Arbitrary upstream HTTP calls and token counting return 501. The plugin does not fabricate token usage. Requested media size, aspect ratio, and duration are natural-language instructions to Muse, not guaranteed native controls.

Reference images must be actual uploaded bytes or a base64 image data URI. The plugin never fetches arbitrary reference URLs or local file paths. Image `response_format: b64_json` returns base64 bytes; `url` returns a data URI, not a public unauthenticated file endpoint. Generated files are also available through authenticated manager downloads.

Example requests (set `CPA_BASE_URL` and the client `CPA_API_KEY`):

```bash
curl "$CPA_BASE_URL/v1/chat/completions" \
  -H "Authorization: Bearer $CPA_API_KEY" -H 'Content-Type: application/json' \
  -d '{"model":"museai/chat","messages":[{"role":"user","content":"Hello"}],"stream":false}'

curl -N "$CPA_BASE_URL/v1/responses" \
  -H "Authorization: Bearer $CPA_API_KEY" -H 'Content-Type: application/json' \
  -d '{"model":"museai/chat","input":"Hello","stream":true}'

curl "$CPA_BASE_URL/v1/images/edits" \
  -H "Authorization: Bearer $CPA_API_KEY" \
  -F model=museai/image -F 'prompt=Make the background blue' -F image=@reference.png

curl "$CPA_BASE_URL/v1/videos" \
  -H "Authorization: Bearer $CPA_API_KEY" \
  -F model=museai/video -F 'prompt=Animate this first frame' -F input_reference=@reference.png

# Preserve the entire returned ID, including the cpa-video. prefix.
curl "$CPA_BASE_URL/v1/videos/$VIDEO_ID" -H "Authorization: Bearer $CPA_API_KEY"
curl "$CPA_BASE_URL/v1/videos/$VIDEO_ID/content" -H "Authorization: Bearer $CPA_API_KEY" -o result.webm
```

## Completion, cancellation, and recovery

- Operations for one selected account serialize; different accounts use separate profiles/processes. No request is sent to the user's existing main conversation.
- Submission confirmation compares whitespace-normalized rendered user text; multiline history remains unchanged in the submitted prompt even when the page collapses its line breaks.
- Ordinary and streaming chat report success only after observed completion. Chat streams end with one stop chunk and `[DONE]`; Responses streams emit `response.completed`. Rewritten already-streamed text or approval requirements fail rather than fabricate completion.
- Completion requires an observed stop-button transition or explicit terminal evidence, followed by stable output. Already-completed video recovery additionally accepts decoded finite-duration video metadata and verifies the selected video's actual container/MIME bytes. Absence of Stop alone never means success.
- Image results exclude prior attachments and upload previews; video results exclude poster/cover images. The selected DOM source is fetched within the owned browser, not by a host-side URL downloader.
- There is no fixed generation/network deadline after acquisition. Client cancellation crosses the C ABI, cancels queued work before submission, and attempts to stop only the owned page. A submitted or ambiguous request is never blindly retried or rotated to another account. Browser shutdown cannot guarantee that Muse has canceled remote billing; inspect the recorded thread when a result is unconfirmed.
- Video creation accepts a durable local task. Its observer belongs to plugin lifetime, so disconnecting a later status/content request does not resubmit or cancel that accepted generation.
- Returned video IDs bind the model, original CPA AuthID, and provider task ID. Retrieval/download remain pinned across CPA restart; deleting/disabling the original credential does not rotate the task to a different account.
- `tasks.json` and media bytes are private, atomically written local state. Keep `data-dir` across restarts. In-progress tasks with a recorded thread resume **observation only** when queried; they never click Send again. A task lacking a recoverable thread after restart becomes failed/unconfirmed rather than regenerating.
- Task inspection is local; deletion removes a local record, not the upstream thread. Active tasks cannot be deleted. Media deletion removes the local file. Account filters enforce stored ownership.
- Plugin disable/reconfigure/unload cancels its work and closes its owned Chromium targets/processes/profiles only. It never attaches to or cleans up other browser sessions.
- The host ends in-flight streams on disable/unload even after native callbacks are closed. Cancellation carries a non-retriable error, never a synthetic successful terminal event. Management configuration changes reload asynchronously; wait for the plugin's `registered` and `effective_enabled` state before sending new work after reenable. `/v1/models` alone is not a readiness check.
- Muse approval dialogs are reported as `approval_required`; the plugin never clicks email, purchase, payment, or other approval controls.

## Transport and observability

The browser uses CPA's resolved request/account/global proxy policy, with explicit `direct` overriding inherited proxy settings. HTTP/HTTPS/SOCKS5/SOCKS5h proxies use an owned loopback bridge; credentials do not enter Chromium command-line arguments. Unsupported host custom transports fail closed. Chromium's direct QUIC and non-proxied WebRTC paths are disabled.

Session renewal uses `HostHTTPClient`; Chromium HTTPS/WSS traffic does not. CPA request logs therefore do not represent full raw browser upstream traffic. Error messages and manager views omit cookies, keys, credential-bearing URLs, and upstream response bodies. Management requests are excluded from proxy request/error logging.

## Verification and reference

From `providers/museai`:

```bash
MUSEAI_TEST_CHROMIUM='/Applications/Google Chrome.app/Contents/MacOS/Google Chrome' go test -race ./...
```

On Linux, substitute your installed Chromium path. Fixtures run actual owned Chromium/CDP against loopback servers, not Muse.ai; an explicitly configured missing browser fails rather than skips. Tests cover stream terminal/delta integrity, history boundaries, cancellation/isolation, authenticated proxy transport, rotated cookies, approval rejection, exact image/video selection, and durable task/media ownership. Host tests cover v8 routes, cancellation ownership, no-retry propagation, and registered video pinning.

Real Muse login, permissions, quota consumption, site DOM stability, and actual generated quality require authorized post-deployment acceptance. Very fast upstream replies without observed Stop or explicit terminal evidence remain unconfirmed until cancellation; this is intentional rather than false completion.

Browser selectors and cookie/session behavior are adapted from [czg86389-hub/muse2api](https://github.com/czg86389-hub/muse2api) at commit `42ee0393bb4063d9bba23a26d91a2101fb9450d3`. Its MIT notice is preserved in [LICENSE.muse2api](LICENSE.muse2api). The implementation deliberately does not copy the reference's active-stop success fallback, video-cover fallback, fixed generation deadlines, or cross-account page sweeping.
