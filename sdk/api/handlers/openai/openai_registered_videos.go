package openai

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"

	"github.com/gin-gonic/gin"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/interfaces"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/registry"
	"github.com/router-for-me/CLIProxyAPI/v8/sdk/api/handlers"
	coreexecutor "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/executor"
	"github.com/tidwall/gjson"
	"github.com/tidwall/sjson"
)

const registeredVideoIDPrefix = "cpa-video."

type registeredVideoTask struct {
	Version        int    `json:"v"`
	Model          string `json:"model"`
	SelectionModel string `json:"selection_model,omitempty"`
	AuthID         string `json:"auth_id"`
	ID             string `json:"id"`
}

func isRegisteredVideosModel(model string) bool {
	info := registry.LookupModelInfo(strings.TrimSpace(model))
	return info != nil && info.Type == registry.OpenAIVideoModelType
}

func encodeRegisteredVideoTask(task registeredVideoTask) (string, error) {
	if task.Version != 1 || task.Model == "" || task.AuthID == "" || task.ID == "" {
		return "", fmt.Errorf("video response did not include a durable model, credential and task ID")
	}
	raw, err := json.Marshal(task)
	if err != nil {
		return "", err
	}
	return registeredVideoIDPrefix + base64.RawURLEncoding.EncodeToString(raw), nil
}

func decodeRegisteredVideoTask(id string) (registeredVideoTask, error) {
	var task registeredVideoTask
	raw, err := base64.RawURLEncoding.DecodeString(strings.TrimPrefix(id, registeredVideoIDPrefix))
	if err != nil {
		return task, fmt.Errorf("invalid video task ID")
	}
	if err := json.Unmarshal(raw, &task); err != nil || task.Version != 1 || task.Model == "" || task.AuthID == "" || task.ID == "" {
		return registeredVideoTask{}, fmt.Errorf("invalid video task ID")
	}
	return task, nil
}

func (h *OpenAIAPIHandler) collectRegisteredVideosCreate(c *gin.Context, body []byte, model string) {
	ctx, cancel := h.GetContextWithCancel(h, c, context.Background())
	defer cancel(nil)
	authID, executionModel := "", model
	ctx = handlers.WithSelectedAuthIDCallback(ctx, func(id string) { authID = id })
	ctx = coreexecutor.WithExecutionModelCallback(ctx, func(selected string) { executionModel = selected })
	response, headers, errMsg := h.ExecuteWithAuthManager(ctx, registry.OpenAIVideoModelType, model, body, "")
	if errMsg != nil {
		h.WriteErrorResponse(c, errMsg)
		return
	}
	id, err := encodeRegisteredVideoTask(registeredVideoTask{Version: 1, Model: executionModel, SelectionModel: model, AuthID: authID, ID: videoIDFromPayload(response)})
	if err == nil {
		response, err = sjson.SetBytes(response, "id", id)
	}
	if err != nil {
		h.WriteErrorResponse(c, &interfaces.ErrorMessage{StatusCode: http.StatusBadGateway, Error: err})
		return
	}
	if gjson.GetBytes(response, "request_id").Exists() {
		response, _ = sjson.SetBytes(response, "request_id", id)
	}
	handlers.WriteUpstreamHeaders(c.Writer.Header(), headers)
	c.Data(http.StatusOK, "application/json", response)
}

// Self-contained routing is independent of videoAuthBindings and survives restart.
// Never fall through to xAI or rotate to another credential on a failed pin.
func (h *OpenAIAPIHandler) handleRegisteredVideosGet(c *gin.Context, videoID, operation string) bool {
	if !strings.HasPrefix(videoID, registeredVideoIDPrefix) {
		return false
	}
	task, err := decodeRegisteredVideoTask(videoID)
	if err != nil {
		h.WriteErrorResponse(c, &interfaces.ErrorMessage{StatusCode: http.StatusBadRequest, Error: err})
		return true
	}
	if h.AuthManager == nil {
		h.WriteErrorResponse(c, &interfaces.ErrorMessage{StatusCode: http.StatusServiceUnavailable, Error: fmt.Errorf("video credential is unavailable")})
		return true
	}
	auth, ok := h.AuthManager.GetByID(task.AuthID)
	if !ok || auth.Disabled {
		h.WriteErrorResponse(c, &interfaces.ErrorMessage{StatusCode: http.StatusNotFound, Error: fmt.Errorf("video credential is unavailable")})
		return true
	}
	body, _ := json.Marshal(map[string]string{"operation": operation, "id": task.ID})
	ctx, cancel := h.GetContextWithCancel(h, c, context.Background())
	defer cancel(nil)
	ctx = handlers.WithPinnedAuthID(ctx, task.AuthID)
	ctx = coreexecutor.WithPinnedExecutionModel(ctx, task.Model)
	selectionModel := task.SelectionModel
	if selectionModel == "" {
		selectionModel = task.Model
	}
	response, errMsg := h.ExecuteModel(ctx, handlers.ModelExecutionRequest{EntryProtocol: registry.OpenAIVideoModelType, ExitProtocol: registry.OpenAIVideoModelType, Model: task.Model, AuthSelectionModel: selectionModel, AuthID: task.AuthID, ForcedProvider: auth.Provider, Body: body, Headers: c.Request.Header, Query: c.Request.URL.Query(), Path: c.Request.URL.Path})
	if errMsg != nil {
		h.WriteErrorResponse(c, errMsg)
		return true
	}
	if operation == "content" {
		contentType := response.Headers.Get("Content-Type")
		if !strings.HasPrefix(strings.ToLower(contentType), "video/") {
			h.WriteErrorResponse(c, &interfaces.ErrorMessage{StatusCode: http.StatusBadGateway, Error: fmt.Errorf("video executor returned non-video content")})
			return true
		}
		copyVideoContentHeaders(c.Writer.Header(), response.Headers)
		c.Data(http.StatusOK, contentType, response.Body)
		return true
	}
	out, err := sjson.SetBytes(response.Body, "id", videoID)
	if err != nil {
		h.WriteErrorResponse(c, &interfaces.ErrorMessage{StatusCode: http.StatusBadGateway, Error: err})
		return true
	}
	handlers.WriteUpstreamHeaders(c.Writer.Header(), response.Headers)
	c.Data(http.StatusOK, "application/json", out)
	return true
}
