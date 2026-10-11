package pluginabi

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestEnvelopeRoundTrip(t *testing.T) {
	payload := json.RawMessage(`{"name":"example"}`)
	env := Envelope{
		OK:     true,
		Result: payload,
	}

	raw, errMarshal := json.Marshal(env)
	if errMarshal != nil {
		t.Fatalf("marshal envelope: %v", errMarshal)
	}

	var decoded Envelope
	if errUnmarshal := json.Unmarshal(raw, &decoded); errUnmarshal != nil {
		t.Fatalf("unmarshal envelope: %v", errUnmarshal)
	}
	if !decoded.OK || string(decoded.Result) != string(payload) {
		t.Fatalf("decoded envelope = %#v, want ok payload", decoded)
	}
}

func TestErrorInterfaceAndConstructor(t *testing.T) {
	var _ error = (*Error)(nil)
	var _ interface{ StatusCode() int } = (*Error)(nil)

	err := NewError("insufficient_quota", "plan limit reached", 403)
	if err == nil {
		t.Fatal("NewError returned nil")
	}
	if err.Error() != "plan limit reached" {
		t.Fatalf("err.Error() = %q, want plan limit reached", err.Error())
	}
	if err.StatusCode() != 403 {
		t.Fatalf("err.StatusCode() = %d, want 403", err.StatusCode())
	}

	envBytes, errEnv := NewErrorEnvelope("rate_limit_exceeded", "too many requests", 429)
	if errEnv != nil {
		t.Fatalf("NewErrorEnvelope() error = %v", errEnv)
	}
	var env Envelope
	if errUnmarshal := json.Unmarshal(envBytes, &env); errUnmarshal != nil {
		t.Fatalf("unmarshal error envelope: %v", errUnmarshal)
	}
	if env.OK {
		t.Fatalf("env.OK = true, want false")
	}
	if env.Error == nil {
		t.Fatal("env.Error = nil, want non-nil")
	}
	if env.Error.Code != "rate_limit_exceeded" || env.Error.HTTPStatus != 429 {
		t.Fatalf("unexpected error payload: %+v", env.Error)
	}

	// Test zero / omitted status
	defaultErr := NewError("server_error", "unexpected error")
	if defaultErr.StatusCode() != 0 {
		t.Fatalf("defaultErr.StatusCode() = %d, want 0", defaultErr.StatusCode())
	}
	defaultEnvBytes, errDefaultEnv := NewErrorEnvelope("server_error", "unexpected error")
	if errDefaultEnv != nil {
		t.Fatalf("NewErrorEnvelope() error = %v", errDefaultEnv)
	}
	if strings.Contains(string(defaultEnvBytes), "http_status") {
		t.Fatalf("default envelope JSON unexpectedly contained http_status: %s", string(defaultEnvBytes))
	}
}
