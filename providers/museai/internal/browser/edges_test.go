package browser

import (
	"bytes"
	"context"
	"encoding/base64"
	"fmt"
	"net/http"
	"testing"
	"time"
)

func TestRunReferenceExcludesUserPreview(t *testing.T) {
	png, _ := base64.StdEncoding.DecodeString("iVBORw0KGgoAAAANSUhEUgAAAAEAAAABCAYAAAAfFcSJAAAADUlEQVR42mP8z8BQDwAEhQGAhKmMIQAAAABJRU5ErkJggg==")
	const page = `<!doctype html><body><main></main><form><textarea></textarea><input type="file" multiple><button type="button" aria-label="Send">Send</button></form><script>
const f=document.querySelector('form'),i=document.querySelector('input'),m=document.querySelector('main');
i.onchange=()=>{for(const file of i.files){const img=document.createElement('img');img.src=URL.createObjectURL(file);f.append(img);const b=document.createElement('button');b.type='button';b.setAttribute('aria-label','Remove attachment');f.append(b);}};
document.querySelector('button').onclick=()=>{const u=document.createElement('div');u.dataset.messageRole='user';u.textContent=document.querySelector('textarea').value;const img=f.querySelector('img');if(img)u.append(img);m.append(u);history.replaceState(null,'','/thread/reference');const stop=document.createElement('button');stop.dataset.testid='hatch-composer-stop-button';f.append(stop);setTimeout(()=>{const a=document.createElement('div');a.dataset.messageRole='assistant';a.dataset.messageStatus='completed';a.innerHTML='<img src="/result.png">';m.append(a);stop.remove();},80);};</script>`
	engine, _ := fixtureEngine(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/result.png" {
			w.Header().Set("Content-Type", "image/png")
			_, _ = w.Write(png)
			return
		}
		w.Header().Set("Content-Type", "text/html")
		_, _ = fmt.Fprint(w, page)
	}))
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	result, err := engine.Run(ctx, Input{Kind: "image", Prompt: "make image", References: []Reference{{Data: png, MIME: "image/png"}}}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(result.Media) != 1 || !bytes.Equal(result.Media[0].Data, png) {
		t.Fatal("reference preview selected instead of agent image")
	}
}

func TestRunImageSelectsAgentAttachmentSibling(t *testing.T) {
	png, _ := base64.StdEncoding.DecodeString("iVBORw0KGgoAAAANSUhEUgAAAAEAAAABCAYAAAAfFcSJAAAADUlEQVR42mP8z8BQDwAEhQGAhKmMIQAAAABJRU5ErkJggg==")
	const page = `<!doctype html><body><main><div class="group/msg"><div data-message-role="assistant">history</div><div data-testid="hatch-chat-attachment-presentation-old"><img src="/old.png"></div></div></main><form><textarea></textarea><button type="button" aria-label="Send">Send</button></form><script>
document.querySelector('button').onclick=()=>{
 const messages=document.querySelector('main'),userGroup=document.createElement('div');userGroup.className='group/msg ms-auto';
 const user=document.createElement('div');user.dataset.messageRole='user';user.textContent=document.querySelector('textarea').value;userGroup.append(user);
 userGroup.insertAdjacentHTML('beforeend','<div data-testid="hatch-chat-attachment-presentation-user"><img src="/input.png"></div>');messages.append(userGroup);
 const agentGroup=document.createElement('div');agentGroup.className='group/msg justify-start';agentGroup.innerHTML='<div data-message-role="assistant" data-message-status="completed">Generated image</div><div data-testid="hatch-chat-attachment-presentation-result"><img src="/result.png"></div>';messages.append(agentGroup);history.replaceState(null,'','/thread/image');
};</script>`
	engine, _ := fixtureEngine(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/result.png":
			w.Header().Set("Content-Type", "image/png")
			_, _ = w.Write(png)
		case "/old.png", "/input.png":
			w.Header().Set("Content-Type", "image/png")
			_, _ = w.Write([]byte("not generated image bytes"))
		default:
			w.Header().Set("Content-Type", "text/html")
			_, _ = fmt.Fprint(w, page)
		}
	}))
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	result, err := engine.Run(ctx, Input{Kind: "image", Prompt: "make image without references"}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(result.Media) != 1 || !bytes.Equal(result.Media[0].Data, png) {
		t.Fatal("agent attachment outside its text bubble was missed or user/history media was selected")
	}
}

func TestRunImageWithoutMediaDoesNotComplete(t *testing.T) {
	const page = `<!doctype html><body><main></main><form><textarea></textarea><button type="button" aria-label="Send">Send</button></form><script>
document.querySelector('button').onclick=()=>{const messages=document.querySelector('main'),user=document.createElement('div');user.dataset.messageRole='user';user.textContent=document.querySelector('textarea').value;messages.append(user);const agent=document.createElement('div');agent.dataset.messageRole='assistant';agent.dataset.messageStatus='completed';agent.textContent='No image was generated';messages.append(agent);};</script>`
	engine, _ := fixtureEngine(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html")
		_, _ = fmt.Fprint(w, page)
	}))
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	_, err := engine.Run(ctx, Input{Kind: "image", Prompt: "make image"}, func(e Event) error {
		if e.Completed {
			t.Error("text-only reply was reported as image success")
		}
		return nil
	})
	typed, ok := err.(*Error)
	if !ok || typed.Code != "missing_media" || typed.HTTPStatus != http.StatusBadGateway || !typed.Submitted {
		t.Fatalf("text-only image result did not fail without retry: %#v", err)
	}
}
func TestStopVisibleCannotComplete(t *testing.T) {
	const page = `<!doctype html><body><main></main><form><textarea></textarea><button type="button" aria-label="Send">Send</button></form><script>
document.querySelector('button').onclick=()=>{const m=document.querySelector('main'),u=document.createElement('div');u.dataset.messageRole='user';u.textContent=document.querySelector('textarea').value;m.append(u);history.replaceState(null,'','/thread/stop');const a=document.createElement('div');a.dataset.messageRole='assistant';a.dataset.messageStatus='completed';a.textContent='stable but still running';m.append(a);const b=document.createElement('button');b.dataset.testid='hatch-composer-stop-button';b.onclick=()=>b.remove();document.querySelector('form').append(b);};</script>`
	engine, _ := fixtureEngine(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html")
		_, _ = fmt.Fprint(w, page)
	}))
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	_, err := engine.Run(ctx, Input{Kind: "chat", Prompt: "wait"}, func(e Event) error {
		if e.Completed {
			t.Error("completed while stop visible")
		}
		if e.Text != "" {
			time.AfterFunc(250*time.Millisecond, cancel)
		}
		return nil
	})
	typed, ok := err.(*Error)
	if !ok || !typed.Submitted || typed.Code != "canceled" {
		t.Fatalf("expected submitted cancellation, got %v", err)
	}
}
func TestVideoCoverRejected(t *testing.T) {
	const page = `<!doctype html><body><main><div data-message-role="user">make video</div><div data-message-role="assistant" data-message-status="completed"><img src="data:image/png;base64,aGlzdG9yeQ=="></div></main><form><textarea></textarea></form>`
	engine, server := fixtureEngine(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html")
		_, _ = fmt.Fprint(w, page)
	}))
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	_, err := engine.Resume(ctx, Input{Kind: "video", ThreadURL: server.URL + "/thread/video"}, nil)
	typed, ok := err.(*Error)
	if !ok || typed.Code != "missing_media" || !typed.Submitted {
		t.Fatalf("video cover accepted or wrong error: %v", err)
	}
}
