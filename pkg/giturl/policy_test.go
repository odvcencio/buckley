package giturl

import "testing"

func TestValidateCloneURL_AllowsWhenPolicyEmpty(t *testing.T) {
	if err := ValidateCloneURL(ClonePolicy{}, "https://github.com/org/repo.git"); err != nil {
		t.Fatalf("expected allowed, got %v", err)
	}
}

func TestValidateCloneURL_RejectsDisallowedScheme(t *testing.T) {
	policy := ClonePolicy{AllowedSchemes: []string{"ssh"}}
	if err := ValidateCloneURL(policy, "https://github.com/org/repo.git"); err == nil {
		t.Fatalf("expected rejection")
	}
}

func TestValidateCloneURL_RejectsHostNotInAllowList(t *testing.T) {
	policy := ClonePolicy{AllowedHosts: []string{"github.com"}}
	if err := ValidateCloneURL(policy, "https://gitlab.com/org/repo.git"); err == nil {
		t.Fatalf("expected rejection")
	}
}

func TestValidateCloneURL_RejectsDeniedHost(t *testing.T) {
	policy := ClonePolicy{DeniedHosts: []string{"github.com"}}
	if err := ValidateCloneURL(policy, "https://github.com/org/repo.git"); err == nil {
		t.Fatalf("expected rejection")
	}
}

func TestValidateCloneURL_DenyPrivateNetworksRejectsLoopbackIP(t *testing.T) {
	policy := ClonePolicy{DenyPrivateNetworks: true}
	if err := ValidateCloneURL(policy, "https://127.0.0.1/org/repo.git"); err == nil {
		t.Fatalf("expected rejection")
	}
}

func TestValidateCloneURL_AllowHostsDisablesPrivateNetworkCheck(t *testing.T) {
	policy := ClonePolicy{
		DenyPrivateNetworks: true,
		AllowedHosts:        []string{"127.0.0.1"},
	}
	if err := ValidateCloneURL(policy, "https://127.0.0.1/org/repo.git"); err != nil {
		t.Fatalf("expected allowed, got %v", err)
	}
}

func TestValidateCloneURL_SCPSyntaxRespectsPolicy(t *testing.T) {
	if err := ValidateCloneURL(ClonePolicy{DenySCPSyntax: true}, "git@github.com:org/repo.git"); err == nil {
		t.Fatalf("expected scp syntax rejection")
	}
	if err := ValidateCloneURL(ClonePolicy{}, "git@github.com:org/repo.git"); err != nil {
		t.Fatalf("expected scp syntax allowed, got %v", err)
	}
}
