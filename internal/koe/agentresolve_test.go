//go:build darwin && !ios && cgo

package koe

import "testing"

func fixtureAgents() []AgentSummary {
	return []AgentSummary{
		{Slug: "finance", DisplayName: "金融分析 agent", Description: map[string]string{"en": "stock and market analysis"}},
		{Slug: "default", DisplayName: "Kocoro"},
		{Slug: "legal", DisplayName: "法务 agent", Description: map[string]string{"en": "contracts"}},
	}
}

func TestResolveExactSlug(t *testing.T) {
	r := NewAgentResolver(fixtureAgents(), NoopSemanticMatcher{})
	got := r.Resolve("finance")
	if got.Status != ResolveResolved || got.Slug != "finance" {
		t.Errorf("Resolve(finance) = %+v, want Resolved/finance", got)
	}
}

func TestResolveDisplayNameSubstring(t *testing.T) {
	r := NewAgentResolver(fixtureAgents(), NoopSemanticMatcher{})
	got := r.Resolve("金融") // ⊂ "金融分析 agent"
	if got.Status != ResolveResolved || got.Slug != "finance" {
		t.Errorf("Resolve(金融) = %+v, want Resolved/finance", got)
	}
}

func TestResolveAmbiguous(t *testing.T) {
	agents := append(fixtureAgents(), AgentSummary{Slug: "finance-jp", DisplayName: "金融日本 agent"})
	r := NewAgentResolver(agents, NoopSemanticMatcher{})
	got := r.Resolve("金融") // now ⊂ two display names
	if got.Status != ResolveAmbiguous {
		t.Fatalf("Resolve(金融) status = %v, want Ambiguous", got.Status)
	}
	if len(got.Candidates) != 2 {
		t.Errorf("candidates = %v, want 2", got.Candidates)
	}
}

func TestResolveNotFound(t *testing.T) {
	r := NewAgentResolver(fixtureAgents(), NoopSemanticMatcher{})
	got := r.Resolve("nonexistent xyz")
	if got.Status != ResolveNotFound {
		t.Errorf("Resolve(nonexistent) = %v, want NotFound", got.Status)
	}
}

func TestResolveSemanticHook(t *testing.T) {
	// A stub matcher that maps any reference to "legal" proves the ③ seam fires
	// only after ① and ② miss.
	r := NewAgentResolver(fixtureAgents(), funcSemanticMatcher(func(ref string, a []AgentSummary) string { return "legal" }))
	got := r.Resolve("the lawyer one")
	if got.Status != ResolveResolved || got.Slug != "legal" {
		t.Errorf("semantic Resolve = %+v, want Resolved/legal", got)
	}
}

func TestResolveHyphenSpaceEquivalence(t *testing.T) {
	// Real agents often have hyphenated slugs and no display name (display_name
	// falls back to the slug); a user naturally says the name with spaces. Live
	// bug: "investment analyst" did not resolve to slug "investment-analyst".
	agents := []AgentSummary{{Slug: "investment-analyst", DisplayName: "investment-analyst"}}
	r := NewAgentResolver(agents, NoopSemanticMatcher{})
	for _, ref := range []string{"investment analyst", "investment-analyst", "Investment Analyst"} {
		got := r.Resolve(ref)
		if got.Status != ResolveResolved || got.Slug != "investment-analyst" {
			t.Errorf("Resolve(%q) = %+v, want Resolved/investment-analyst", ref, got)
		}
	}
}

func TestResolveStripsAgentFiller(t *testing.T) {
	// A user says "use the investment agent"; the trailing filler word must not
	// break the match.
	agents := []AgentSummary{{Slug: "investment-analyst", DisplayName: "investment-analyst"}}
	r := NewAgentResolver(agents, NoopSemanticMatcher{})
	for _, ref := range []string{"investment agent", "the investment agent", "investment"} {
		got := r.Resolve(ref)
		if got.Status != ResolveResolved || got.Slug != "investment-analyst" {
			t.Errorf("Resolve(%q) = %+v, want Resolved/investment-analyst", ref, got)
		}
	}
}

func TestResolveDefaultRevertsToImplicit(t *testing.T) {
	// "default" is not a named registry entry (agents are academic-writer,
	// investment-analyst, ...) — it is the implicit default (empty slug). Without
	// this a user could switch TO a specialist by voice but never back to default.
	agents := []AgentSummary{{Slug: "investment-analyst", DisplayName: "investment-analyst"}}
	r := NewAgentResolver(agents, NoopSemanticMatcher{})
	for _, ref := range []string{"default", "默认", "the default agent", "默认 agent", "Kocoro", "Kocoro agent"} {
		got := r.Resolve(ref)
		if got.Status != ResolveResolved || got.Slug != "" {
			t.Errorf("Resolve(%q) = %+v, want Resolved/\"\" (implicit default)", ref, got)
		}
	}
}

func TestResolveLiteralDefaultSlugStillWins(t *testing.T) {
	// If a real agent is literally named "default", the exact-slug match wins over
	// the implicit-default shortcut.
	agents := []AgentSummary{{Slug: "default", DisplayName: "Kocoro"}}
	r := NewAgentResolver(agents, NoopSemanticMatcher{})
	if got := r.Resolve("default"); got.Status != ResolveResolved || got.Slug != "default" {
		t.Errorf("Resolve(default) = %+v, want the literal default slug", got)
	}
}

type funcSemanticMatcher func(ref string, agents []AgentSummary) string

func (f funcSemanticMatcher) Match(ref string, agents []AgentSummary) string { return f(ref, agents) }
