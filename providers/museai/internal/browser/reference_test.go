package browser

import (
	"context"
	"encoding/base64"
	"fmt"
	"net/http"
	"strings"
	"testing"
	"time"
)

func TestReferenceUploadWaitsForProcessingAndUploading(t *testing.T) {
	processing, uploading := make(chan struct{}), make(chan struct{})
	processed, uploaded := make(chan struct{}), make(chan struct{})
	const page = `<!doctype html><body><main></main><div data-hatch-composer-root><div data-hatch-composer-chrome role="button"><div><div id="previews"></div><div><textarea></textarea><input type="file" multiple></div></div><button aria-label="Send">Send</button></div></div><script>
const previews=document.querySelector('#previews');
document.querySelector('input').onchange=async e=>{
 const file=e.target.files[0],preview=document.createElement('div');
 const img=document.createElement('img');img.src=URL.createObjectURL(file);preview.append(img);
 const remove=document.createElement('button');remove.setAttribute('aria-label','Remove attachment');preview.append(remove);
 const status=document.createElement('div');status.setAttribute('role','status');status.setAttribute('aria-label','Processing attachment');status.textContent='Processing';preview.append(status);previews.append(preview);
 await fetch('/processed');status.setAttribute('aria-label','Uploading attachment');status.textContent='Uploading';
 await fetch('/uploaded');status.remove();
 const hidden=document.createElement('div');hidden.hidden=true;hidden.setAttribute('role','status');hidden.setAttribute('aria-label','Processing attachment');previews.append(hidden);
 const alert=document.createElement('div');alert.hidden=true;alert.setAttribute('role','alert');alert.textContent='Stale attachment error';document.querySelector('[data-hatch-composer-root]').prepend(alert);
};</script>`
	engine, server := fixtureEngine(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/processed":
			close(processing)
			select {
			case <-processed:
			case <-r.Context().Done():
			}
		case "/uploaded":
			close(uploading)
			select {
			case <-uploaded:
			case <-r.Context().Done():
			}
		default:
			w.Header().Set("Content-Type", "text/html")
			_, _ = fmt.Fprint(w, page)
		}
	}))
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	s, operation, err := engine.open(ctx, Input{})
	if err != nil {
		t.Fatal(err)
	}
	defer s.close()
	if err = s.page.call(operation, "Page.navigate", map[string]any{"url": server.URL + "/thread/new"}, nil); err != nil {
		t.Fatal(err)
	}
	if err = s.waitReady(operation, engine.cfg.PollInterval); err != nil {
		t.Fatal(err)
	}
	png, _ := base64.StdEncoding.DecodeString("iVBORw0KGgoAAAANSUhEUgAAAAEAAAABCAYAAAAfFcSJAAAADUlEQVR42mP8z8BQDwAEhQGAhKmMIQAAAABJRU5ErkJggg==")
	done := make(chan error, 1)
	go func() {
		done <- s.upload(operation, []Reference{{Data: png, MIME: "image/png"}}, engine.cfg.PollInterval)
	}()
	for _, stage := range []struct {
		name    string
		started <-chan struct{}
		release chan struct{}
	}{{"processing", processing, processed}, {"uploading", uploading, uploaded}} {
		select {
		case <-stage.started:
		case <-ctx.Done():
			t.Fatalf("%s stage not observed", stage.name)
		}
		var state struct {
			Count   int  `json:"count"`
			Pending bool `json:"pending"`
		}
		if err = s.page.evaluate(operation, uploadStateJS, &state); err != nil {
			t.Fatal(err)
		}
		if state.Count != 1 || !state.Pending {
			t.Fatalf("%s preview was accepted as ready: %+v", stage.name, state)
		}
		select {
		case err = <-done:
			t.Fatalf("upload ended during %s: %v", stage.name, err)
		default:
		}
		close(stage.release)
	}
	select {
	case err = <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-ctx.Done():
		t.Fatal("completed reference remained pending")
	}
}

func TestRunRejectsReferenceFailureBeforeSubmission(t *testing.T) {
	for _, test := range []struct {
		name   string
		script string
	}{
		{"processing_rejected", `previews.innerHTML='';const alert=document.createElement('div');alert.setAttribute('role','alert');alert.textContent='PRIVATE attachment could not be processed';root.prepend(alert);`},
		{"reference_disappears_before_send", `document.querySelector('textarea').addEventListener('input',()=>{previews.innerHTML='';});`},
	} {
		t.Run(test.name, func(t *testing.T) {
			const page = `<!doctype html><body><main></main><div data-hatch-composer-root><div data-hatch-composer-chrome role="button"><div><div id="previews"></div><div><textarea></textarea><input type="file" multiple></div></div><button aria-label="Send" onclick="fetch('/submitted')">Send</button></div></div><script>
const root=document.querySelector('[data-hatch-composer-root]'),previews=document.querySelector('#previews');
document.querySelector('input').onchange=()=>{previews.innerHTML='<button aria-label="Remove attachment">Remove</button>';%s};</script>`
			engine, _ := fixtureEngine(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path == "/submitted" {
					t.Error("failed reference was submitted")
					return
				}
				w.Header().Set("Content-Type", "text/html")
				_, _ = fmt.Fprintf(w, page, test.script)
			}))
			ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
			defer cancel()
			png, _ := base64.StdEncoding.DecodeString("iVBORw0KGgoAAAANSUhEUgAAAAEAAAABCAYAAAAfFcSJAAAADUlEQVR42mP8z8BQDwAEhQGAhKmMIQAAAABJRU5ErkJggg==")
			_, err := engine.Run(ctx, Input{Kind: "image", Prompt: "make image", References: []Reference{{Data: png, MIME: "image/png"}}}, nil)
			typed, ok := err.(*Error)
			if !ok || typed.Code != "reference_upload_failed" || typed.HTTPStatus != http.StatusBadRequest || typed.Submitted {
				t.Fatalf("reference failure did not stop before submission: %v", err)
			}
			if strings.Contains(typed.Message, "PRIVATE") {
				t.Fatal("page error text leaked")
			}
		})
	}
}
