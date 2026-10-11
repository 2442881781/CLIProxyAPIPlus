package main

import (
	"bytes"
	"context"
	"encoding/json"
	"strings"
	"sync"
	"testing"

	"cpa-plugin-museai/internal/browser"
)

func TestResponsesStreamCompletionAndRewrite(t *testing.T) {
	var events []map[string]any
	encoder := newStreamEncoder("resp_test", "museai/chat", "openai-response", func(raw []byte) error {
		_, data, found := strings.Cut(string(raw), "\ndata: ")
		if !found {
			t.Fatalf("invalid event: %q", raw)
		}
		var event map[string]any
		if errDecode := json.Unmarshal([]byte(strings.TrimSpace(data)), &event); errDecode != nil {
			t.Fatal(errDecode)
		}
		events = append(events, event)
		return nil
	})
	for _, text := range []string{"Hello", "Hello", "Hello 世界"} {
		if errUpdate := encoder.update(text); errUpdate != nil {
			t.Fatal(errUpdate)
		}
	}
	if errComplete := encoder.complete("Hello 世界"); errComplete != nil {
		t.Fatal(errComplete)
	}
	var deltas strings.Builder
	completed := 0
	for i, event := range events {
		if event["sequence_number"] != float64(i) {
			t.Fatalf("non-monotonic sequence: %#v", event)
		}
		switch event["type"] {
		case "response.output_text.delta":
			deltas.WriteString(event["delta"].(string))
		case "response.completed":
			completed++
			response := event["response"].(map[string]any)
			if response["status"] != "completed" || response["output_text"] != "Hello 世界" {
				t.Fatalf("wrong terminal response: %#v", response)
			}
			if _, exists := response["usage"]; exists {
				t.Fatal("fabricated usage")
			}
		}
	}
	if deltas.String() != "Hello 世界" || completed != 1 {
		t.Fatalf("stream corrupted: text=%q completions=%d", deltas.String(), completed)
	}

	var failed bytes.Buffer
	rewritten := newStreamEncoder("resp_rewrite", "museai/chat", "openai-response", func(raw []byte) error { _, errWrite := failed.Write(raw); return errWrite })
	if errUpdate := rewritten.update("first version"); errUpdate != nil {
		t.Fatal(errUpdate)
	}
	errRewrite := rewritten.update("new version")
	if errRewrite == nil {
		t.Fatal("already-streamed text rewrite was silently accepted")
	}
	if errFail := rewritten.fail(errRewrite); errFail != nil {
		t.Fatal(errFail)
	}
	if !strings.Contains(failed.String(), "event: response.failed") || strings.Contains(failed.String(), "event: response.completed") {
		t.Fatal("rewrite falsely completed")
	}
}

func TestChatStreamExactDeltasAndTerminal(t *testing.T) {
	var output bytes.Buffer
	encoder := newStreamEncoder("chat_test", "museai/chat", "openai", func(raw []byte) error {
		_, errWrite := output.Write(append(append([]byte("data: "), raw...), []byte("\n\n")...))
		return errWrite
	})
	if errUpdate := encoder.update("a"); errUpdate != nil {
		t.Fatal(errUpdate)
	}
	if errUpdate := encoder.update("a"); errUpdate != nil {
		t.Fatal(errUpdate)
	}
	if errComplete := encoder.complete("ab"); errComplete != nil {
		t.Fatal(errComplete)
	}
	// The Chat HTTP handler owns SSE framing and the final DONE marker.
	_, _ = output.WriteString("data: [DONE]\n\n")
	var deltas strings.Builder
	finishes, done := 0, 0
	for _, line := range strings.Split(output.String(), "\n") {
		if !strings.HasPrefix(line, "data: ") {
			continue
		}
		data := strings.TrimPrefix(line, "data: ")
		if data == "[DONE]" {
			done++
			continue
		}
		var event struct {
			Choices []struct {
				Delta struct {
					Content string `json:"content"`
				} `json:"delta"`
				FinishReason *string `json:"finish_reason"`
			} `json:"choices"`
		}
		if errDecode := json.Unmarshal([]byte(data), &event); errDecode != nil {
			t.Fatal(errDecode)
		}
		for _, choice := range event.Choices {
			deltas.WriteString(choice.Delta.Content)
			if choice.FinishReason != nil {
				if *choice.FinishReason != "stop" {
					t.Fatal("unexpected finish")
				}
				finishes++
			}
		}
	}
	if deltas.String() != "ab" || finishes != 1 || done != 1 {
		t.Fatalf("chat stream corrupted: text=%q stop=%d done=%d", deltas.String(), finishes, done)
	}
}

