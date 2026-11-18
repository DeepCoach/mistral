package main

import (
	"crypto/sha256"
	"encoding/hex"
	"strings"
)

// promptCacheKeyField is the Mistral chat completions field that hints which
// requests are likely to share a prefix. Without it the upstream scatters
// requests across cache nodes and long agent conversations rarely hit.
const promptCacheKeyField = "prompt_cache_key"

// promptCacheAffinityHeader is Mistral's load-balancer routing hint. The prompt
// cache lives on individual inference pods, and prompt_cache_key alone only
// raises the chance of landing on the pod that holds the prefix. Mistral's own
// Vibe client sends this header with the session's root identity so every turn
// reaches the same pod; without it long agent loops drop cache whenever the
// balancer picks another backend.
const promptCacheAffinityHeader = "x-affinity"

// promptCacheRootKeys lists the host-provided session identities in authority
// order for the cache routing identity. The root comes first because that is
// exactly what the official Vibe client pins: a fork routes to its family's
// root pod while telemetry keeps the child's own session id. The remaining
// fields are the fallback for a request the host did not tag with a root.
var promptCacheRootKeys = []string{
	"root_session_id",
	"canonical_root_session_id",
	"parent_root_session_id",
}

// promptCacheSessionKeys lists the host-provided session identities that are
// only a last resort for the content-root key: a routed request's
// canonical_session_id is the LCP affinity identity, which the host documents
// as routing-only and which changes when the matcher rebinds a conversation
// (compaction, forks).
var promptCacheSessionKeys = []string{
	"canonical_session_id",
	"execution_session_id",
	"derived_session_id",
}

// cacheRoutings carries the two independent cache signals a request needs. They
// are deliberately separate, matching the official Vibe client:
//
//   - Affinity is the request header that actually decides which inference pod
//     serves the request. Its value is the conversation family's root session
//     id, forwarded verbatim when the client already sends one.
//   - Key is the body field that only raises the odds of reusing a prefix. It
//     is derived from the conversation content so compaction replays and turns
//     of one conversation keep the same value.
//
// Conflating them, as this plugin used to, made the routing identity follow the
// prompt content: a fork or a client that only sent a cache key lost the pod
// affinity the official client would have kept.
type cacheRoutings struct {
	Key            string
	KeySource      string
	Affinity       string
	AffinitySource string
}

// applyPromptCacheRouting adds prompt_cache_key to the upstream body and
// resolves the x-affinity header value. An explicit client key wins over a
// derived one. The affinity keeps the client's own value when it sends the
// header; otherwise it is the session root identity from the host metadata, and
// only failing that the same value as the cache key so a routed request still
// has a stable routing identity.
func applyPromptCacheRouting(payload, original []byte, metadata map[string]any, headers map[string][]string, model string) ([]byte, cacheRoutings) {
	var routing cacheRoutings
	var doc map[string]any
	if err := decodeJSON(payload, &doc); err != nil {
		return payload, routing
	}
	if existing, _ := doc[promptCacheKeyField].(string); strings.TrimSpace(existing) != "" {
		routing.Key, routing.KeySource = strings.TrimSpace(existing), "client"
	} else if clientKey := clientPromptCacheKey(original); clientKey != "" {
		routing.Key, routing.KeySource = clientKey, "client"
	} else {
		routing.Key, routing.KeySource = derivePromptCacheKey(doc, metadata, model)
	}
	routing.Affinity, routing.AffinitySource = resolveCacheAffinity(metadata, headers, routing)
	if routing.Key == "" {
		return payload, routing
	}
	if existing, _ := doc[promptCacheKeyField].(string); strings.TrimSpace(existing) == routing.Key {
		return payload, routing
	}
	doc[promptCacheKeyField] = routing.Key
	out, err := encodeJSON(doc)
	if err != nil {
		return payload, routing
	}
	return out, routing
}

// resolveCacheAffinity picks the value for the x-affinity header. A client that
// already sends the header speaks the official protocol, so its value is
// forwarded unchanged rather than rehashed: the Vibe CLI sends the root session
// id, which is the one identity that survives forks and compaction.
func resolveCacheAffinity(metadata map[string]any, headers map[string][]string, routing cacheRoutings) (value, source string) {
	if client := headerValue(headers, promptCacheAffinityHeader); client != "" {
		return affinityHeaderValue(client), "client"
	}
	for _, name := range promptCacheRootKeys {
		raw, ok := metadata[name].(string)
		if !ok || strings.TrimSpace(raw) == "" {
			continue
		}
		if resolved := affinityHeaderValue(raw); resolved != "" {
			return resolved, "session"
		}
	}
	// No root identity to route on: fall back to the content root so every turn
	// of the conversation still lands on the pod that already holds its prefix.
	if routing.Key != "" {
		return affinityHeaderValue(routing.Key), routing.KeySource
	}
	return "", ""
}

