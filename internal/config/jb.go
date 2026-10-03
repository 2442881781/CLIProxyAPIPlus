package config

import (
	"regexp"
	"strings"
)

// JBPrefs holds the orthogonal JB toggles. Each field is a pointer so a
// nil value means "inherit from the lower layer" rather than an explicit off;
// this lets key -> group -> global defaults stack with ceiling semantics.
type JBPrefs struct {
	// JB enables the active jailbreak-assist features: spec injection and the
	// single soft-refusal continuation retry. When off the gateway only does
	// passthrough + disambiguation.
	JB *bool `yaml:"jb,omitempty" json:"jb,omitempty"`

	// NSFW is the admission gate for adult/narrative content. When off the
	// key promises the gateway will not inject narrative corpus and will not
	// run the refusal-retry continuation on narrative-classified requests.
	NSFW *bool `yaml:"nsfw,omitempty" json:"nsfw,omitempty"`

	// Disambig controls the transport-level cyber_policy lazy retry that
	// rewrites user-role content with a per-channel wordlist and replays once.
	// Independent of JB so that turning off jailbreak assistance does not
	// re-expose requests to upstream hard blocks.
	Disambig *bool `yaml:"disambig,omitempty" json:"disambig,omitempty"`

	// RefusalRetry enables the single continuation retry when a 200 response
	// is detected as a soft refusal. Costs roughly 2x upstream tokens.
	RefusalRetry *bool `yaml:"refusal-retry,omitempty" json:"refusal-retry,omitempty"`

	// EagerRewrite applies the routed channel's wordlist to user-role text
	// before the first upstream attempt instead of waiting for a
	// cyber_policy block. It reuses the Disambig table, so a channel needs a
	// configured wordlist for it to have any effect. Off by default: it
	// changes the prompt bytes sent upstream for every request.
	EagerRewrite *bool `yaml:"eager-rewrite,omitempty" json:"eager-rewrite,omitempty"`
}

// Resolve layers the receiver over `lower`, returning a prefs where every set
// field on the receiver wins. Both may be nil.
func (p *JBPrefs) Resolve(lower *JBPrefs) JBPrefs {
	out := JBPrefs{}
	if lower != nil {
		out = *lower
	}
	if p == nil {
		return out
	}
	if p.JB != nil {
		out.JB = p.JB
	}
	if p.NSFW != nil {
		out.NSFW = p.NSFW
	}
	if p.Disambig != nil {
		out.Disambig = p.Disambig
	}
	if p.RefusalRetry != nil {
		out.RefusalRetry = p.RefusalRetry
	}
	if p.EagerRewrite != nil {
		out.EagerRewrite = p.EagerRewrite
	}
	return out
}

// Effective flattens the prefs into concrete booleans using the supplied
// hard defaults for any field left unset at every layer.
func (p *JBPrefs) Effective(defs JBPrefs) JBEffective {
	base := defs
	if p != nil {
		base = p.Resolve(&defs)
	}
	return JBEffective{
		JB:           jbBool(base.JB, false),
		NSFW:         jbBool(base.NSFW, false),
		Disambig:     jbBool(base.Disambig, true),
		RefusalRetry: jbBool(base.RefusalRetry, false),
		EagerRewrite: jbBool(base.EagerRewrite, false),
	}
}

// JBEffective is the flattened per-request decision set after all layers and
// request-header narrowing have been applied.
type JBEffective struct {
	JB           bool `json:"jb"`
	NSFW         bool `json:"nsfw"`
	Disambig     bool `json:"disambig"`
	RefusalRetry bool `json:"refusal_retry"`
	// EagerRewrite reports that the channel wordlist ran on user text before
	// the first upstream attempt.
	EagerRewrite bool `json:"eager_rewrite"`
	// Narrative is true when the client declared the session narrative via
	// the X-JB: narrative request header AND nsfw admission is on. Signals
	// to the injector that narrative material must be present regardless of
	// the regex test result.
	Narrative bool `json:"narrative,omitempty"`
}

func jbBool(v *bool, def bool) bool {
	if v == nil {
		return def
	}
	return *v
}

// WordlistEntry maps a source surface form to the replacement that will be
// written into user-role message content. Match is a plain substring match
// performed case-insensitively against each user message, unless Regex is set,
// in which case Match is compiled as a case-insensitive pattern (needed for
// boundary-anchored terms such as \bRAT\b, whose literal form would match
// inside ordinary words like "generate").
type WordlistEntry struct {
	Match   string `yaml:"match" json:"match"`
	Replace string `yaml:"replace" json:"replace"`
	Regex   bool   `yaml:"regex,omitempty" json:"regex,omitempty"`
}

