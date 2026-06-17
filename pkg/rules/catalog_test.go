package rules

import "testing"

func TestFactContractsIncludesCoreDomains(t *testing.T) {
	contracts := FactContracts()
	if len(contracts) < 20 {
		t.Fatalf("got %d contracts, want at least 20", len(contracts))
	}
	byDomain := map[string]FactContract{}
	for _, contract := range contracts {
		byDomain[contract.Domain] = contract
		if len(contract.Facts) == 0 {
			t.Fatalf("domain %q has no facts", contract.Domain)
		}
	}
	for _, domain := range []string{"approval", "risk", "tool_budget", "permissions/sandbox", "cost/budgets", "session/compaction"} {
		if _, ok := byDomain[domain]; !ok {
			t.Fatalf("missing contract for domain %q", domain)
		}
	}
	assertFact(t, byDomain["approval"], "approval.mode")
	assertFact(t, byDomain["approval"], "risk.level")
	assertFact(t, byDomain["routing"], "task.phase")
	assertFact(t, byDomain["routing"], "model.supports_reasoning")
	assertFact(t, byDomain["tool_budget"], "agent.max_tool_calls")
	assertFact(t, byDomain["permissions/sandbox"], "risk_score")
}

func TestFactContractsForDomain(t *testing.T) {
	contracts := FactContractsForDomain("approval")
	if len(contracts) != 1 {
		t.Fatalf("got %d contracts, want 1", len(contracts))
	}
	if contracts[0].Domain != "approval" {
		t.Fatalf("domain=%q want approval", contracts[0].Domain)
	}
	if got := FactContractsForDomain("missing"); len(got) != 0 {
		t.Fatalf("missing domain returned %+v", got)
	}
}

func assertFact(t *testing.T, contract FactContract, key string) {
	t.Helper()
	for _, fact := range contract.Facts {
		if fact.Key == key {
			return
		}
	}
	t.Fatalf("domain %q missing fact %q in %+v", contract.Domain, key, contract.Facts)
}