// clientPromptCacheKey reads an explicit key from the raw client body. The
// plugin converts some protocols into chat completions and would otherwise drop
// the field, so the original request is the last place to look.
func clientPromptCacheKey(original []byte) string {
	if len(original) == 0 {
		return ""
	}
	var doc map[string]any
	if err := decodeJSON(original, &doc); err != nil {
		return ""
	}
	key, _ := doc[promptCacheKeyField].(string)
	return strings.TrimSpace(key)
}

// derivePromptCacheKey builds the key from the conversation root. The root is
// the concatenated leading instructions and the first user message, which is
// exactly the part of the prompt every turn of one conversation shares. Two
// conversations get different keys even when they share a system prompt, and a
// compaction replay of the same history keeps the original key.
func derivePromptCacheKey(doc map[string]any, metadata map[string]any, model string) (key, source string) {
	instructions, firstUser := leadingPromptParts(doc)
	if instructions != "" || firstUser != "" {
		return hashPromptCacheKey(model, "root", instructions+"\x00"+firstUser), "root"
	}
	for _, name := range promptCacheSessionKeys {
		if value, ok := metadata[name].(string); ok && strings.TrimSpace(value) != "" {
			return hashPromptCacheKey(model, name, value), "session"
		}
	}
	return "", ""
}

// leadingPromptParts returns the concatenated system/developer instructions and
// the first user message. Instructions define the shared prefix; the first user
// message identifies the conversation.
func leadingPromptParts(doc map[string]any) (instructions, firstUser string) {
	messages, _ := doc["messages"].([]any)
	parts := make([]string, 0, 4)
	for _, item := range messages {
		message, ok := item.(map[string]any)
		if !ok {
			continue
		}
		role, _ := message["role"].(string)
		switch role {
		case "system", "developer":
			if text := textForCacheKey(message["content"]); text != "" {
				parts = append(parts, text)
			}
		case "user":
			if firstUser == "" {
				firstUser = textForCacheKey(message["content"])
			}
		}
	}
	return strings.Join(parts, "\x00"), firstUser
}

func hashPromptCacheKey(model, kind, identity string) string {
	identity = strings.Join([]string{
		"cli-proxy-api:mistral:prompt-cache",
		strings.ToLower(strings.TrimSpace(model)),
		kind,
		strings.TrimSpace(identity),
	}, "\x00")
	sum := sha256.Sum256([]byte(identity))
	// 20 bytes of SHA-256 keeps the hint short without making collisions likely.
	return "mstrl-" + hex.EncodeToString(sum[:20])
}

// affinityHeaderValue turns a cache key into a valid header value. Client keys
// can be arbitrary strings, so anything outside a conservative token set is
// replaced by a hash.
func affinityHeaderValue(key string) string {
	key = strings.TrimSpace(key)
	if key == "" {
		return ""
	}
	if len(key) <= 128 && isHeaderToken(key) {
		return key
	}
	sum := sha256.Sum256([]byte("cli-proxy-api:mistral:affinity-header\x00" + key))
	return "mstrl-" + hex.EncodeToString(sum[:20])
}

func isHeaderToken(value string) bool {
	for _, char := range value {
		if char < 0x21 || char > 0x7e {
			return false
		}
	}
	return true
}

// headerValue looks up a header case-insensitively. The host forwards the
// client request headers verbatim, so casing is whatever the client sent.
func headerValue(headers map[string][]string, name string) string {
	for key, values := range headers {
		if !strings.EqualFold(strings.TrimSpace(key), name) {
			continue
		}
		for _, value := range values {
			if trimmed := strings.TrimSpace(value); trimmed != "" {
				return trimmed
			}
		}
	}
	return ""
}

func textForCacheKey(value any) string {
	switch typed := value.(type) {
	case string:
		return typed
	case []any:
		var buf strings.Builder
		for _, item := range typed {
			part, ok := item.(map[string]any)
			if !ok {
				continue
			}
			if text, ok := part["text"].(string); ok {
				buf.WriteString(text)
			}
		}
		return buf.String()
	default:
		return ""
	}
}
