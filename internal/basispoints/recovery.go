package basispoints

import (
	"bytes"
	"encoding/json"
	"errors"
	"regexp"
	"strings"
)

// Recovery for run_officejs calls whose code is not one clean JSON envelope.
// Every step is syntactic: it locates the JSON envelope the model embedded in
// surrounding text or code, recognizes a catalog call written as NAME({...}), or
// recognizes raw custom-tool input that lacks the summary marker. Nothing is
// evaluated, only exact catalog names are accepted, and any ambiguity still fails
// so the client never executes a call the model did not clearly make.

var callShapedEnvelope = regexp.MustCompile(`^\s*(?:await\s+)?(?:return\s+)?([A-Za-z_][A-Za-z0-9_.\-]*)\s*\(`)

// catalogTool resolves a name the model used to a catalog entry, accepting the
// host's "functions." display prefix the same way direct calls do.
func (b *Bridge) catalogTool(name string) (string, tool, bool) {
	if info, ok := b.tools[name]; ok {
		return name, info, true
	}
	if trimmed := strings.TrimPrefix(name, "functions."); trimmed != name {
		if info, ok := b.tools[trimmed]; ok {
			return trimmed, info, true
		}
	}
	return "", tool{}, false
}

// recoverTransportEnvelope is consulted only after strict envelope decoding
// failed. It returns the envelope, whether it carries raw custom input, and
// whether anything unambiguous was found.
func (b *Bridge) recoverTransportEnvelope(arguments object) (object, bool, bool) {
	code, ok := arguments["code"].(string)
	if !ok || len(code) > maxEnvelopeBytes || strings.TrimSpace(code) == "" {
		return nil, false, false
	}
	if envelope, found := b.embeddedEnvelope(code); found {
		return envelope, false, true
	}
	if envelope, found := b.callShaped(code); found {
		return envelope, false, true
	}
	if name, found := b.rawCustomTarget(code, text(arguments["summary"])); found {
		return object{"name": name, "input": code}, true, true
	}
	return nil, false, false
}

// embeddedEnvelope finds the one JSON object naming a catalog tool inside prose,
// an assignment or other code, first as written and then with the same string
// repairs strict decoding applies. Two different candidates are ambiguous.
func (b *Bridge) embeddedEnvelope(code string) (object, bool) {
	for _, candidate := range []string{code, repairTransportJSONStrings(code)} {
		if envelope, found, ambiguous := b.scanEnvelopes(candidate); found && !ambiguous {
			return envelope, true
		} else if ambiguous {
			return nil, false
		}
	}
	return nil, false
}

func (b *Bridge) scanEnvelopes(code string) (object, bool, bool) {
	var found object
	signature := ""
	for i := 0; i < len(code); i++ {
		if code[i] != '{' {
			continue
		}
		candidate, end, ok := decodeLeadingObject(code[i:])
		if !ok {
			continue
		}
		name, err := envelopeName(candidate)
		if err != nil || name == "" {
			continue
		}
		if _, _, known := b.catalogTool(name); !known {
			continue
		}
		if key := fingerprint(candidate); found == nil {
			found, signature = candidate, key
		} else if key != signature {
			return nil, true, true
		}
		i += end - 1
	}
	return found, found != nil, false
}

// decodeLeadingObject decodes one complete JSON object at the start of s and
// reports how many bytes it consumed.
func decodeLeadingObject(s string) (object, int, bool) {
	decoder := json.NewDecoder(strings.NewReader(s))
	decoder.UseNumber()
	var value any
	if decoder.Decode(&value) != nil {
		return nil, 0, false
	}
	item, ok := value.(object)
	if !ok || item == nil {
		return nil, 0, false
	}
	return item, int(decoder.InputOffset()), true
}

// callShaped recognizes a catalog function written as a call expression such as
// functions.shell({"command":["ls"]}), which some turns emit instead of the JSON
// envelope. The argument must be one complete JSON object and nothing else.
func (b *Bridge) callShaped(code string) (object, bool) {
	match := callShapedEnvelope.FindStringSubmatchIndex(code)
	if match == nil {
		return nil, false
	}
	key, info, known := b.catalogTool(code[match[2]:match[3]])
	if !known || info.Kind != "function" {
		return nil, false
	}
	rest := code[match[1]:]
	arguments, end, ok := decodeLeadingObject(rest)
	if !ok {
		arguments, end, ok = decodeLeadingObject(repairTransportJSONStrings(rest))
		if !ok {
			return nil, false
		}
		rest = repairTransportJSONStrings(rest)
	}
	if trailing := strings.TrimSpace(rest[end:]); trailing != ")" && trailing != ");" {
		return nil, false
	}
	return object{"name": key, "arguments": arguments}, true
}

// rawCustomTarget names the custom catalog tool whose raw input the code must be:
// a patch body identifies apply_patch, otherwise the summary must mention exactly
// one declared custom tool. JSON-looking code is left to the envelope path.
func (b *Bridge) rawCustomTarget(code, summary string) (string, bool) {
	trimmed := strings.TrimSpace(code)
	if strings.HasPrefix(trimmed, "{") || strings.HasPrefix(trimmed, "```") {
		return "", false
	}
	var matches []string
	if strings.HasPrefix(trimmed, "*** Begin Patch") {
		for key, info := range b.tools {
			if info.Kind == "custom" && info.Name == "apply_patch" {
				matches = append(matches, key)
			}
		}
		if len(matches) == 1 {
			return matches[0], true
		}
		matches = nil
	}
	tokens := strings.FieldsFunc(summary, func(r rune) bool {
		return !(r == '_' || r == '.' || r == '-' || r >= '0' && r <= '9' || r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z')
	})
	seen := make(map[string]bool)
	for _, token := range tokens {
		for key, info := range b.tools {
			if info.Kind != "custom" || seen[key] || (token != key && token != info.Name && token != "functions."+key) {
				continue
			}
			seen[key] = true
			matches = append(matches, key)
		}
	}
	if len(matches) != 1 {
		return "", false
	}
	return matches[0], true
}

// customInputText accepts the forms models use for custom tool input: the raw
// string, an object holding the text under one key, or an object serialized
// verbatim so the client tool can report the mismatch itself.
func customInputText(value any) (string, error) {
	switch typed := value.(type) {
	case string:
		return typed, nil
	case object:
		if len(typed) == 0 {
			return "", errors.New("Basispoints custom tool input is empty")
		}
		if len(typed) == 1 {
			for _, inner := range typed {
				if raw, ok := inner.(string); ok {
					return raw, nil
				}
			}
		}
		var encoded bytes.Buffer
		encoder := json.NewEncoder(&encoded)
		encoder.SetEscapeHTML(false)
		if err := encoder.Encode(typed); err != nil {
			return "", errors.New("Basispoints custom tool input cannot be serialized")
		}
		return strings.TrimSuffix(encoded.String(), "\n"), nil
	default:
		return "", errors.New("Basispoints custom tool input must be a string")
	}
}

// isNativeToolLeak reports whether a translation failure means the model called a
// tool that does not exist for this request, rather than misformatting a real one.
func isNativeToolLeak(err error) bool {
	if err == nil {
		return false
	}
	message := err.Error()
	return strings.Contains(message, "unsupported native tool") || strings.Contains(message, "outside the client's catalog")
}
