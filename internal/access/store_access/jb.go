package storeaccess

import (
	"net/http"
	"strings"

	"github.com/router-for-me/CLIProxyAPI/v8/internal/config"
	log "github.com/sirupsen/logrus"
)

// JBHeader is the request header carrying per-request JB signals. The header
// may only narrow (disable) features for one request; it can never raise a
// field above the key's resolved ceiling.
const JBHeader = "X-JB"

// JBSnapshot is the resolved JB decision for one request, stamped into
// executor.Options.Metadata so the upstream round-trip layer can apply the
// state machine without re-reading the store.
type JBSnapshot struct {
	config.JBEffective

	// KeyID identifies the store key that produced this ceiling, for logging.
	KeyID string `json:"key_id,omitempty"`
	// Group is the group name consulted during resolution.
	Group string `json:"group,omitempty"`
	// HeaderRejected reports that the client sent an X-JB directive the
	// ceiling does not permit (e.g. `narrative` while nsfw=off). The request
	// still proceeds under the ceiling; this flag is for observability.
	HeaderRejected bool `json:"header_rejected,omitempty"`
}

// jbMetadataKey is the executor.Options.Metadata key under which the resolved
// snapshot travels.
const jbMetadataKey = "storeaccess.jb"

// ResolveJB collapses the three-layer chain (global defaults -> group -> key)
// into the request's effective JB ceiling, then applies any narrowing the
// client requested via the X-JB header.
//
// Callers pass the access metadata map produced during authentication. When
// the request was not authenticated through the store (config api-keys, no
// provider) meta is nil and the global defaults apply directly.
//
// Header directives understood:
//
//	no-spec      disable spec injection (handled by the injector; folds into jb for this request)
//	no-nsfw      disable nsfw admission for this request
//	no-retry     disable the refusal-retry continuation for this request
//	no-disambig  disable the cyber_policy lazy retry for this request
//	narrative    declare the session narrative; requires nsfw admission or the
//	             directive is rejected (logged + HeaderRejected)
//	auto         defer narrative classification to the regex (default behavior)
func ResolveJB(cfg *config.SDKConfig, meta map[string]string, header http.Header) JBSnapshot {
	var snap JBSnapshot
	if cfg == nil || !cfg.JB.IsEnabled() {
		return snap
	}
	snap.JBEffective = cfg.JB.Defaults.Effective(config.JBPrefs{
		Disambig: new(true),
	})

	// Group then key layers. Only when the request authenticated via the
	// store do these layers exist; a config-key or unauthenticated request
	// keeps the global floor.
	if meta != nil {
		snap.KeyID = strings.TrimSpace(meta["key_id"])
		snap.Group = strings.TrimSpace(meta["group"])
		if snap.KeyID != "" {
			if entry := storeEntry(snap.KeyID); entry != nil {
				var grp *Group
				if snap.Group != "" {
					grp = storeGroup(snap.Group)
				}
				snap.JBEffective = layeredEffective(cfg.JB.Defaults, grp, entry)
			}
		}
	}

	// X-JB header narrowing. Each directive only turns a feature off, or
	// (for `narrative`) asserts session intent under an existing ceiling.
	for _, directive := range strings.Split(header.Get(JBHeader), ",") {
		switch strings.ToLower(strings.TrimSpace(directive)) {
		case "", "auto":
			// no-op: auto is the default behavior
		case "narrative":
			if snap.NSFW {
				snap.Narrative = true
			} else {
				snap.HeaderRejected = true
				log.WithFields(log.Fields{
					"key_id": snap.KeyID, "directive": "narrative",
				}).Info("jb: X-JB narrative rejected by nsfw ceiling")
			}
		case "no-nsfw":
			snap.NSFW = false
			snap.Narrative = false
		case "no-retry":
			snap.RefusalRetry = false
		case "no-spec":
			snap.JB = false
		case "no-disambig":
			snap.Disambig = false
		}
	}
	return snap
}

// layeredEffective folds the three layers into concrete booleans. Key wins on
// fields it sets; group fills the rest; global defaults fill what's left.
// The result is the key's ceiling — request headers may only narrow it.
func layeredEffective(global config.JBPrefs, grp *Group, entry *AccessKey) config.JBEffective {
	merged := global
	if grp != nil && grp.JB != nil {
		merged = grp.JB.Resolve(&merged)
	}
	if entry != nil && entry.JB != nil {
		merged = entry.JB.Resolve(&merged)
	}
	return merged.Effective(config.JBPrefs{Disambig: new(true)})
}

// storeEntry / storeGroup are variables so tests can stub the store without
// spinning up a real file-backed Store.
var (
	storeEntry = func(id string) *AccessKey {
		if s := DefaultStore(); s != nil {
			return s.Get(id)
		}
		return nil
	}
	storeGroup = func(name string) *Group {
		if s := DefaultStore(); s != nil {
			return s.GetGroup(name)
		}
		return nil
	}
)

// InjectMetadata stamps the resolved snapshot into executor options metadata.
// The upstream round-trip layer reads it via SnapshotFromMetadata.
func (s JBSnapshot) InjectMetadata(meta map[string]any) map[string]any {
	if meta == nil {
		meta = make(map[string]any)
	}
	meta[jbMetadataKey] = s
	return meta
}

// SnapshotFromMetadata extracts the snapshot stamped by InjectMetadata, or
// the zero value (all off) when absent.
func SnapshotFromMetadata(meta map[string]any) JBSnapshot {
	if meta == nil {
		return JBSnapshot{}
	}
	if raw, ok := meta[jbMetadataKey]; ok {
		if snap, okCast := raw.(JBSnapshot); okCast {
			return snap
		}
	}
	return JBSnapshot{}
}
