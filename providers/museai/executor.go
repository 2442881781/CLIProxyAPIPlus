package main

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"time"

	"cpa-plugin-museai/internal/browser"
	"github.com/router-for-me/CLIProxyAPI/v8/sdk/pluginabi"
	"github.com/router-for-me/CLIProxyAPI/v8/sdk/pluginapi"
)

func (p *provider) execute(req executorRequest, stream bool) (any, error) {
	if req.AuthProvider != providerID {
		return nil, requestError("auth_mismatch", "Muse executor requires the selected Muse credential")
	}
	c, errCredential := decodeCredential(req.StorageJSON)
	if errCredential != nil {
		return nil, errCredential
	}
	if c.Disabled {
		return nil, requestError("auth_disabled", "Muse account is disabled")
	}
	if req.BrowserTransport == nil {
		return nil, requestError("transport_missing", "Host did not supply browser transport policy")
	}
	if req.BrowserTransport.Error != "" {
		return nil, requestError("unsupported_transport", req.BrowserTransport.Error)
	}
	if req.Format == "openai-image" || req.Format == "openai-video" {
		payload, errPayload := mediaPayload(req)
		if errPayload != nil {
			return nil, errPayload
		}
		req.Payload = payload
	}
	ctx, cancel, errCtx := p.scopedContext(req.CallbackID)
	if errCtx != nil {
		return nil, errCtx
	}
	if stream {
		if req.StreamID == "" {
			cancel()
			return nil, requestError("stream_missing", "Host stream ID is required")
		}
		if !p.begin() {
			cancel()
			return nil, requestError("plugin_stopped", "Muse plugin is stopping")
		}
		go func() {
			defer p.workers.Done()
			defer cancel()
			errRun := p.runStream(ctx, req, c)
			closeReq := map[string]any{"stream_id": req.StreamID}
			if errRun != nil {
				closeReq["error_detail"] = asPluginError(errRun)
			}
			_, _ = p.call(pluginabi.MethodHostStreamClose, closeReq)
		}()
		return map[string]any{"headers": http.Header{"Content-Type": {"text/event-stream"}, "Cache-Control": {"no-store"}}}, nil
	}
	defer cancel()
	if req.Format == "openai-video" {
		var op struct {
			Operation string `json:"operation"`
			ID        string `json:"id"`
		}
		if errDecode := json.Unmarshal(req.Payload, &op); errDecode != nil {
			return nil, errDecode
		}
		if op.Operation == "retrieve" || op.Operation == "content" {
			return p.retrieveVideo(ctx, req, c, op.Operation, op.ID)
		}
		return p.createVideo(ctx, req, c)
	}
	unlock, errLock := p.lockAccount(ctx, req.AuthID)
	if errLock != nil {
		return nil, errLock
	}
	defer unlock()
	c, errCredential = p.credentialByID(req.AuthID)
	if errCredential != nil {
		return nil, errCredential
	}
	if req.Format == "openai-image" {
		return p.image(ctx, req, c)
	}
	if req.Format != "openai" && req.Format != "openai-response" {
		return nil, requestError("unsupported_protocol", "Unsupported Muse conversation protocol")
	}
	prompt, errPrompt := conversationPrompt(req.Payload, req.Format)
	if errPrompt != nil {
		return nil, errPrompt
	}
	result, errRun := p.engine.Run(ctx, browserInput(req, c, prompt, "chat", nil), nil)
	p.syncCookies(req.AuthID, c, result.Cookies)
	if errRun != nil {
		return nil, errRun
	}
	id := "resp_" + newID()
	payload := chatResponse(id, req.Model, result.Text, time.Now().Unix())
	if req.Format == "openai-response" {
		payload = jsonBytes(responsesResponse(id, req.Model, result.Text, "completed", time.Now().Unix()))
	}
	return pluginapi.ExecutorResponse{Payload: payload, Headers: http.Header{"Content-Type": {"application/json"}}}, nil
}

func browserInput(req executorRequest, c credential, prompt, kind string, refs []browser.Reference) browser.Input {
	return browser.Input{AuthID: req.AuthID, ProxyURL: req.BrowserTransport.ProxyURL, Cookies: c.browserCookies(), Prompt: prompt, Kind: kind, References: refs}
}

func (p *provider) emit(req executorRequest, payload []byte) error {
	_, errCall := p.call(pluginabi.MethodHostStreamEmit, map[string]any{"stream_id": req.StreamID, "payload": payload})
	return errCall
}

