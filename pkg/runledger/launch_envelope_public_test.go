package runledger_test

import (
	"context"
	"go/ast"
	"go/parser"
	"go/token"
	"io/fs"
	"path/filepath"
	"reflect"
	"runtime"
	"strings"
	"testing"

	"m31labs.dev/buckley/pkg/launchadmission"
	"m31labs.dev/buckley/pkg/launchcontract"
	"m31labs.dev/buckley/pkg/model"
	"m31labs.dev/buckley/pkg/runledger"
)

func TestLaunchAdmissionPublicSurface_HasNoRawWriterSealerOrEvidenceGetter(t *testing.T) {
	var store launchadmission.Store = (*runledger.SQLiteStore)(nil)
	_ = store
	var observe func(*model.Manager, context.Context, launchcontract.ProfileDescriptor) (model.LaunchPriceProof, error) = (*model.Manager).VerifyLaunchPrice
	_ = observe

	for _, subject := range []struct {
		name      string
		typeOf    reflect.Type
		forbidden []string
	}{
		{name: "runledger store", typeOf: reflect.TypeOf((*runledger.SQLiteStore)(nil)), forbidden: []string{"EnsureLaunchEnvelope", "SealLaunchEnvelope", "StoreLaunchEnvelope"}},
		{name: "model manager", typeOf: reflect.TypeOf((*model.Manager)(nil)), forbidden: []string{"AdmitOpenRouterFreeLaunch", "ObservePrice", "PriceEvidence", "GetPriceEvidence"}},
	} {
		for _, name := range subject.forbidden {
			if _, ok := subject.typeOf.MethodByName(name); ok {
				t.Fatalf("%s exposes forbidden raw authority method %s", subject.name, name)
			}
		}
	}

	_, current, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("test source location unavailable")
	}
	repositoryRoot := filepath.Clean(filepath.Join(filepath.Dir(current), "..", ".."))
	for _, scan := range []struct {
		directory string
		forbidden map[string]struct{}
	}{
		{directory: filepath.Join(repositoryRoot, "pkg", "launchadmission"), forbidden: stringSet("SealEnvelope", "NewSealedEnvelope", "NewRecord")},
		{directory: filepath.Join(repositoryRoot, "pkg", "launchcontract"), forbidden: stringSet("NewCatalogReceipt", "NewFreePriceEvidence", "NewFreePriceEvidenceWithReceipt")},
	} {
		packages, err := parser.ParseDir(token.NewFileSet(), scan.directory, func(info fs.FileInfo) bool {
			name := info.Name()
			return filepath.Ext(name) == ".go" && !strings.HasSuffix(name, "_test.go")
		}, 0)
		if err != nil {
			t.Fatal(err)
		}
		for _, parsedPackage := range packages {
			for _, file := range parsedPackage.Files {
				for _, declaration := range file.Decls {
					function, ok := declaration.(*ast.FuncDecl)
					if !ok || !function.Name.IsExported() {
						continue
					}
					if _, forbidden := scan.forbidden[function.Name.Name]; forbidden {
						t.Fatalf("raw launch authority remains exported: %s", function.Name.Name)
					}
				}
			}
		}
	}
}

func stringSet(values ...string) map[string]struct{} {
	set := make(map[string]struct{}, len(values))
	for _, value := range values {
		set[value] = struct{}{}
	}
	return set
}