func TestConversationHistoryAndUnsupportedInputs(t *testing.T) {
	prompt, errPrompt := conversationPrompt([]byte(`{"messages":[{"role":"system","content":"Be concise"},{"role":"user","content":"first question"},{"role":"assistant","content":"first answer"},{"role":"user","content":[{"type":"text","text":"next question"}]}]}`), "openai")
	if errPrompt != nil {
		t.Fatal(errPrompt)
	}
	previous := -1
	for _, text := range []string{"[system]\nBe concise", "[user]\nfirst question", "[assistant]\nfirst answer", "[user]\nnext question"} {
		index := strings.Index(prompt, text)
		if index <= previous {
			t.Fatalf("lost or reordered history: %q", prompt)
		}
		previous = index
	}
	prompt, errPrompt = conversationPrompt([]byte(`{"instructions":"system context","input":[{"role":"user","content":[{"type":"input_text","text":"question"}]},{"role":"assistant","content":[{"type":"output_text","text":"answer"}]},{"role":"user","content":"continue"}]}`), "openai-response")
	if errPrompt != nil || !strings.Contains(prompt, "[system]\nsystem context") || !strings.HasSuffix(prompt, "[user]\ncontinue") {
		t.Fatalf("Responses history failed: %q %v", prompt, errPrompt)
	}
	for _, raw := range []string{
		`{"messages":[{"role":"user","content":"hi"}],"tools":[{"type":"function","function":{"name":"write"}}]}`,
		`{"messages":[{"role":"tool","content":"result"}]}`,
		`{"messages":[{"role":"user","content":[{"type":"image_url","image_url":{"url":"https://example.com/a.png"}}]}]}`,
		`{"messages":[{"role":"assistant","tool_calls":[{"id":"call_1"}],"content":""}]}`,
		`{"messages":[{"role":"user","content":"hi"}],"response_format":{"type":"json_schema"}}`,
	} {
		if _, errInvalid := conversationPrompt([]byte(raw), "openai"); errInvalid == nil {
			t.Fatalf("unsupported input accepted: %s", raw)
		}
	}
	if _, errInvalid := conversationPrompt([]byte(`{"input":"hi","previous_response_id":"resp_old"}`), "openai-response"); errInvalid == nil {
		t.Fatal("opaque history silently ignored")
	}
}

func TestDurableTaskRecoveryAndMediaOwnership(t *testing.T) {
	dir := t.TempDir()
	store, errOpen := openTaskStore(dir)
	if errOpen != nil {
		t.Fatal(errOpen)
	}
	record, errSave := store.saveMedia(browser.Media{Data: []byte("generated bytes"), MIME: "video/mp4"}, "video", "account-a")
	if errSave != nil {
		t.Fatal(errSave)
	}
	for _, task := range []videoTask{
		{ID: "recover", AuthID: "account-a", Model: "museai/video", Status: "in_progress", Submitted: true, ThreadURL: "https://muse.ai/thread/existing"},
		{ID: "unconfirmed", AuthID: "account-b", Model: "museai/video", Status: "queued"},
		{ID: "complete", AuthID: "account-a", Model: "museai/video", Status: "completed", Media: []mediaRecord{record}},
	} {
		if errPut := store.put(task); errPut != nil {
			t.Fatal(errPut)
		}
	}
	restarted, errOpen := openTaskStore(dir)
	if errOpen != nil {
		t.Fatal(errOpen)
	}
	task, exists := restarted.get("recover")
	if !exists || task.Status != "in_progress" || !task.Submitted || task.ThreadURL != "https://muse.ai/thread/existing" {
		t.Fatalf("lost resume state: %#v", task)
	}
	task, _ = restarted.get("unconfirmed")
	if task.Status != "failed" || !strings.Contains(task.Error, "not repeated") {
		t.Fatalf("stranded submission may repeat: %#v", task)
	}
	if _, _, errRead := restarted.readMedia(record.Name, "account-b"); errRead == nil {
		t.Fatal("cross-account media read accepted")
	}
	data, _, errRead := restarted.readMedia(record.Name, "account-a")
	if errRead != nil || string(data) != "generated bytes" {
		t.Fatalf("lost durable media: %q %v", data, errRead)
	}
	if errRemove := restarted.removeTask("recover", "account-b"); errRemove == nil {
		t.Fatal("cross-account task delete accepted")
	}
	if !restarted.start("recover") {
		t.Fatal("first observer not started")
	}
	if restarted.start("recover") {
		t.Fatal("duplicate observer started")
	}
	if errRemove := restarted.removeTask("recover", "account-a"); errRemove == nil {
		t.Fatal("active task removed")
	}
	restarted.finish("recover")
	if errRemove := restarted.removeTask("recover", "account-a"); errRemove != nil {
		t.Fatal(errRemove)
	}
	if errRemove := restarted.removeMedia(record.Name, "account-b"); errRemove == nil {
		t.Fatal("cross-account media delete accepted")
	}
	if errRemove := restarted.removeMedia(record.Name, "account-a"); errRemove != nil {
		t.Fatal(errRemove)
	}
	afterDelete, errOpen := openTaskStore(dir)
	if errOpen != nil {
		t.Fatal(errOpen)
	}
	if _, exists := afterDelete.get("recover"); exists {
		t.Fatal("deleted task restored on restart")
	}
	if _, _, errRead := afterDelete.readMedia(record.Name, "account-a"); errRead == nil {
		t.Fatal("deleted media restored on restart")
	}
}

func TestAccountQueueCancellationAndIsolation(t *testing.T) {
	p := &provider{gates: make(map[string]chan struct{})}
	unlock, errLock := p.lockAccount(context.Background(), "a")
	if errLock != nil {
		t.Fatal(errLock)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if unlockCanceled, errCanceled := p.lockAccount(ctx, "a"); errCanceled == nil {
		unlockCanceled()
		t.Fatal("canceled queued request acquired account")
	}
	other, errOther := p.lockAccount(context.Background(), "b")
	if errOther != nil {
		t.Fatal(errOther)
	}
	other()
	unlock()
	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		next, errNext := p.lockAccount(context.Background(), "a")
		if errNext != nil {
			t.Error(errNext)
			return
		}
		next()
	}()
	wg.Wait()
}