func (p *provider) runStream(ctx context.Context, req executorRequest, c credential) error {
	if req.Format == "openai-video" {
		return requestError("unsupported_stream", "Video tasks use asynchronous creation and polling, not streaming")
	}
	unlock, errLock := p.lockAccount(ctx, req.AuthID)
	if errLock != nil {
		return errLock
	}
	defer unlock()
	var errCredential error
	c, errCredential = p.credentialByID(req.AuthID)
	if errCredential != nil {
		return errCredential
	}
	if req.Format == "openai-image" {
		response, errImage := p.image(ctx, req, c)
		if errImage != nil {
			return errImage
		}
		var decoded any
		if errDecode := json.Unmarshal(response.Payload, &decoded); errDecode != nil {
			return errDecode
		}
		return p.emit(req, []byte(fmt.Sprintf("event: image_generation.completed\ndata: %s\n\n", jsonBytes(decoded))))
	}
	prompt, errPrompt := conversationPrompt(req.Payload, req.Format)
	if errPrompt != nil {
		return errPrompt
	}
	encoder := newStreamEncoder("resp_"+newID(), req.Model, req.Format, func(payload []byte) error { return p.emit(req, payload) })
	result, errRun := p.engine.Run(ctx, browserInput(req, c, prompt, "chat", nil), func(event browser.Event) error { return encoder.update(event.Text) })
	p.syncCookies(req.AuthID, c, result.Cookies)
	if errRun != nil {
		if ctx.Err() == nil {
			_ = encoder.fail(errRun)
		}
		return errRun
	}
	return encoder.complete(result.Text)
}

type generationRequest struct {
	Prompt         string            `json:"prompt"`
	N              int               `json:"n"`
	Size           string            `json:"size"`
	Seconds        json.RawMessage   `json:"seconds"`
	Duration       json.RawMessage   `json:"duration"`
	AspectRatio    string            `json:"aspect_ratio"`
	ResponseFormat string            `json:"response_format"`
	ReferenceImage string            `json:"reference_image"`
	Image          json.RawMessage   `json:"image"`
	Images         []json.RawMessage `json:"images"`
	InputReference json.RawMessage   `json:"input_reference"`
	Mask           json.RawMessage   `json:"mask"`
}

func generationInput(raw []byte, kind string) (generationRequest, string, []browser.Reference, error) {
	var req generationRequest
	if errDecode := json.Unmarshal(raw, &req); errDecode != nil {
		return req, "", nil, requestError("invalid_request", "Invalid media request JSON")
	}
	if strings.TrimSpace(req.Prompt) == "" {
		return req, "", nil, requestError("missing_prompt", "Media prompt is required")
	}
	if req.N > 1 {
		return req, "", nil, requestError("unsupported_count", "Muse media currently accepts n=1 only")
	}
	if presentJSON(req.Mask) {
		return req, "", nil, requestError("unsupported_reference", "Muse does not support image masks")
	}
	if req.ResponseFormat != "" && req.ResponseFormat != "url" && req.ResponseFormat != "b64_json" {
		return req, "", nil, requestError("unsupported_format", "Media response_format must be url or b64_json")
	}
	var values []json.RawMessage
	if req.ReferenceImage != "" {
		values = append(values, jsonBytes(req.ReferenceImage))
	}
	if presentJSON(req.Image) {
		values = append(values, req.Image)
	}
	values = append(values, req.Images...)
	if presentJSON(req.InputReference) {
		values = append(values, req.InputReference)
	}
	if len(values) > 1 {
		return req, "", nil, requestError("unsupported_references", "Muse accepts one reference image per generation")
	}
	var refs []browser.Reference
	for _, value := range values {
		ref, errRef := referenceImage(value)
		if errRef != nil {
			return req, "", nil, errRef
		}
		refs = append(refs, ref)
	}
	prompt := "Generate an image for this request. Return the generated image attachment. "
	if kind == "video" {
		prompt = "Generate a video for this request. Return the actual video attachment, not a cover image. "
	}
	if len(refs) > 0 {
		prompt += "Use the attached reference image"
		if kind == "video" {
			prompt += " as the first frame"
		}
		prompt += ". "
	}
	prompt += req.Prompt
	if req.Size != "" {
		prompt += "\nRequested size: " + req.Size
	}
	if req.AspectRatio != "" {
		prompt += "\nRequested aspect ratio: " + req.AspectRatio
	}
	if kind == "video" {
		if seconds := secondsValue(req); seconds != "" {
			prompt += "\nRequested duration in seconds: " + seconds
		}
	}
	return req, prompt, refs, nil
}

