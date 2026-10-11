package browser

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/binary"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"
)

// These tests use an owned Chromium and a loopback fixture, never muse.ai.
// Set MUSEAI_TEST_CHROMIUM to select the executable in CI.
func fixtureEngine(t *testing.T, handler http.Handler) (*Engine, *httptest.Server) {
	t.Helper()
	server := httptest.NewServer(handler)
	previous := museOrigin
	museOrigin = server.URL
	t.Cleanup(func() { museOrigin = previous; server.Close() })
	engine, err := New(Config{Executable: os.Getenv("MUSEAI_TEST_CHROMIUM"), DataDir: t.TempDir(), NoSandbox: true, PollInterval: 20 * time.Millisecond})
	if err != nil {
		if os.Getenv("MUSEAI_TEST_CHROMIUM") != "" {
			t.Fatal("explicit owned Chromium configuration failed: ", err)
		}
		t.Skip("owned Chromium not available: ", err)
	}
	t.Cleanup(func() { _ = engine.Close() })
	return engine, server
}

const fixturePage = `<!doctype html><html><body><main id="messages"><div data-message-role="assistant"><img src="data:image/png;base64,aGlzdG9yeQ==">history</div></main><form><textarea></textarea><input type="file" multiple><button type="button" aria-label="Send">Send</button></form><script>
const form=document.querySelector('form'),messages=document.querySelector('#messages'),button=document.querySelector('button');
document.querySelector('input').onchange=()=>{const files=document.querySelector('input').files;for(const f of files){const b=document.createElement('button');b.type='button';b.setAttribute('aria-label','Remove attachment');form.append(b);}}
button.onclick=()=>{window.submissions=(window.submissions||0)+1;const prompt=document.querySelector('textarea').value;const user=document.createElement('div');user.dataset.messageRole='user';user.textContent=prompt;messages.append(user);history.replaceState(null,'','/thread/created?secret=must-not-persist#fragment');document.cookie='rotated=fresh; Path=/';const stop=document.createElement('button');stop.dataset.testid='hatch-composer-stop-button';stop.textContent='Stop';form.append(stop);stop.onclick=()=>{window.stopped=true;stop.remove();};const a=document.createElement('div');a.dataset.messageRole='assistant';messages.append(a);if(prompt==='cancel')return;setTimeout(()=>a.textContent='Hello',40);setTimeout(()=>a.textContent='Hello world',80);setTimeout(()=>{a.dataset.messageStatus='completed';stop.remove();},120);};
</script></body></html>`

