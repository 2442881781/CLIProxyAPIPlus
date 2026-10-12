package main

import (
	"encoding/json"
	"testing"

	"cpa-plugin-museai/internal/browser"
)

func TestNormalizeMuseQuotaObservedUnitsAndReset(t *testing.T) {
	var q browser.Quota
	if err := json.Unmarshal([]byte(`{"tier":{"tierId":"hatch_free","name":"Muse Free"},"usage":{"state":"METERED","percentUsed":4,"resetsAt":1791970781},"statusSubtitle":"Weekly limit resets on Oct 14","usageRowLabel":"Free plan","topupBalance":360000000,"topupTotal":360000000,"topupRowValueLabel":"0% used (9B tokens left)"}`), &q); err != nil {
		t.Fatal(err)
	}
	out, errNormalize := normalizeMuseQuota(q)
	if errNormalize != nil {
		t.Fatal(errNormalize)
	}
	buckets := out.Groups[0].Buckets
	if out.Subscription.Plan != "Free plan" || buckets[0].RemainingFraction != 0.96 || buckets[0].ResetTime != "2026-10-14T09:39:41Z" {
		t.Fatalf("incorrect weekly quota: %+v", out)
	}
	if buckets[1].RemainingFraction != 1 || buckets[1].ResetTime != "" || buckets[1].Description != "0% used (9B tokens left)" || len(out.Summary) != 0 {
		t.Fatal("top-up credits were converted to invented tokens or a reset time")
	}
}

func TestNormalizeMuseQuotaRejectsUnknownInsteadOfZero(t *testing.T) {
	for _, raw := range []string{
		`{}`, `{"usage":{"state":"UNKNOWN"}}`, `{"usage":{"state":"METERED"}}`,
		`{"usage":{"state":"METERED","percentUsed":-1}}`, `{"usage":{"state":"METERED","percentUsed":101}}`,
		`{"usage":{"state":"METERED","percentUsed":0},"topupTotal":100}`,
		`{"usage":{"state":"METERED","percentUsed":0},"topupTotal":100,"topupBalance":101}`,
	} {
		var q browser.Quota
		if err := json.Unmarshal([]byte(raw), &q); err != nil {
			t.Fatal(err)
		}
		if _, errNormalize := normalizeMuseQuota(q); errNormalize == nil {
			t.Fatalf("unconfirmed quota accepted: %s", raw)
		}
	}
	var q browser.Quota
	_ = json.Unmarshal([]byte(`{"usage":{"state":"METERED","percentUsed":100}}`), &q)
	out, errNormalize := normalizeMuseQuota(q)
	if errNormalize != nil || out.Groups[0].Buckets[0].RemainingFraction != 0 || out.Groups[0].Buckets[0].ResetTime != "" {
		t.Fatal("exhausted quota or missing reset was misrepresented")
	}
}

func TestNormalizeMuseQuotaUnlimitedAndUnspecifiedPeriod(t *testing.T) {
	var q browser.Quota
	if err := json.Unmarshal([]byte(`{"tier":{"name":"Muse"},"usage":{"state":"UNLIMITED"}}`), &q); err != nil {
		t.Fatal(err)
	}
	out, errNormalize := normalizeMuseQuota(q)
	if errNormalize != nil || !out.Unlimited || len(out.Groups) != 0 {
		t.Fatal("unlimited usage became an invented percentage")
	}
	if err := json.Unmarshal([]byte(`{"usage":{"state":"METERED","percentUsed":0}}`), &q); err != nil {
		t.Fatal(err)
	}
	out, errNormalize = normalizeMuseQuota(q)
	if errNormalize != nil || out.Groups[0].Buckets[0].Window != "subscription" {
		t.Fatal("unknown period became a weekly limit")
	}
}
