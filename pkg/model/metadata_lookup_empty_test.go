package model

import (
	"reflect"
	"testing"

	"m31labs.dev/buckley/pkg/config"
)

type emptyAliasMetadataProvider struct {
	*stubProvider
	emptyAll bool
	calls    []string
}

func (p *emptyAliasMetadataProvider) GetModelInfo(id string) (*ModelInfo, error) {
	p.calls = append(p.calls, id)
	if p.emptyAll || id != "future-model" {
		return nil, nil
	}
	return &ModelInfo{ID: id, ContextLength: 64000, MaxCompletionTokens: 1024}, nil
}

func TestManagerMetadataLookup_EmptyProviderResults(t *testing.T) {
	const providerID = "openai_compatible"
	const qualified = providerID + "/future-model"
	for _, governed := range []bool{false, true} {
		for _, emptyAll := range []bool{false, true} {
			name := "ordinary"
			if governed {
				name = "governed"
			}
			if emptyAll {
				name += "/all_empty"
			} else {
				name += "/empty_first_alias"
			}
			t.Run(name, func(t *testing.T) {
				provider := &emptyAliasMetadataProvider{stubProvider: &stubProvider{id: providerID}, emptyAll: emptyAll}
				cfg := config.DefaultConfig()
				cfg.Models.DefaultProvider = providerID
				mgr := &Manager{config: cfg, providers: map[string]Provider{providerID: provider}}
				var info *ModelInfo
				var err error
				if governed {
					info, err = mgr.GetModelInfoForRoute(ModelRoute{ProviderID: providerID, SelectedModel: qualified})
				} else {
					info, err = mgr.GetModelInfo(qualified)
				}
				if emptyAll {
					if err == nil || info != nil {
						t.Fatalf("empty metadata accepted: info=%+v err=%v", info, err)
					}
				} else if err != nil || info == nil || info.ID != "future-model" || info.MaxCompletionTokens != 1024 {
					t.Fatalf("did not continue to valid alias: info=%+v err=%v", info, err)
				}
				if !reflect.DeepEqual(provider.calls, []string{qualified, "future-model"}) {
					t.Fatalf("candidate calls=%v, want both aliases once", provider.calls)
				}
			})
		}
	}
}
