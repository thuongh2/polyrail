package gateway

import (
	"fmt"
	"regexp"
	"slices"
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

// applyGuardrails enforces an org's guardrails on input text. It returns a user-facing reason
// when the request must be blocked.
func applyGuardrails(org store.Org, segs []*string) (string, error) {
	g := org.Guardrails
	res, err := compiled(org.ID, g)
	if err != nil {
		return "", err
	}
	total := 0
	for _, s := range segs {
		total += len(*s)
		for _, re := range res {
			if re.MatchString(*s) {
				return "input matches a blocked pattern", nil
			}
		}
	}
	if g.MaxInputChars > 0 && total > g.MaxInputChars {
		return fmt.Sprintf("input is %d characters, limit is %d", total, g.MaxInputChars), nil
	}
	if g.MaskPII {
		for _, s := range segs {
			*s = rePhone.ReplaceAllString(reEmail.ReplaceAllString(*s, "[email]"), "[phone]")
		}
	}
	return "", nil
}
