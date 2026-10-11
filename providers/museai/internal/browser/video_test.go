package browser

import (
	"bytes"
	"context"
	"fmt"
	"net/http"
	"sync/atomic"
	"testing"
	"time"
)

func TestResumeUsesVideoNodeNotPoster(t *testing.T) {
	// Chromium records its own local canvas, producing real encoded video bytes.
	// No terminal attribute is supplied: Resume must recognize decoded metadata.
	engine, server := fixtureEngine(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/unexpected-submit" {
			t.Error("Resume resubmitted")
		}
		w.Header().Set("Content-Type", "text/html")
		_, _ = fmt.Fprint(w, `<!doctype html><body><main><div data-message-role="user">old</div><div data-message-role="assistant"><img src="data:image/png;base64,b2xk"></div><div data-message-role="user">persisted</div><div data-message-role="assistant"><img src="data:image/png;base64,Y292ZXI="><video id="clip" muted></video></div></main><form><textarea></textarea><button type="button" aria-label="Send" onclick="fetch('/unexpected-submit')">Send</button></form><script>
const canvas=document.createElement('canvas');canvas.width=32;canvas.height=32;const stream=canvas.captureStream(10);const recorder=new MediaRecorder(stream,{mimeType:'video/webm'});const chunks=[];recorder.ondataavailable=e=>chunks.push(e.data);recorder.onstop=()=>{const video=document.querySelector('#clip');video.onloadedmetadata=()=>{if(!Number.isFinite(video.duration))video.currentTime=1e6;};video.onseeked=()=>{video.onseeked=null;video.currentTime=0;video.play();};video.src=URL.createObjectURL(new Blob(chunks,{type:'video/webm'}));stream.getTracks().forEach(t=>t.stop());};recorder.start();let n=0;const paint=setInterval(()=>{const c=canvas.getContext('2d');c.fillStyle=n++%2?'red':'blue';c.fillRect(0,0,32,32);},30);setTimeout(()=>{clearInterval(paint);recorder.stop();},400);
</script>`)
	}))
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	result, err := engine.Resume(ctx, Input{Kind: "video", ThreadURL: server.URL + "/thread/clip"}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(result.Media) != 1 || result.Media[0].MIME != "video/webm" || !validMedia(result.Media[0].Data, "video/webm", "video") || bytes.Contains(result.Media[0].Data, []byte("cover")) {
		t.Fatal("Resume did not return real video node bytes")
	}
}
func TestApprovalNeverClicked(t *testing.T) {
	var clicked atomic.Bool
	engine, _ := fixtureEngine(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/approve" {
			clicked.Store(true)
		}
		w.Header().Set("Content-Type", "text/html")
		_, _ = fmt.Fprint(w, `<!doctype html><body><form><textarea></textarea><button type="button" aria-label="Send">Send</button></form><div role="dialog"><button onclick="fetch('/approve')">Approve</button></div>`)
	}))
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	_, err := engine.Run(ctx, Input{Kind: "chat", Prompt: "hello"}, nil)
	typed, ok := err.(*Error)
	if !ok || typed.Code != "approval_required" || typed.Submitted || clicked.Load() {
		t.Fatalf("approval was clicked or not rejected: %v", err)
	}
}