func referenceImage(raw json.RawMessage) (browser.Reference, error) {
	var value string
	if json.Unmarshal(raw, &value) != nil {
		var object struct {
			URL      string          `json:"url"`
			ImageURL json.RawMessage `json:"image_url"`
		}
		if errDecode := json.Unmarshal(raw, &object); errDecode != nil {
			return browser.Reference{}, requestError("invalid_reference", "Reference image must be a data URI")
		}
		value = object.URL
		if value == "" && len(object.ImageURL) > 0 {
			if json.Unmarshal(object.ImageURL, &value) != nil {
				var nested struct {
					URL string `json:"url"`
				}
				_ = json.Unmarshal(object.ImageURL, &nested)
				value = nested.URL
			}
		}
	}
	meta, payload, found := strings.Cut(value, ",")
	if !found || !strings.HasPrefix(meta, "data:image/") || !strings.HasSuffix(meta, ";base64") {
		return browser.Reference{}, requestError("invalid_reference", "Use a base64 image data URI or multipart image upload; arbitrary URLs/files are not fetched")
	}
	if len(payload) > 32<<20 {
		return browser.Reference{}, requestError("reference_too_large", "Reference image exceeds 24 MiB")
	}
	mime := strings.TrimSuffix(strings.TrimPrefix(meta, "data:"), ";base64")
	if _, valid := mediaExtension(mime); !valid || !strings.HasPrefix(mime, "image/") {
		return browser.Reference{}, requestError("invalid_reference", "Unsupported reference image MIME")
	}
	data, errDecode := base64.StdEncoding.DecodeString(payload)
	if errDecode != nil || len(data) == 0 {
		return browser.Reference{}, requestError("invalid_reference", "Reference image base64 is invalid")
	}
	return browser.Reference{Data: data, MIME: mime}, nil
}

func secondsValue(req generationRequest) string {
	raw := req.Seconds
	if len(raw) == 0 {
		raw = req.Duration
	}
	var value string
	if json.Unmarshal(raw, &value) == nil {
		return value
	}
	if len(raw) > 0 && string(raw) != "null" {
		return string(raw)
	}
	return ""
}

func (p *provider) image(ctx context.Context, req executorRequest, c credential) (pluginapi.ExecutorResponse, error) {
	input, prompt, refs, errInput := generationInput(req.Payload, "image")
	if errInput != nil {
		return pluginapi.ExecutorResponse{}, errInput
	}
	result, errRun := p.engine.Run(ctx, browserInput(req, c, prompt, "image", refs), nil)
	p.syncCookies(req.AuthID, c, result.Cookies)
	if errRun != nil {
		return pluginapi.ExecutorResponse{}, errRun
	}
	var items []any
	for _, media := range result.Media {
		if _, errSave := p.store.saveMedia(media, "image", req.AuthID); errSave != nil {
			return pluginapi.ExecutorResponse{}, errSave
		}
		item := map[string]any{"revised_prompt": input.Prompt, "mime_type": media.MIME}
		b64 := base64.StdEncoding.EncodeToString(media.Data)
		if input.ResponseFormat == "url" {
			item["url"] = "data:" + media.MIME + ";base64," + b64
		} else {
			item["b64_json"] = b64
		}
		items = append(items, item)
	}
	if len(items) == 0 {
		return pluginapi.ExecutorResponse{}, requestError("missing_media", "Muse completed without a generated image")
	}
	return pluginapi.ExecutorResponse{Payload: jsonBytes(map[string]any{"created": time.Now().Unix(), "data": items}), Headers: http.Header{"Content-Type": {"application/json"}}}, nil
}

func (p *provider) createVideo(ctx context.Context, req executorRequest, c credential) (pluginapi.ExecutorResponse, error) {
	input, prompt, refs, errInput := generationInput(req.Payload, "video")
	if errInput != nil {
		return pluginapi.ExecutorResponse{}, errInput
	}
	if errCtx := ctx.Err(); errCtx != nil {
		return pluginapi.ExecutorResponse{}, errCtx
	}
	task := videoTask{ID: newID(), AuthID: req.AuthID, Model: req.Model, Status: "queued", CreatedAt: time.Now().Unix(), Seconds: secondsValue(input), Size: input.Size}
	if errStore := p.store.put(task); errStore != nil {
		return pluginapi.ExecutorResponse{}, errStore
	}
	if errStart := p.startVideo(task, req, c, prompt, refs, false); errStart != nil {
		task.Status, task.Error = "failed", asPluginError(errStart).Message
		_ = p.store.put(task)
		return pluginapi.ExecutorResponse{}, errStart
	}
	return pluginapi.ExecutorResponse{Payload: videoPayload(task), Headers: http.Header{"Content-Type": {"application/json"}}}, nil
}