func TestRunOwnedFixture(t *testing.T) {
	engine, server := fixtureEngine(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html")
		_, _ = fmt.Fprint(w, fixturePage)
	}))
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	var events []Event
	result, err := engine.Run(ctx, Input{Kind: "chat", Prompt: "hello\n\n[user]\nsecond question", ProxyURL: "direct", Cookies: []Cookie{{Name: "credential", Value: "private"}}}, func(e Event) error { events = append(events, e); return nil })
	if err != nil {
		t.Fatal(err)
	}
	if result.Text != "Hello world" || result.ThreadURL != server.URL+"/thread/created" {
		t.Fatalf("unexpected result text/thread: %q %q", result.Text, result.ThreadURL)
	}
	if len(events) == 0 || !events[len(events)-1].Completed {
		t.Fatal("completion event missing")
	}
	early := false
	for _, e := range events {
		if e.ThreadURL != "" && !e.Completed {
			early = true
		}
		if e.Text != "" && !strings.HasPrefix(result.Text, e.Text) {
			t.Fatal("non-cumulative text")
		}
	}
	if !early {
		t.Fatal("durable thread not emitted before completion")
	}
	rotated := false
	for _, c := range result.Cookies {
		if c.Name == "rotated" && c.Value == "fresh" {
			rotated = true
		}
	}
	if !rotated {
		t.Fatal("rotated cookie not returned")
	}
}
func TestResumeDoesNotSubmitOrCollectHistory(t *testing.T) {
	png, _ := base64.StdEncoding.DecodeString("iVBORw0KGgoAAAANSUhEUgAAAAEAAAABCAYAAAAfFcSJAAAADUlEQVR42mP8z8BQDwAEhQGAhKmMIQAAAABJRU5ErkJggg==")
	engine, server := fixtureEngine(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/generated" {
			w.Header().Set("Content-Type", "image/png")
			_, _ = w.Write(png)
			return
		}
		w.Header().Set("Content-Type", "text/html")
		_, _ = fmt.Fprint(w, `<!doctype html><body><main><div data-message-role="user">old request</div><div data-message-role="assistant"><img src="data:image/png;base64,aGlzdG9yeQ=="></div><div data-message-role="user">persisted request</div><div data-message-role="assistant" data-message-status="completed">generated<img src="/generated"></div></main><form><textarea></textarea><button type="button" aria-label="Send" onclick="fetch('/unexpected-submit')">Send</button></form>`)
		if r.URL.Path == "/unexpected-submit" {
			t.Error("Resume submitted")
		}
	}))
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	result, err := engine.Resume(ctx, Input{Kind: "image", ThreadURL: server.URL + "/thread/persisted", Prompt: "DO NOT SEND"}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(result.Media) != 1 || !bytes.Equal(result.Media[0].Data, png) {
		t.Fatal("Resume did not select exact latest-turn media")
	}
}
func TestCancellationReadsCookiesAndCloseWakesRun(t *testing.T) {
	engine, _ := fixtureEngine(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html")
		_, _ = fmt.Fprint(w, fixturePage)
	}))
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	started := make(chan struct{})
	done := make(chan struct {
		result Result
		err    error
	}, 1)
	go func() {
		result, err := engine.Run(ctx, Input{Kind: "chat", Prompt: "cancel"}, func(e Event) error {
			if e.ThreadURL != "" {
				select {
				case <-started:
				default:
					close(started)
				}
			}
			return nil
		})
		done <- struct {
			result Result
			err    error
		}{result, err}
	}()
	select {
	case <-started:
	case <-ctx.Done():
		t.Fatal("fixture submission not observed")
	}
	if err := engine.Close(); err != nil {
		t.Fatal(err)
	}
	var output struct {
		result Result
		err    error
	}
	select {
	case output = <-done:
	case <-ctx.Done():
		t.Fatal("Close did not wake Run")
	}
	engineErr, ok := output.err.(*Error)
	if !ok || !engineErr.Submitted || engineErr.Code != "canceled" {
		t.Fatalf("unexpected cancellation: %v", output.err)
	}
	rotated := false
	for _, c := range output.result.Cookies {
		if c.Name == "rotated" && c.Value == "fresh" {
			rotated = true
		}
	}
	if !rotated {
		t.Fatal("cancellation lost rotated cookies")
	}
}
func TestMediaAndThreadInvariants(t *testing.T) {
	if validMedia([]byte("cover image"), "video/mp4", "video") {
		t.Fatal("cover accepted as video")
	}
	imageContainer := make([]byte, 24)
	binary.BigEndian.PutUint32(imageContainer, 24)
	copy(imageContainer[4:], "ftypavif")
	copy(imageContainer[16:], "mif1isom")
	if validMedia(imageContainer, "video/mp4", "video") {
		t.Fatal("AVIF accepted as video")
	}
	for _, raw := range []string{"http://127.0.0.1/thread/id", "https://user:pass@muse.ai/thread/id", "https://muse.ai/thread/new", "https://muse.ai/thread/%2e%2e", "https://muse.ai/thread/id/child"} {
		if _, err := threadURL(raw); err == nil {
			t.Fatal("unsafe thread accepted")
		}
	}
	normalized, err := threadURL("https://muse.ai/thread/id?secret=private#token")
	if err != nil || normalized != "https://muse.ai/thread/id" {
		t.Fatal("thread normalization failed")
	}
}
