## What's Changed

- Use CPA canonical session ID (`canonical_session_id`) when present.
- Fallback to inbound session headers or content-based hashing if the canonical session ID is missing.

## Upgrade Notes

- Replace the old plugin binary with the new release binary.
- Restart CLIProxyAPI after replacing the plugin.
- Hard-refresh Management Center if the plugin page looks stale.

**Full Changelog**: https://github.com/massiveits/opencode-go-cliproxyapi/compare/v0.1.4...v0.1.5
