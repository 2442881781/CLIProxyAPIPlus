package management

import (
	"net/http"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/registry"
	coreusage "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/usage"
)

// Feature: GET /v0/management/model-pressure
//
//   One row per client-facing model: exact in-flight gauge, 60s-window
//   rate/error/latency sums, and the supply side (serving vs cooling upstream
//   credentials, latest upstream-reported used-percent where the provider
//   emits it).

func setupModelPressureRouter(t *testing.T) *gin.Engine {
	t.Helper()
	gin.SetMode(gin.TestMode)
	r := gin.New()
	h := &Handler{}
	r.GET("/model-pressure", h.GetModelPressure)
	return r
}

func registerPressureClient(t *testing.T, provider, model string) string {
	t.Helper()
	clientID := "pressure-client-" + uuid.NewString()
	registry.GetGlobalRegistry().RegisterClient(clientID, provider, []*registry.ModelInfo{{ID: model}})
	t.Cleanup(func() { registry.GetGlobalRegistry().UnregisterClient(clientID) })
	return clientID
}

func suspendPressureClient(t *testing.T, clientID, model string) {
	t.Helper()
	reg := registry.GetGlobalRegistry()
	epoch := reg.ClientRegistrationEpoch(clientID)
	gen := reg.GetGeneration()
	ok := reg.ApplyClientModelProjections(clientID, epoch, gen, []registry.ClientModelProjection{{
		ModelID:       model,
		Suspended:     true,
		SuspendReason: "cooldown",
	}})
	if !ok {
		t.Fatalf("ApplyClientModelProjections(%q) returned false", clientID)
	}
}

func pressureRowFor(body map[string]any, model string) map[string]any {
	models, _ := body["models"].([]any)
	for _, raw := range models {
		row, _ := raw.(map[string]any)
		if row["model"] == model {
			return row
		}
	}
	return nil
}

func TestModelPressureEndpoint_Shape(t *testing.T) {
	// Given recorded pressure for two models
	// Then the response lists each with in_flight, rate fields, and supply counts
	// And window_seconds is present
	tracker := coreusage.NewModelPressureTracker()
	t.Cleanup(coreusage.SetDefaultModelPressureForTesting(tracker))

	modelA := "mp-model-a-" + uuid.NewString()
	modelB := "mp-model-b-" + uuid.NewString()
	registerPressureClient(t, "claude", modelA)

	scope := tracker.Begin(modelA)
	defer scope.End()
	tracker.Observe(modelA, coreusage.ModelPressureEvent{InputTokens: 60, OutputTokens: 30, TotalTokens: 90})
	tracker.Observe(modelB, coreusage.ModelPressureEvent{TotalTokens: 10, Failed: true})

	r := setupModelPressureRouter(t)
	rec, body := doReq(t, r, "GET", "/model-pressure", "")
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d body=%s", rec.Code, rec.Body.String())
	}
	if body["window_seconds"] != float64(60) {
		t.Fatalf("window_seconds = %v, want 60", body["window_seconds"])
	}

	rowA := pressureRowFor(body, modelA)
	if rowA == nil {
		t.Fatalf("model %q missing: %s", modelA, rec.Body.String())
	}
	if rowA["in_flight"] != float64(1) {
		t.Fatalf("in_flight = %v, want 1", rowA["in_flight"])
	}
	if rowA["serving_auths"] != float64(1) {
		t.Fatalf("serving_auths = %v, want 1", rowA["serving_auths"])
	}
	for _, field := range []string{"requests_per_second", "total_tokens_per_second", "error_rate", "avg_latency_ms"} {
		if _, ok := rowA[field]; !ok {
			t.Fatalf("row missing field %q: %v", field, rowA)
		}
	}

	rowB := pressureRowFor(body, modelB)
	if rowB == nil {
		t.Fatalf("model %q missing", modelB)
	}
	if rowB["in_flight"] != float64(0) {
		t.Fatalf("modelB in_flight = %v, want 0", rowB["in_flight"])
	}
	if rowB["error_rate"] != float64(1) {
		t.Fatalf("modelB error_rate = %v, want 1", rowB["error_rate"])
	}
}

func TestModelPressureEndpoint_SupplyCounts(t *testing.T) {
	// Given a model served by three auths, one cooling
	// Then serving_auths=2 and cooling_auths=1 come from the live projections
	tracker := coreusage.NewModelPressureTracker()
	t.Cleanup(coreusage.SetDefaultModelPressureForTesting(tracker))

	model := "mp-supply-" + uuid.NewString()
	registerPressureClient(t, "claude", model)
	registerPressureClient(t, "claude", model)
	cooling := registerPressureClient(t, "claude", model)
	suspendPressureClient(t, cooling, model)

	tracker.Observe(model, coreusage.ModelPressureEvent{TotalTokens: 1})

	r := setupModelPressureRouter(t)
	rec, body := doReq(t, r, "GET", "/model-pressure", "")
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d body=%s", rec.Code, rec.Body.String())
	}
	row := pressureRowFor(body, model)
	if row == nil {
		t.Fatalf("model %q missing: %s", model, rec.Body.String())
	}
	if row["serving_auths"] != float64(2) {
		t.Fatalf("serving_auths = %v, want 2", row["serving_auths"])
	}
	if row["suspended_auths"] != float64(1) {
		t.Fatalf("suspended_auths = %v, want 1", row["suspended_auths"])
	}
}

func TestModelPressureEndpoint_HidesIdleModels(t *testing.T) {
	// Given a registered model that nobody requested and a model with a
	// recent completed request
	// Then only the requested model is listed
	tracker := coreusage.NewModelPressureTracker()
	t.Cleanup(coreusage.SetDefaultModelPressureForTesting(tracker))

	idle := "mp-idle-" + uuid.NewString()
	registerPressureClient(t, "claude", idle)

	active := "mp-active-" + uuid.NewString()
	registerPressureClient(t, "claude", active)
	tracker.Observe(active, coreusage.ModelPressureEvent{TotalTokens: 5})

	r := setupModelPressureRouter(t)
	rec, body := doReq(t, r, "GET", "/model-pressure", "")
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d body=%s", rec.Code, rec.Body.String())
	}
	if row := pressureRowFor(body, idle); row != nil {
		t.Fatalf("idle registered model %q must not be listed: %v", idle, row)
	}
	if row := pressureRowFor(body, active); row == nil {
		t.Fatalf("model %q with recent activity missing: %s", active, rec.Body.String())
	}
}
