package util

import "testing"

func TestCookieHeadersAreFullyRedacted(t *testing.T) {
	for _, name := range []string{"Cookie", "Set-Cookie", " cookie ", "SET-COOKIE"} {
		if got := MaskSensitiveHeaderValue(name, "session=super-secret; refresh=another-secret; HttpOnly; Secure"); got != "[REDACTED]" {
			t.Fatalf("%s exposed credentials: %q", name, got)
		}
	}
}
