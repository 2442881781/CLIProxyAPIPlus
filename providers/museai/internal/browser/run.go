package browser

import (
	"bytes"
	"context"
	"encoding/base64"
	"errors"
	"image"
	_ "image/gif"
	_ "image/jpeg"
	_ "image/png"
	"mime"
	"net/http"
	"strings"
	"time"
)

type baseline struct {
	Agents  int      `json:"agents"`
	Users   int      `json:"users"`
	Sources []string `json:"sources"`
}
type candidate struct {
	Src    string `json:"src"`
	Video  bool   `json:"video"`
	Width  int    `json:"width"`
	Height int    `json:"height"`
}
type pageState struct {
	Text      string      `json:"text"`
	Confirmed bool        `json:"confirmed"`
	Stop      bool        `json:"stop"`
	Terminal  bool        `json:"terminal"`
	Blocked   bool        `json:"blocked"`
	URL       string      `json:"url"`
	Media     []candidate `json:"media"`
}

func (e *Engine) Run(ctx context.Context, in Input, emit func(Event) error) (Result, error) {
	return e.execute(ctx, in, emit, false)
}
func (e *Engine) Resume(ctx context.Context, in Input, emit func(Event) error) (Result, error) {
	return e.execute(ctx, in, emit, true)
}
func (e *Engine) execute(ctx context.Context, in Input, emit func(Event) error, resume bool) (result Result, err error) {
	submitted := resume
	operationCanceled := false
	defer func() {
		if err == nil {
			return
		}
		if operationCanceled || ctx.Err() != nil || errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
			err = &Error{Code: "canceled", Message: "browser operation canceled", HTTPStatus: 499, Submitted: submitted}
			return
		}
		var engineError *Error
		if errors.As(err, &engineError) {
			engineError.Submitted = submitted
			return
		}
		// Callback/CDP errors can include user text; never expose their raw strings.
		err = &Error{Code: "browser_operation", Message: "owned browser operation failed", HTTPStatus: 502, Submitted: submitted}
	}()
	if in.Kind != "chat" && in.Kind != "image" && in.Kind != "video" {
		return result, &Error{Code: "invalid_kind", Message: "unsupported generation kind", HTTPStatus: 400}
	}
	destination := museOrigin + "/thread/new"
	if resume {
		destination, err = threadURL(in.ThreadURL)
		if err != nil {
			return result, &Error{Code: "invalid_thread", Message: "invalid persisted Muse thread URL", HTTPStatus: 400}
		}
		result.ThreadURL = destination
	} else {
		if strings.TrimSpace(in.Prompt) == "" {
			return result, &Error{Code: "invalid_prompt", Message: "prompt is empty", HTTPStatus: 400}
		}
		if in.ThreadURL != "" {
			return result, &Error{Code: "invalid_thread", Message: "Run always creates an isolated new thread; use Resume to attach", HTTPStatus: 400}
		}
	}
	for _, ref := range in.References {
		mediaType, _, parseErr := mime.ParseMediaType(ref.MIME)
		if parseErr != nil || !strings.HasPrefix(mediaType, "image/") || len(ref.Data) == 0 || !validMedia(ref.Data, mediaType, "image") {
			return result, &Error{Code: "invalid_reference", Message: "reference must contain verified image bytes", HTTPStatus: 400}
		}
	}
	s, operation, err := e.open(ctx, in)
	if err != nil {
		return result, err
	}
	defer s.close()
	defer func() {
		operationCanceled = operation.Err() != nil
		// This cleanup context is independent of caller cancellation. Read renewed
		// HttpOnly cookies on both success and failure, then close only our target.
		cleanup, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		defer cancel()
		if operation.Err() != nil || err != nil {
			var ignored bool
			_ = s.page.evaluate(cleanup, stopJS, &ignored)
		}
		cookies, cookieErr := s.readCookies(cleanup)
		if cookieErr == nil {
			result.Cookies = cookies
		}
		_ = s.browser.call(cleanup, "Target.closeTarget", map[string]any{"targetId": s.target}, nil)
	}()
	if err = s.applyCookies(operation, in.Cookies); err != nil {
		return result, err
	}
	var frameTree struct {
		FrameTree struct {
			Frame struct {
				ID string `json:"id"`
			} `json:"frame"`
		} `json:"frameTree"`
	}
	if err = s.page.call(operation, "Page.getFrameTree", nil, &frameTree); err != nil {
		return result, err
	}
	s.page.mainFrame.Store(frameTree.FrameTree.Frame.ID)
	var navigation struct {
		ErrorText string `json:"errorText"`
	}
	if err = s.page.call(operation, "Page.navigate", map[string]any{"url": destination}, &navigation); err != nil {
		return result, err
	}
	if navigation.ErrorText != "" {
		return result, &Error{Code: "network", Message: "Muse navigation failed on the selected browser transport", HTTPStatus: 502}
	}
	// Only initial page/credential acquisition is bounded; generation has no
	// deadline other than caller cancellation, including awaited media fetches.
	acquire, cancel := context.WithTimeout(operation, 45*time.Second)
	err = s.waitReady(acquire, e.cfg.PollInterval)
	cancel()
	if err != nil {
		return result, err
	}
	var base baseline
	if err = s.page.evaluate(operation, baselineJS, &base); err != nil {
		return result, err
	}
	if !resume {
		if len(in.References) > 0 {
			if err = s.upload(operation, in.References, e.cfg.PollInterval); err != nil {
				return result, err
			}
			// Upload previews are part of the exclusion baseline, never results.
			if err = s.page.evaluate(operation, baselineJS, &base); err != nil {
				return result, err
			}
		}
		if err = s.prepare(operation, in.Prompt, e.cfg.PollInterval); err != nil {
			return result, err
		}
		// Mark ambiguous dispatch as submitted BEFORE issuing the single click.
		// Transport loss after dispatch must never cause upstream resubmission.
		submitted = true
		var clicked bool
		if err = s.page.evaluate(operation, clickSendJS+"("+jsArg(in.Prompt)+")", &clicked); err != nil {
			return result, err
		}
		if !clicked {
			submitted = false
			return result, errors.New("composer refused submission")
		}
	}
	if emit != nil {
		if err = emit(Event{ThreadURL: result.ThreadURL, Submitted: submitted}); err != nil {
			return result, err
		}
	}
	options := map[string]any{"resume": resume, "users": base.Users, "sources": base.Sources, "prompt": in.Prompt}
	if resume {
		options["sources"] = []string{}
	}
	lastSignature := ""
	stable := 0
	observedStop := false
	lastText := ""
	lastThread := result.ThreadURL
	for {
		if err = operation.Err(); err != nil {
			return result, err
		}
		var state pageState
		if err = s.page.evaluate(operation, pollJS+"("+jsArg(options)+")", &state); err != nil {
			return result, err
		}
		if thread, urlErr := threadURL(state.URL); urlErr == nil {
			result.ThreadURL = thread
		}
		if result.ThreadURL != lastThread {
			if emit != nil {
				if err = emit(Event{Text: lastText, ThreadURL: result.ThreadURL, Submitted: submitted}); err != nil {
					return result, err
				}
			}
			lastThread = result.ThreadURL
		}
		if state.Blocked {
			return result, &Error{Code: "approval_required", Message: "Muse requires a user approval; no approval was clicked", HTTPStatus: 409, Submitted: submitted}
		}
		if state.Stop {
			observedStop = true
		}
		// Ignore stale assistant messages until the submitted user bubble is seen.
		if state.Confirmed {
			result.Text = state.Text
			if state.Text != lastText || result.ThreadURL != lastThread {
				if emit != nil {
					if err = emit(Event{Text: state.Text, ThreadURL: result.ThreadURL, Submitted: true}); err != nil {
						return result, err
					}
				}
				lastText = state.Text
				lastThread = result.ThreadURL
			}
			signature := state.Text
			for _, m := range state.Media {
				signature += "\x00" + m.Src
			}
			if signature == lastSignature {
				stable++
			} else {
				lastSignature = signature
				stable = 0
			}
			// Absence of a stop button alone is not evidence of completion. Resume
			// requires explicit terminal markup if generation already ended on reload.
			if !state.Stop && (observedStop || state.Terminal) && stable >= 3 {
				if in.Kind == "chat" {
					if result.Text == "" {
						return result, &Error{Code: "empty_result", Message: "Muse completed without assistant text", HTTPStatus: 502, Submitted: true}
					}
				} else {
					for _, m := range state.Media {
						if (in.Kind == "video") != m.Video {
							continue
						}
						media, extractErr := s.extract(operation, m, in.Kind)
						if extractErr != nil {
							return result, extractErr
						}
						result.Media = append(result.Media, media)
					}
					if len(result.Media) == 0 {
						return result, &Error{Code: "missing_media", Message: "Muse completed without the requested new agent media", HTTPStatus: 502, Submitted: true}
					}
				}
				// Fetching a blob may take time. Re-observe stop/terminal state before
				// emitting success; never declare completion while stop is visible.
				var final pageState
				if err = s.page.evaluate(operation, pollJS+"("+jsArg(options)+")", &final); err != nil {
					return result, err
				}
				if final.Blocked {
					return result, &Error{Code: "approval_required", Message: "Muse requires a user approval; no approval was clicked", HTTPStatus: 409, Submitted: true}
				}
				finalSignature := final.Text
				for _, m := range final.Media {
					finalSignature += "\x00" + m.Src
				}
				if final.Stop || finalSignature != signature {
					result.Media = nil
					stable = 0
					continue
				}
				if emit != nil {
					if err = emit(Event{Text: result.Text, ThreadURL: result.ThreadURL, Submitted: true, Completed: true}); err != nil {
						return result, err
					}
				}
				return result, nil
			}
		}
		if err = delay(operation, e.cfg.PollInterval); err != nil {
			return result, err
		}
	}
}
func (s *session) waitReady(ctx context.Context, poll time.Duration) error {
	for {
		switch s.page.navigationStatus.Load() {
		case 401:
			return &Error{Code: "authentication", Message: "Muse rejected the credential", HTTPStatus: 401}
		case 403:
			return &Error{Code: "access_denied", Message: "Muse denied access on the selected browser transport", HTTPStatus: 403}
		case 429:
			return &Error{Code: "quota", Message: "Muse returned a rate or quota limit", HTTPStatus: 429}
		}
		if status := s.page.navigationStatus.Load(); status >= 500 {
			return &Error{Code: "upstream_unavailable", Message: "Muse navigation returned an upstream server error", HTTPStatus: 502}
		}
		var state struct {
			Ready   bool `json:"ready"`
			Blocked bool `json:"blocked"`
			Login   bool `json:"login"`
		}
		if err := s.page.evaluate(ctx, readyJS, &state); err == nil {
			if state.Blocked {
				return &Error{Code: "approval_required", Message: "Muse requires a user approval; no approval was clicked", HTTPStatus: 409}
			}
			if state.Ready {
				return nil
			}
			if state.Login {
				return &Error{Code: "authentication", Message: "Muse credential requires login", HTTPStatus: 401}
			}
		}
		if err := delay(ctx, poll); err != nil {
			if errors.Is(err, context.DeadlineExceeded) {
				return &Error{Code: "page_unavailable", Message: "Muse composer readiness was not confirmed", HTTPStatus: 503}
			}
			return err
		}
	}
}
func (s *session) prepare(ctx context.Context, prompt string, poll time.Duration) error {
	var prepared bool
	if err := s.page.evaluate(ctx, preparePromptJS+"("+jsArg(prompt)+")", &prepared); err != nil {
		return err
	}
	if !prepared {
		return errors.New("cannot focus composer")
	}
	for {
		var state struct {
			Ready   bool `json:"ready"`
			Blocked bool `json:"blocked"`
		}
		if err := s.page.evaluate(ctx, sendStateJS+"("+jsArg(prompt)+")", &state); err != nil {
			return err
		}
		if state.Blocked {
			return &Error{Code: "approval_required", Message: "Muse requires a user approval; no approval was clicked", HTTPStatus: 409}
		}
		if state.Ready {
			return nil
		}
		if err := delay(ctx, poll); err != nil {
			return err
		}
	}
}
func (s *session) upload(ctx context.Context, references []Reference, poll time.Duration) error {
	refs := make([]map[string]string, 0, len(references))
	for _, ref := range references {
		mediaType, _, _ := mime.ParseMediaType(ref.MIME)
		refs = append(refs, map[string]string{"data": base64.StdEncoding.EncodeToString(ref.Data), "mime": mediaType})
	}
	var uploaded bool
	if err := s.page.evaluate(ctx, uploadJS+"("+jsArg(refs)+")", &uploaded); err != nil {
		return err
	}
	if !uploaded {
		return errors.New("reference upload rejected")
	}
	for {
		var state struct {
			Count   int  `json:"count"`
			Pending bool `json:"pending"`
			Blocked bool `json:"blocked"`
		}
		if err := s.page.evaluate(ctx, uploadStateJS, &state); err != nil {
			return err
		}
		if state.Blocked {
			return &Error{Code: "approval_required", Message: "Muse requires a user approval; no approval was clicked", HTTPStatus: 409}
		}
		if state.Count >= len(references) && !state.Pending {
			return nil
		}
		if err := delay(ctx, poll); err != nil {
			return err
		}
	}
}
func (s *session) extract(ctx context.Context, m candidate, kind string) (Media, error) {
	var extracted struct {
		OK   bool   `json:"ok"`
		MIME string `json:"mime"`
		Data string `json:"data"`
	}
	if err := s.page.evaluate(ctx, extractJS+"("+jsArg(m.Src)+")", &extracted); err != nil {
		return Media{}, err
	}
	data, err := base64.StdEncoding.DecodeString(extracted.Data)
	mediaType, _, mimeErr := mime.ParseMediaType(extracted.MIME)
	if !extracted.OK || err != nil || mimeErr != nil || !validMedia(data, mediaType, kind) {
		return Media{}, &Error{Code: "invalid_media", Message: "generated media bytes or MIME do not match the requested kind", HTTPStatus: 502, Submitted: true}
	}
	return Media{Data: data, MIME: mediaType, Width: m.Width, Height: m.Height}, nil
}
func validMedia(data []byte, mediaType, kind string) bool {
	if len(data) == 0 || !strings.HasPrefix(mediaType, kind+"/") {
		return false
	}
	if kind == "image" {
		switch mediaType {
		case "image/png", "image/jpeg", "image/gif":
			_, _, err := image.DecodeConfig(bytes.NewReader(data))
			return err == nil && http.DetectContentType(data) == mediaType
		case "image/webp":
			return len(data) >= 12 && string(data[:4]) == "RIFF" && string(data[8:12]) == "WEBP"
		case "image/avif":
			return len(data) >= 16 && string(data[4:8]) == "ftyp" && (string(data[8:12]) == "avif" || string(data[8:12]) == "avis")
		default:
			return false
		}
	}
	switch mediaType {
	case "video/mp4", "video/quicktime":
		return validISOvideo(data)
	case "video/webm":
		return len(data) >= 4 && bytes.Equal(data[:4], []byte{0x1a, 0x45, 0xdf, 0xa3}) && bytes.Contains(data[:min(len(data), 4096)], []byte("webm"))
	default:
		return false
	}
}

// ISO BMFF images share ftyp with MP4. Require a recognized video brand and
// reject image brands rather than trusting a .mp4 URL or declared MIME.
func validISOvideo(data []byte) bool {
	if len(data) < 16 || string(data[4:8]) != "ftyp" {
		return false
	}
	size := int(uint32(data[0])<<24 | uint32(data[1])<<16 | uint32(data[2])<<8 | uint32(data[3]))
	if size < 16 || size > len(data) {
		return false
	}
	video := false
	for offset := 8; offset+4 <= size; offset += 4 {
		if offset == 12 {
			continue
		}
		switch string(data[offset : offset+4]) {
		case "avif", "avis", "heic", "heix", "hevc", "hevx", "mif1", "msf1":
			return false
		case "isom", "iso2", "iso3", "iso4", "iso5", "iso6", "mp41", "mp42", "avc1", "qt  ", "M4V ", "MSNV", "dash":
			video = true
		}
	}
	return video
}