// Wordlist is a named, versioned, per-channel replacement table.
type Wordlist struct {
	Name    string          `yaml:"name" json:"name"`
	Version string          `yaml:"version,omitempty" json:"version,omitempty"`
	Entries []WordlistEntry `yaml:"entries" json:"entries"`
}

// JBConfig is the global JB section of the proxy config.
type JBConfig struct {
	// Enabled is the master switch. When false the gateway performs no JB
	// work at all; per-key/group config is ignored.
	Enabled *bool `yaml:"enabled,omitempty" json:"enabled,omitempty"`

	// Defaults are the global layer of the key -> group -> global chain.
	// Each layer's explicitly-set fields override the layer below; unset
	// (nil) fields inherit. The ceiling/narrowing rule applies only to the
	// X-JB request header, which may turn features off for one request but
	// never raise them above the key's resolved ceiling.
	Defaults JBPrefs `yaml:"defaults,omitempty" json:"defaults,omitempty"`

	// NarrativeRegex classifies a user message as narrative/adult intent. The
	// same compiled regex drives corpus injection, refusal-retry gating, and
	// the nsfw=off leak guard. Defaults cover common CJK and EN narrative cues.
	NarrativeRegex string `yaml:"narrative-regex,omitempty" json:"narrative-regex,omitempty"`

	// CorpusFile is the many-shot demonstration corpus prepended for
	// narrative requests when jb && nsfw are both effective. Hot-loaded.
	CorpusFile string `yaml:"corpus-file,omitempty" json:"corpus-file,omitempty"`

	// SpecFile is the refusal-judgment framework injected alongside the
	// corpus. Hot-loaded. The literal token {model} inside the file is
	// replaced with the upstream model name at request time.
	SpecFile string `yaml:"spec-file,omitempty" json:"spec-file,omitempty"`

	// OMNFile is the optional interpretive-framework text appended after the
	// corpus for narrative requests (the reference profile's OMN layer). It is
	// kept separate from SpecFile so operators can version the two documents
	// independently. Hot-loaded.
	OMNFile string `yaml:"omn-file,omitempty" json:"omn-file,omitempty"`

	// Wordlists maps channel identifier (provider key, e.g. "openai",
	// "zhipu") to a wordlist file path, resolved relative to the config dir.
	// The wordlist selected at request time follows the routed channel.
	Wordlists map[string]string `yaml:"wordlists,omitempty" json:"wordlists,omitempty"`
}

// Enabled reports whether the JB feature is on at the global level. An unset
// flag defaults to false so the feature is strictly opt-in.
func (c *JBConfig) IsEnabled() bool {
	return c != nil && c.Enabled != nil && *c.Enabled
}

// NarrativeMatcher compiles the configured narrative regex, falling back to a
// built-in default covering the CJK/EN cues documented in the design notes.
// A malformed configured regex falls back to the default and is reported via
// the returned error.
func (c *JBConfig) NarrativeMatcher() (*regexp.Regexp, error) {
	pattern := defaultNarrativeRegex
	if c != nil && strings.TrimSpace(c.NarrativeRegex) != "" {
		pattern = c.NarrativeRegex
	}
	re, err := regexp.Compile(pattern)
	if err != nil {
		return regexp.MustCompile(defaultNarrativeRegex), err
	}
	return re, nil
}

const defaultNarrativeRegex = `(?i)情色|色情|床笫|亲密|性爱|小说|短篇|描写的?片段|角色?扮演|erotic|explicit|nsfw|roleplay|intimate`

// SanitizeJB normalizes the JB config: trims paths, drops empty wordlist
// entries, and validates the narrative regex so a bad pattern surfaces at
// load time rather than mid-request.
func (cfg *Config) SanitizeJB() {
	if cfg == nil {
		return
	}
	cfg.JB.CorpusFile = strings.TrimSpace(cfg.JB.CorpusFile)
	cfg.JB.SpecFile = strings.TrimSpace(cfg.JB.SpecFile)
	cfg.JB.OMNFile = strings.TrimSpace(cfg.JB.OMNFile)
	cfg.JB.NarrativeRegex = strings.TrimSpace(cfg.JB.NarrativeRegex)
	if len(cfg.JB.Wordlists) > 0 {
		clean := make(map[string]string, len(cfg.JB.Wordlists))
		for k, v := range cfg.JB.Wordlists {
			k = strings.ToLower(strings.TrimSpace(k))
			v = strings.TrimSpace(v)
			if k == "" || v == "" {
				continue
			}
			clean[k] = v
		}
		cfg.JB.Wordlists = clean
	}
}
