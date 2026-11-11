package gateway

import (
	"fmt"
	"regexp"
	"slices"
	"strings"
	"sync"

	"github.com/agent-gateway/agent-gateway/internal/store"
	"github.com/maximhq/bifrost/core/schemas"
)

var (
	reEmail = regexp.MustCompile(`[A-Za-z0-9._%+-]+@[A-Za-z0-9.-]+\.[A-Za-z]{2,}`)
	rePhone = regexp.MustCompile(`\+?\d[\d\s().-]{7,}\d`)
)

// guardCache holds compiled blocked patterns per org, keyed by the pattern list itself.
var guardCache sync.Map // string(org id) -> compiledGuard

type compiledGuard struct {
	src []string
	res []*regexp.Regexp
}

func compiled(orgID string, g store.Guardrails) ([]*regexp.Regexp, error) {
	if c, ok := guardCache.Load(orgID); ok && slices.Equal(c.(compiledGuard).src, g.BlockedPatterns) {
		return c.(compiledGuard).res, nil
	}
	res := make([]*regexp.Regexp, 0, len(g.BlockedPatterns))
	for _, p := range g.BlockedPatterns {
		re, err := regexp.Compile(p)
		if err != nil {
			return nil, fmt.Errorf("invalid blocked pattern %q: %w", p, err)
		}
		res = append(res, re)
	}
	guardCache.Store(orgID, compiledGuard{src: g.BlockedPatterns, res: res})
	return res, nil
}

// texts returns pointers to every text segment of the messages, so guards can read and rewrite them.
func texts(msgs []schemas.ChatMessage) []*string {
	var out []*string
	for i := range msgs {
		c := msgs[i].Content
		if c == nil {
			continue
		}
		if c.ContentStr != nil {
			out = append(out, c.ContentStr)
		}
		for j := range c.ContentBlocks {
			if c.ContentBlocks[j].Text != nil {
				out = append(out, c.ContentBlocks[j].Text)
			}
		}
	}
	return out
}

// guardInput is the text one request exposes to guardrails. all gets blocked patterns, max chars,
// secret and PII masking; user (user-role text) gets the prompt-injection scan; prose (chat and
// Responses message text, never tool results) gets lossless compression. dropped counts chars of
// system messages chatGuard already removed as duplicates.
type guardInput struct {
	all, user, prose []*string
	dropped          int
}

// guardVerdict is the outcome: a non-empty reason blocks with 400 and the error code in code.
type guardVerdict struct {
	reason, code string
	injection    bool // a prompt-injection pattern matched (warn or block)
	saved        int  // chars removed by compression
}

// applyGuardrails enforces an org's guardrails on input text, rewriting it in place.
func applyGuardrails(org store.Org, in guardInput) (guardVerdict, error) {
	g := org.Guardrails
	var v guardVerdict
	res, err := compiled(org.ID, g)
	if err != nil {
		return v, err
	}
	if g.Compression == "lossless" {
		v.saved = in.dropped
		for _, s := range in.prose {
			c := compressText(*s)
			v.saved += len(*s) - len(c)
			*s = c
		}
	}
	total := 0
	for _, s := range in.all {
		total += len(*s)
		for _, re := range res {
			if re.MatchString(*s) {
				v.reason, v.code = "input matches a blocked pattern", "guardrail_blocked"
				return v, nil
			}
		}
	}
	if g.MaxInputChars > 0 && total > g.MaxInputChars {
		v.reason, v.code = fmt.Sprintf("input is %d characters, limit is %d", total, g.MaxInputChars), "guardrail_blocked"
		return v, nil
	}
	if g.PromptInjection != "" && injected(in.user) {
		v.injection = true
		if g.PromptInjection == "block" {
			v.reason, v.code = "input looks like a prompt injection", "prompt_injection"
			return v, nil
		}
	}
	for _, s := range in.all {
		if g.MaskSecrets {
			*s = maskSecrets(*s)
		}
		if g.MaskPII {
			*s = rePhone.ReplaceAllString(reEmail.ReplaceAllString(*s, "[email]"), "[phone]")
		}
	}
	return v, nil
}

// secretPatterns are well-known credential shapes, applied in order (sk-ant- before sk-).
// Generic "password=" / "api_key=" assignments are not masked: in code they are mostly
// `os.getenv(...)` calls and config templates, so masking them would break prompts.
var secretPatterns = []struct {
	typ string
	re  *regexp.Regexp
}{
	{"private_key", regexp.MustCompile(`(?s)-----BEGIN [A-Z ]*PRIVATE KEY-----.*?-----END [A-Z ]*PRIVATE KEY-----`)},
	{"anthropic", regexp.MustCompile(`\bsk-ant-[A-Za-z0-9_-]{20,}`)},
	{"openai", regexp.MustCompile(`\bsk-(?:proj-)?[A-Za-z0-9_-]{20,}`)},
	{"stripe", regexp.MustCompile(`\b[sr]k_live_[A-Za-z0-9]{16,}`)},
	{"aws", regexp.MustCompile(`\b(?:AKIA|ASIA)[0-9A-Z]{16}\b`)},
	{"github", regexp.MustCompile(`\b(?:gh[pousr]_[A-Za-z0-9]{36,}|github_pat_[A-Za-z0-9_]{22,})`)},
	{"slack", regexp.MustCompile(`\bxox[baprs]-[A-Za-z0-9-]{10,}`)},
	{"google", regexp.MustCompile(`\bAIza[0-9A-Za-z_-]{35}`)},
	{"jwt", regexp.MustCompile(`\beyJ[A-Za-z0-9_-]{8,}\.eyJ[A-Za-z0-9_-]{8,}\.[A-Za-z0-9_-]{8,}`)},
}

