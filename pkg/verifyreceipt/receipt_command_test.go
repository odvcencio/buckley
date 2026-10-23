package verifyreceipt

import (
	"strings"
	"testing"
)

func TestReceipt_CommandRedactsCredentialsWithoutChangingIdentity(t *testing.T) {
	secret := "sk-" + strings.Repeat("x", 40)
	r := Receipt{Argv: []string{"env", "OPENAI_API_KEY=" + secret, "go", "test", "./..."}}
	if command := r.Command(); strings.Contains(command, secret) {
		t.Fatal("credential exposed in review command label")
	}
	if !strings.Contains(r.Argv[1], secret) {
		t.Fatal("display formatting mutated recorded command identity")
	}
}
