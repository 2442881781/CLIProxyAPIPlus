package browser

import (
	"context"
	"fmt"
	"net/http"
	"testing"
	"time"
)

func TestReadQuotaCapturesSubscriptionWithoutGenerating(t *testing.T) {
	engine, _ := fixtureEngine(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodPost {
			if r.Header.Get("Next-Action") != "changing-action-id" {
				t.Error("quota did not use the site's action")
			}
			w.Header().Set("Content-Type", "text/x-component")
			_, _ = fmt.Fprint(w, "0:{\"a\":\"$@1\"}\n1:{\"success\":true,\"subscription\":{\"tier\":{\"tierId\":\"hatch_free\",\"name\":\"Muse Free\"},\"usage\":{\"state\":\"METERED\",\"percentUsed\":4,\"resetsAt\":1791970781},\"statusSubtitle\":\"Weekly limit resets on Oct 14\",\"usageRowLabel\":\"Free plan\",\"topupBalance\":360000000,\"topupTotal\":360000000,\"topupRowValueLabel\":\"0% used (9B tokens left)\"}}\n")
			return
		}
		if r.URL.Path == "/unexpected-generation" {
			t.Error("quota submitted a generation")
		}
		w.Header().Set("Content-Type", "text/html")
		_, _ = fmt.Fprint(w, `<!doctype html><body><form><textarea></textarea><button aria-label="Send" onclick="fetch('/unexpected-generation')">Send</button></form><button aria-label="Settings" onpointerdown="if(event.isTrusted&&event.buttons===1)this.insertAdjacentHTML('afterend','<button role=menuitem onclick=openSettings()><span>Settings</span><kbd>Ctrl+,</kbd></button>')">Settings</button><script>async function openSettings(){document.body.insertAdjacentHTML('beforeend','<div role=dialog>Usage</div>');await fetch(location.pathname,{method:'POST',headers:{'Next-Action':'changing-action-id'},body:'[]'});}</script>`)
	}))
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	quota, errRead := engine.ReadQuota(ctx, Input{Cookies: []Cookie{{Name: "credential", Value: "private"}}})
	if errRead != nil {
		t.Fatal(errRead)
	}
	if quota.Usage == nil || quota.Usage.PercentUsed == nil || *quota.Usage.PercentUsed != 4 || quota.TopupRowValueLabel != "0% used (9B tokens left)" {
		t.Fatalf("quota changed the observed subscription: %+v", quota)
	}
}

func TestReadQuotaRejectsLoginInsteadOfEmptySuccess(t *testing.T) {
	engine, _ := fixtureEngine(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html")
		_, _ = fmt.Fprint(w, `<!doctype html><input autocomplete="email" placeholder="Mobile number or email">`)
	}))
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	_, errRead := engine.ReadQuota(ctx, Input{})
	err, ok := errRead.(*Error)
	if !ok || err.HTTPStatus != 401 || err.Submitted {
		t.Fatalf("login was not reported as authentication failure: %v", errRead)
	}
}

func TestReadQuotaCloseCancelsPendingObservation(t *testing.T) {
	requested := make(chan struct{})
	release := make(chan struct{})
	defer close(release)
	engine, _ := fixtureEngine(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodPost {
			select {
			case <-requested:
			default:
				close(requested)
			}
			select {
			case <-r.Context().Done():
			case <-release:
			}
			return
		}
		w.Header().Set("Content-Type", "text/html")
		_, _ = fmt.Fprint(w, `<!doctype html><button aria-label="Settings" onclick="this.insertAdjacentHTML('afterend','<button role=menuitem onclick=openSettings()>Settings</button>')">Settings</button><script>async function openSettings(){document.body.insertAdjacentHTML('beforeend','<div role=dialog>Usage</div>');await fetch(location.pathname,{method:'POST',headers:{'Next-Action':'changing-action-id'},body:'[]'});}</script>`)
	}))
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	done := make(chan error, 1)
	go func() { _, errRead := engine.ReadQuota(ctx, Input{}); done <- errRead }()
	select {
	case <-requested:
	case <-ctx.Done():
		t.Fatal("subscription query never started")
	}
	if errClose := engine.Close(); errClose != nil {
		t.Fatal(errClose)
	}
	select {
	case errRead := <-done:
		err, ok := errRead.(*Error)
		if !ok || err.HTTPStatus < 400 || err.Submitted {
			t.Fatalf("closed observer became a success: %v", errRead)
		}
	case <-ctx.Done():
		t.Fatal("closing browser did not release quota observer")
	}
}
