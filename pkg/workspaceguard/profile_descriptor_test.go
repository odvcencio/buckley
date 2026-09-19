package workspaceguard

import (
	"bytes"
	"encoding/json"
	"reflect"
	"strings"
	"testing"
)

func TestLaunchProfileDescriptor_CanonicalContracts(t *testing.T) {
	for _, id := range []string{"gsxmail", "gosx", "tqwebp"} {
		t.Run(id, func(t *testing.T) {
			descriptor, err := ResolveLaunchProfileDescriptor(id)
			if err != nil {
				t.Fatal(err)
			}
			if descriptor.Provider != "openrouter" || descriptor.Model != "stealth/ox-alpha" || descriptor.ReasoningEffort != "max" || descriptor.Privacy.RetentionMode != "non_zdr" || descriptor.Privacy.ZDR || descriptor.Privacy.DataCollection != "deny" || descriptor.Privacy.AllowFallbacks || !descriptor.Privacy.RequireParameters || !descriptor.License.Required || !descriptor.License.RootOnly || descriptor.ProviderPostAttempts != 1 || descriptor.ManagerAffordabilityAttempts != 1 || descriptor.RetryOwner != "dapr" || descriptor.Enforced || descriptor.State != LaunchStateAdmissionPending {
				t.Fatalf("descriptor = %+v", descriptor)
			}
			if descriptor.PriceGuard.MaxPrice != (LaunchMaxPrice{Prompt: "0", Completion: "0", Request: "0", Image: "0"}) || descriptor.PriceGuard.CatalogTTLMS != LaunchCatalogTTL.Milliseconds() {
				t.Fatalf("price guard = %+v", descriptor.PriceGuard)
			}
			canonical, err := descriptor.CanonicalBytes()
			if err != nil {
				t.Fatal(err)
			}
			digest, err := descriptor.Digest()
			if err != nil || len(digest) != 64 {
				t.Fatalf("digest = %q, %v", digest, err)
			}
			decoded, err := DecodeLaunchProfileDescriptor(canonical)
			if err != nil || !reflect.DeepEqual(decoded, descriptor) {
				t.Fatalf("round trip = %+v, %v", decoded, err)
			}
			var reordered map[string]any
			if err := json.Unmarshal(canonical, &reordered); err != nil {
				t.Fatal(err)
			}
			mapBytes, err := json.Marshal(reordered)
			if err != nil {
				t.Fatal(err)
			}
			fromMap, err := DecodeLaunchProfileDescriptor(mapBytes)
			if err != nil {
				t.Fatalf("map-order decode: %v", err)
			}
			mapCanonical, _ := fromMap.CanonicalBytes()
			if !bytes.Equal(mapCanonical, canonical) {
				t.Fatalf("canonical bytes changed across map order\nwant=%s\n got=%s", canonical, mapCanonical)
			}
		})
	}
}

func TestLaunchProfileDescriptor_RejectsDriftUnknownMissingAndTrailing(t *testing.T) {
	descriptor, err := ResolveLaunchProfileDescriptor("gsxmail")
	if err != nil {
		t.Fatal(err)
	}
	mutations := []func(*LaunchProfileDescriptor){
		func(d *LaunchProfileDescriptor) { d.Schema = "buckley.launch.profile.v2" },
		func(d *LaunchProfileDescriptor) { d.Provider = "openai" },
		func(d *LaunchProfileDescriptor) { d.Model = "ox-alpha" },
		func(d *LaunchProfileDescriptor) { d.ReasoningEffort = "high" },
		func(d *LaunchProfileDescriptor) { d.Privacy.ZDR = true },
		func(d *LaunchProfileDescriptor) { d.Privacy.DataCollection = "allow" },
		func(d *LaunchProfileDescriptor) { d.Privacy.AllowFallbacks = true },
		func(d *LaunchProfileDescriptor) { d.Privacy.RequireParameters = false },
		func(d *LaunchProfileDescriptor) { d.License.AllowedIDs = []string{"MIT", "Apache-2.0"} },
		func(d *LaunchProfileDescriptor) { d.Limits.ModelRequests++ },
		func(d *LaunchProfileDescriptor) { d.PriceGuard.MaxPrice.Prompt = "0.0" },
		func(d *LaunchProfileDescriptor) { d.ProviderPostAttempts++ },
		func(d *LaunchProfileDescriptor) { d.ManagerAffordabilityAttempts++ },
		func(d *LaunchProfileDescriptor) { d.RetryOwner = "provider" },
		func(d *LaunchProfileDescriptor) { d.Enforced = true },
	}
	for index, mutate := range mutations {
		changed := descriptor
		changed.License.AllowedIDs = append([]string(nil), descriptor.License.AllowedIDs...)
		mutate(&changed)
		if err := changed.Validate(); err == nil {
			t.Fatalf("mutation %d accepted: %+v", index, changed)
		}
	}

	canonical, _ := descriptor.CanonicalBytes()
	var object map[string]any
	if err := json.Unmarshal(canonical, &object); err != nil {
		t.Fatal(err)
	}
	delete(object, "enforced")
	missing, _ := json.Marshal(object)
	if _, err := DecodeLaunchProfileDescriptor(missing); err == nil {
		t.Fatal("missing false field accepted")
	}
	object["enforced"] = false
	object["unknown"] = true
	unknown, _ := json.Marshal(object)
	if _, err := DecodeLaunchProfileDescriptor(unknown); err == nil {
		t.Fatal("unknown field accepted")
	}
	if _, err := DecodeLaunchProfileDescriptor(append(canonical, []byte("\n{}")...)); err == nil {
		t.Fatal("trailing object accepted")
	}
	duplicate := strings.Replace(string(canonical), `"schema":"`+LaunchProfileDescriptorSchema+`"`, `"schema":"`+LaunchProfileDescriptorSchema+`","schema":"`+LaunchProfileDescriptorSchema+`"`, 1)
	if _, err := DecodeLaunchProfileDescriptor([]byte(duplicate)); err == nil {
		t.Fatal("duplicate profile field accepted")
	}
	if _, err := DecodeLaunchProfileDescriptor([]byte(strings.Repeat("x", MaxProfileDescriptorBytes+1))); err == nil {
		t.Fatal("oversized descriptor accepted")
	}
}
