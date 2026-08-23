package launchadmission_test

import (
	"reflect"
	"testing"

	"m31labs.dev/buckley/pkg/launchadmission"
	"m31labs.dev/buckley/pkg/launchimage"
	"m31labs.dev/buckley/pkg/model"
	"m31labs.dev/buckley/pkg/workspaceguard"
)

func TestSealedEnvelope_PublicSurfaceIsOpaque(t *testing.T) {
	typeOfSeal := reflect.TypeOf(launchadmission.SealedEnvelope{})
	for index := 0; index < typeOfSeal.NumField(); index++ {
		if typeOfSeal.Field(index).IsExported() {
			t.Fatalf("sealed envelope exposes mutable field %s", typeOfSeal.Field(index).Name)
		}
	}
	typeOfRecord := reflect.TypeOf(launchadmission.Record{})
	for index := 0; index < typeOfRecord.NumField(); index++ {
		if typeOfRecord.Field(index).IsExported() {
			t.Fatalf("record exposes mutable field %s", typeOfRecord.Field(index).Name)
		}
	}
}

func TestService_PublicConstructorRequiresConcreteAuthorities(t *testing.T) {
	var constructor func(*workspaceguard.GitInspector, *model.Manager, *launchimage.Verifier, launchadmission.Store) (*launchadmission.Service, error) = launchadmission.NewService
	_ = constructor
	if _, err := launchadmission.NewService(nil, nil, nil, nil); err == nil {
		t.Fatal("nil concrete authorities constructed a service")
	}
	for _, proof := range []any{workspaceguard.LaunchProof{}, model.LaunchPriceProof{}, launchimage.Proof{}} {
		typeOfProof := reflect.TypeOf(proof)
		for index := 0; index < typeOfProof.NumField(); index++ {
			if typeOfProof.Field(index).IsExported() {
				t.Fatalf("%s exposes authority field %s", typeOfProof, typeOfProof.Field(index).Name)
			}
		}
	}
	if !reflect.ValueOf((workspaceguard.LaunchProof{}).Snapshot()).IsZero() || !reflect.ValueOf((model.LaunchPriceProof{}).Snapshot()).IsZero() || !reflect.ValueOf((launchimage.Proof{}).Snapshot()).IsZero() {
		t.Fatal("zero proof exposed nonzero projection state")
	}
}