func maskSecrets(s string) string {
	for _, p := range secretPatterns {
		s = p.re.ReplaceAllLiteralString(s, "[REDACTED:"+p.typ+"]")
	}
	return s
}

// injectionScanBytes bounds the prompt-injection scan per request.
const injectionScanBytes = 16 << 10

// reInjection holds high-signal jailbreak phrasings only; broad matches would block normal
// questions about prompts.
var reInjection = regexp.MustCompile(`(?i)\b(?:` +
	`(?:ignore|disregard|forget|override)\s+(?:(?:all|any|the|your|my|of)\s+)*(?:previous|prior|above|earlier|preceding|system|original|developer)\s+(?:instructions|prompts?|rules|guidelines|directives|messages?)` +
	`|(?:ignore|disregard)\s+(?:all|any|every)\s+(?:instructions|rules|guidelines)` +
	`|you\s+are\s+now\s+(?:DAN|in\s+developer\s+mode|jailbroken|unrestricted|unfiltered)` +
	`|(?:enable|activate|enter)\s+(?:DAN|developer|god|jailbreak)\s+mode` +
	`|do\s+anything\s+now` +
	`|(?:reveal|show|print|repeat|output|leak)\s+(?:me\s+)?(?:your|the)\s+(?:(?:full|entire|hidden|original|initial)\s+)?(?:system\s+(?:prompt|message)|(?:initial|hidden)\s+instructions)` +
	`|pretend\s+(?:that\s+)?you\s+have\s+no\s+(?:restrictions|rules|guidelines|filters)` +
	`)`)

// injected reports whether the first injectionScanBytes of segs match reInjection.
func injected(segs []*string) bool {
	budget := injectionScanBytes
	for _, s := range segs {
		t := *s
		if len(t) > budget {
			t = t[:budget]
		}
		if reInjection.MatchString(t) {
			return true
		}
		if budget -= len(t); budget <= 0 {
			return false
		}
	}
	return false
}

var reSpaces = regexp.MustCompile(`[ \t]{2,}`)

func isFence(l string) bool { return strings.HasPrefix(strings.TrimLeft(l, " \t"), "```") }

// compressText is lossless whitespace compression: outside ``` fences it trims trailing
// whitespace, collapses inner runs of spaces/tabs (leading indentation is kept) and allows at
// most one blank line in a row. Fenced code, everything from an unclosed fence on, and text
// that looks like JSON are left as they are. It is idempotent.
func compressText(s string) string {
	if t := strings.TrimSpace(s); strings.HasPrefix(t, "{") || strings.HasPrefix(t, "[") {
		return s
	}
	lines := strings.Split(s, "\n")
	stop, open := len(lines), -1
	for i, l := range lines {
		if isFence(l) {
			if open < 0 {
				open = i
			} else {
				open = -1
			}
		}
	}
	if open >= 0 {
		stop = open
	}
	out := make([]string, 0, len(lines))
	inFence, blank := false, false
	for i, l := range lines {
		if i >= stop {
			out = append(out, lines[i:]...)
			break
		}
		fence := isFence(l)
		if !inFence && !fence {
			l = strings.TrimRight(l, " \t\r")
			body := strings.TrimLeft(l, " \t")
			l = l[:len(l)-len(body)] + reSpaces.ReplaceAllString(body, " ")
			if l == "" && blank && i < len(lines)-1 {
				continue
			}
			blank = l == ""
		} else {
			blank = false
		}
		if fence {
			inFence = !inFence
		}
		out = append(out, l)
	}
	return strings.Join(out, "\n")
}

// chatGuard collects a chat request's guardrail input. With compression on it first drops
// exact-duplicate system/developer messages (string content, compared after compression).
// Tool messages and assistant tool_calls are never compressed.
func chatGuard(g store.Guardrails, msgs []schemas.ChatMessage) ([]schemas.ChatMessage, guardInput) {
	var in guardInput
	if g.Compression == "lossless" {
		seen := map[string]bool{}
		kept := make([]schemas.ChatMessage, 0, len(msgs))
		for _, m := range msgs {
			if (m.Role == schemas.ChatMessageRoleSystem || m.Role == schemas.ChatMessageRoleDeveloper) && m.Content != nil && m.Content.ContentStr != nil {
				k := string(m.Role) + "\x00" + compressText(*m.Content.ContentStr)
				if seen[k] {
					in.dropped += len(*m.Content.ContentStr)
					continue
				}
				seen[k] = true
			}
			kept = append(kept, m)
		}
		msgs = kept
	}
	for i := range msgs {
		segs := texts(msgs[i : i+1])
		in.all = append(in.all, segs...)
		if msgs[i].Role == schemas.ChatMessageRoleUser {
			in.user = append(in.user, segs...)
		}
		if msgs[i].Role != schemas.ChatMessageRoleTool {
			in.prose = append(in.prose, segs...)
		}
	}
	return msgs, in
}
