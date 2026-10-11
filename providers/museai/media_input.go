package main

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"io"
	"mime"
	"mime/multipart"
	"net/http"
	"strings"
)

func mediaPayload(req executorRequest) ([]byte, error) {
	if json.Valid(req.Payload) {
		return req.Payload, nil
	}
	mediaType, params, errType := mime.ParseMediaType(req.Headers.Get("Content-Type"))
	if errType != nil || mediaType != "multipart/form-data" || params["boundary"] == "" {
		return nil, requestError("invalid_request", "Media request must be JSON or multipart/form-data")
	}
	reader := multipart.NewReader(bytes.NewReader(req.Payload), params["boundary"])
	fields := make(map[string]any)
	var references []string
	for {
		part, errPart := reader.NextPart()
		if errPart == io.EOF {
			break
		}
		if errPart != nil {
			return nil, requestError("invalid_request", "Invalid media multipart body")
		}
		limit := int64(1 << 20)
		if part.FileName() != "" {
			limit = 24 << 20
		}
		data, errRead := io.ReadAll(io.LimitReader(part, limit+1))
		errClose := part.Close()
		if errRead != nil || errClose != nil || int64(len(data)) > limit {
			return nil, requestError("invalid_request", "Media multipart field exceeds its size limit or could not be read")
		}
		name := part.FormName()
		if part.FileName() != "" {
			if name != "image" && name != "image[]" && name != "input_reference" {
				return nil, requestError("unsupported_reference", "Only a reference image file is supported; masks and other file parts are not supported")
			}
			contentType := part.Header.Get("Content-Type")
			if contentType == "" || contentType == "application/octet-stream" {
				contentType = http.DetectContentType(data)
			}
			contentType, _, errType = mime.ParseMediaType(contentType)
			if errType != nil || !strings.HasPrefix(contentType, "image/") {
				return nil, requestError("invalid_reference", "Reference file must contain an image")
			}
			references = append(references, "data:"+contentType+";base64,"+base64.StdEncoding.EncodeToString(data))
			continue
		}
		switch name {
		case "model", "prompt", "size", "seconds", "duration", "aspect_ratio", "response_format", "reference_image":
			fields[name] = string(data)
		case "n":
			var count int
			if json.Unmarshal(data, &count) != nil {
				return nil, requestError("invalid_count", "n must be an integer")
			}
			fields[name] = count
		case "image", "input_reference":
			fields[name] = string(data)
		case "mask":
			return nil, requestError("unsupported_reference", "Muse does not support image masks")
		}
	}
	if len(references) > 0 {
		fields["images"] = references
	}
	return json.Marshal(fields)
}