func (p *provider) startVideo(task videoTask, req executorRequest, c credential, prompt string, refs []browser.Reference, resume bool) error {
	if !p.begin() {
		return requestError("plugin_stopped", "Muse plugin is stopping")
	}
	if !p.store.start(task.ID) {
		p.workers.Done()
		return nil
	}
	go func() {
		defer p.workers.Done()
		defer p.store.finish(task.ID)
		unlock, errLock := p.lockAccount(p.ctx, task.AuthID)
		if errLock != nil {
			task.Status, task.Error = "failed", asPluginError(errLock).Message
			_ = p.store.put(task)
			return
		}
		defer unlock()
		var errCredential error
		c, errCredential = p.credentialByID(task.AuthID)
		if errCredential != nil {
			task.Status, task.Error = "failed", asPluginError(errCredential).Message
			_ = p.store.put(task)
			return
		}
		task.Status = "in_progress"
		if errSave := p.store.put(task); errSave != nil {
			return
		}
		input := browserInput(req, c, prompt, "video", refs)
		input.ThreadURL = task.ThreadURL
		observe := func(event browser.Event) error {
			changed := false
			if event.Submitted && !task.Submitted {
				task.Submitted, changed = true, true
			}
			if event.ThreadURL != "" && task.ThreadURL != event.ThreadURL {
				task.ThreadURL, changed = event.ThreadURL, true
			}
			if changed {
				return p.store.put(task)
			}
			return nil
		}
		var result browser.Result
		var errRun error
		if resume {
			result, errRun = p.engine.Resume(p.ctx, input, observe)
		} else {
			result, errRun = p.engine.Run(p.ctx, input, observe)
		}
		p.syncCookies(task.AuthID, c, result.Cookies)
		if result.ThreadURL != "" {
			task.ThreadURL = result.ThreadURL
		}
		if errRun != nil {
			if p.ctx.Err() != nil && task.ThreadURL != "" {
				task.Status, task.Error = "in_progress", "Plugin stopped; query this task after restart to resume observation without resubmitting"
			} else {
				task.Status, task.Error = "failed", asPluginError(errRun).Message
			}
			_ = p.store.put(task)
			return
		}
		for _, media := range result.Media {
			record, errSave := p.store.saveMedia(media, "video", task.AuthID)
			if errSave != nil {
				task.Status, task.Error = "failed", asPluginError(errSave).Message
				_ = p.store.put(task)
				return
			}
			task.Media = append(task.Media, record)
		}
		if len(task.Media) == 0 {
			task.Status, task.Error = "failed", "Muse completed without verified video content"
		} else {
			task.Status, task.Error, task.CompletedAt = "completed", "", time.Now().Unix()
		}
		if errSave := p.store.put(task); errSave != nil {
			p.log("error", "Muse video terminal state could not be persisted")
		}
	}()
	return nil
}

func videoPayload(task videoTask) []byte {
	response := map[string]any{"id": task.ID, "object": "video", "model": task.Model, "status": task.Status, "created_at": task.CreatedAt}
	if task.Seconds != "" {
		response["seconds"] = task.Seconds
	}
	if task.Size != "" {
		response["size"] = task.Size
	}
	if task.Status == "completed" {
		response["completed_at"], response["progress"] = task.CompletedAt, 100
	}
	if task.Error != "" && task.Status == "failed" {
		response["error"] = map[string]any{"code": "generation_failed", "message": task.Error}
	}
	return jsonBytes(response)
}

func (p *provider) retrieveVideo(ctx context.Context, req executorRequest, c credential, operation, id string) (pluginapi.ExecutorResponse, error) {
	task, exists := p.store.get(id)
	if !exists || task.AuthID != req.AuthID {
		return pluginapi.ExecutorResponse{}, &pluginabi.Error{Code: "task_not_found", Message: "Video task not found for the selected credential", HTTPStatus: 404, RequestScoped: true, NoRetry: true}
	}
	if task.Status == "in_progress" && task.ThreadURL != "" {
		if errStart := p.startVideo(task, req, c, "", nil, true); errStart != nil {
			return pluginapi.ExecutorResponse{}, errStart
		}
	}
	if operation != "content" {
		return pluginapi.ExecutorResponse{Payload: videoPayload(task), Headers: http.Header{"Content-Type": {"application/json"}}}, nil
	}
	if task.Status != "completed" || len(task.Media) == 0 {
		return pluginapi.ExecutorResponse{}, &pluginabi.Error{Code: "video_not_ready", Message: "Video content is not available; poll the task for its actual state", HTTPStatus: 409, RequestScoped: true, NoRetry: true}
	}
	data, record, errRead := p.store.readMedia(task.Media[0].Name, req.AuthID)
	if errRead != nil {
		return pluginapi.ExecutorResponse{}, errRead
	}
	return pluginapi.ExecutorResponse{Payload: data, Headers: http.Header{"Content-Type": {record.MIME}, "Content-Disposition": {`attachment; filename="` + record.Name + `"`}, "Cache-Control": {"no-store"}}}, nil
}
