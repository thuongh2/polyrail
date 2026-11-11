package gateway

import (
	"github.com/maximhq/bifrost/core/schemas"

	"github.com/polyrail/polyrail/internal/store"
)

// tokens is the billable token split of one response (D32). in and out are totals as Bifrost
// reports them: prompt tokens include cache reads and writes (Bifrost folds Anthropic's separate
// cache_read/cache_creation counts into prompt_tokens), completion tokens include reasoning.
type tokens struct{ in, out, read, write, reasoning int }

func chatTokens(u *schemas.BifrostLLMUsage) *tokens {
	if u == nil {
		return nil
	}
	t := &tokens{in: u.PromptTokens, out: u.CompletionTokens}
	if d := u.PromptTokensDetails; d != nil {
		t.read, t.write = d.CachedReadTokens, d.CachedWriteTokens
	}
	if d := u.CompletionTokensDetails; d != nil {
		t.reasoning = d.ReasoningTokens
	}
	if t.reasoning == 0 {
		t.reasoning = u.ReasoningTokens // SGLang's top-level spelling
	}
	return t
}

func respTokens(u *schemas.ResponsesResponseUsage) *tokens {
	if u == nil {
		return nil
	}
	t := &tokens{in: u.InputTokens, out: u.OutputTokens}
	if d := u.InputTokensDetails; d != nil {
		t.read, t.write = d.CachedReadTokens, d.CachedWriteTokens
	}
	if d := u.OutputTokensDetails; d != nil {
		t.reasoning = d.ReasoningTokens
	}
	return t
}

// into copies the counts onto a request log (no-op for nil).
func (t *tokens) into(l *store.RequestLog) {
	if t != nil {
		l.InTokens, l.OutTokens, l.CacheReadTokens, l.CacheWriteTokens, l.ReasoningTokens = t.in, t.out, t.read, t.write, t.reasoning
	}
}

// logCost is the token cost of a request log in micro-USD (the single token-pricing formula):
// uncached prompt tokens at the input price, cache reads and writes at their prices (the input
// price when unset), completion tokens (reasoning included, so never added twice) at the output
// price; one rounding to the nearest micro-USD (D11).
func logCost(m store.Model, l store.RequestLog) int64 {
	read, write := int64(l.CacheReadTokens), int64(l.CacheWriteTokens)
	uncached := max(int64(l.InTokens)-read-write, 0) // a provider reporting more cached than prompt tokens
	rp, wp := m.CacheReadPrice, m.CacheWritePrice
	if rp == 0 {
		rp = m.InputPrice
	}
	if wp == 0 {
		wp = m.InputPrice
	}
	return (uncached*m.InputPrice + read*rp + write*wp + int64(l.OutTokens)*m.OutputPrice + 500_000) / 1_000_000 // nearest micro-USD: unbiased over many requests
}

func cost(m store.Model, in, out int) int64 {
	return logCost(m, store.RequestLog{InTokens: in, OutTokens: out})
}
