package model_test

import (
	"context"
	"go/ast"
	"go/parser"
	"go/token"
	"path/filepath"
	"runtime"
	"testing"

	"m31labs.dev/buckley/pkg/launchcontract"
	"m31labs.dev/buckley/pkg/model"
)

func TestLaunchAdmissionPublicSurface_OnlyManagerPerformsTrustedPriceObservation(t *testing.T) {
	var operation func(*model.Manager, context.Context, launchcontract.ProfileDescriptor) (model.LaunchPriceProof, error) = (*model.Manager).VerifyLaunchPrice
	_ = operation
	if _, err := (*model.Manager)(nil).VerifyLaunchPrice(context.Background(), launchcontract.ProfileDescriptor{}); err == nil {
		t.Fatal("nil manager minted price evidence")
	}

	_, current, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("test source location unavailable")
	}
	pricePath := filepath.Join(filepath.Dir(current), "..", "launchcontract", "price.go")
	file, err := parser.ParseFile(token.NewFileSet(), pricePath, nil, 0)
	if err != nil {
		t.Fatal(err)
	}
	for _, declaration := range file.Decls {
		function, ok := declaration.(*ast.FuncDecl)
		if !ok || function.Recv != nil || !function.Name.IsExported() {
			continue
		}
		if function.Name.Name == "NewCatalogReceipt" || function.Name.Name == "NewFreePriceEvidence" || function.Name.Name == "NewFreePriceEvidenceWithReceipt" {
			t.Fatalf("raw launch evidence mint remains exported: %s", function.Name.Name)
		}
	}
}
