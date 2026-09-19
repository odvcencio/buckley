package launchadmission_test

import (
	"reflect"
	"testing"

	"m31labs.dev/buckley/pkg/launchadmission"
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
