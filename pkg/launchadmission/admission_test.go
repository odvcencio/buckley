package launchadmission

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"reflect"
	"strings"
	"testing"
	"time"

	"m31labs.dev/buckley/pkg/launchcontract"
)

type workspaceObserverFunc func(context.Context, WorkspaceRequest) (WorkspaceObservation, error)

func (f workspaceObserverFunc) ObserveWorkspace(ctx context.Context, req WorkspaceRequest) (WorkspaceObservation, error) {
	return f(ctx, req)
}

type priceObserverFunc func(context.Context, PriceRequest) (launchcontract.FreePriceEvidence, error)

func (f priceObserverFunc) ObservePrice(ctx context.Context, req PriceRequest) (launchcontract.FreePriceEvidence, error) {
	return f(ctx, req)
}

type imageObserverFunc func(context.Context, ImageRequest) (ImageObservation, error)

func (f imageObserverFunc) ObserveImage(ctx context.Context, req ImageRequest) (ImageObservation, error) {
	return f(ctx, req)
}

type admissionStoreFunc func(context.Context, SealedEnvelope) (Record, bool, error)

func (f admissionStoreFunc) EnsureLaunchAdmission(ctx context.Context, sealed SealedEnvelope) (Record, bool, error) {
	return f(ctx, sealed)
}

func TestServiceAdmit_ObservesSealsAndDefendsMutableValues(t *testing.T) {
	now := time.Date(2026, 8, 21, 22, 0, 0, 123, time.UTC)
	workspaceCalls, imageCalls, priceCalls, storeCalls := 0, 0, 0, 0
	workspace := workspaceObserverFunc(func(_ context.Context, req WorkspaceRequest) (WorkspaceObservation, error) {
		workspaceCalls++
		if req.Root != "/work/gosx" || req.Profile.ID != "gosx" {
			t.Fatalf("workspace request = %+v", req)
		}
		req.Profile.License.AllowedIDs[0] = "mutated"
		return testWorkspace("/work/gosx"), nil
	})
	image := imageObserverFunc(func(_ context.Context, req ImageRequest) (ImageObservation, error) {
		imageCalls++
		if req.WorkspaceRoot != "/work/gosx" || !reflect.DeepEqual(req.Profile.License.AllowedIDs, []string{"Apache-2.0", "MIT"}) {
			t.Fatalf("image request = %+v", req)
		}
		return testImage(), nil
	})
	priceValue := testPrice(t, now)
	price := priceObserverFunc(func(_ context.Context, req PriceRequest) (launchcontract.FreePriceEvidence, error) {
		priceCalls++
		if !reflect.DeepEqual(req.Profile.License.AllowedIDs, []string{"Apache-2.0", "MIT"}) {
			t.Fatalf("price profile mutated: %+v", req.Profile)
		}
		return priceValue, nil
	})
	var storedBytes []byte
	store := admissionStoreFunc(func(_ context.Context, sealed SealedEnvelope) (Record, bool, error) {
		storeCalls++
		snapshot := sealed.Snapshot()
		snapshot.Profile.License.AllowedIDs[0] = "store-mutation"
		storedBytes, _ = sealed.CanonicalBytes()
		record, err := sealed.Record(now)
		return record, true, err
	})
	service, err := newService(workspace, price, image, store)
	if err != nil {
		t.Fatal(err)
	}
	record, inserted, err := service.Admit(context.Background(), Request{SessionID: "session-1", RunID: "run-1", ProfileID: "gosx", WorkspaceRoot: "/work/gosx"})
	if err != nil || !inserted {
		t.Fatalf("Admit = %+v, %v, %v", record, inserted, err)
	}
	if workspaceCalls != 1 || imageCalls != 1 || priceCalls != 1 || storeCalls != 1 {
		t.Fatalf("calls workspace=%d image=%d price=%d store=%d", workspaceCalls, imageCalls, priceCalls, storeCalls)
	}
	if len(storedBytes) == 0 || record.CreatedAt() != now || record.SessionID() != "session-1" || record.RunID() != "run-1" || !digestPattern.MatchString(record.Digest()) {
		t.Fatalf("record = %+v bytes=%d", record.Snapshot(), len(storedBytes))
	}
	first := record.Snapshot()
	first.Profile.License.AllowedIDs[0] = "mutated"
	if record.Snapshot().Profile.License.AllowedIDs[0] != "Apache-2.0" {
		t.Fatal("record accessor leaked mutable profile storage")
	}
	encoded, err := record.CanonicalBytes()
	if err != nil {
		t.Fatal(err)
	}
	decoded, err := DecodeRecord(encoded)
	if err != nil || !reflect.DeepEqual(decoded.Snapshot(), record.Snapshot()) || decoded.CreatedAt() != now {
		t.Fatalf("decoded record = %+v, %v", decoded.Snapshot(), err)
	}
}

func TestServiceAdmit_FailsBeforeStoreOnObservationErrorsAndMalformedOutput(t *testing.T) {
	now := time.Date(2026, 8, 21, 22, 0, 0, 0, time.UTC)
	baseWorkspace := workspaceObserverFunc(func(context.Context, WorkspaceRequest) (WorkspaceObservation, error) {
		return testWorkspace("/work/gsxmail"), nil
	})
	baseImage := imageObserverFunc(func(context.Context, ImageRequest) (ImageObservation, error) { return testImage(), nil })
	basePrice := priceObserverFunc(func(context.Context, PriceRequest) (launchcontract.FreePriceEvidence, error) {
		return testPrice(t, now), nil
	})
	for _, test := range []struct {
		name      string
		workspace workspaceObserver
		image     imageObserver
		price     priceObserver
	}{
		{name: "workspace error", workspace: workspaceObserverFunc(func(context.Context, WorkspaceRequest) (WorkspaceObservation, error) {
			return WorkspaceObservation{}, errors.New("blocked")
		}), image: baseImage, price: basePrice},
		{name: "workspace forged root", workspace: workspaceObserverFunc(func(context.Context, WorkspaceRequest) (WorkspaceObservation, error) {
			return testWorkspace("/work/other"), nil
		}), image: baseImage, price: basePrice},
		{name: "image malformed", workspace: baseWorkspace, image: imageObserverFunc(func(context.Context, ImageRequest) (ImageObservation, error) {
			value := testImage()
			value.SBOMSHA256 = "bad"
			return value, nil
		}), price: basePrice},
		{name: "price malformed", workspace: baseWorkspace, image: baseImage, price: priceObserverFunc(func(context.Context, PriceRequest) (launchcontract.FreePriceEvidence, error) {
			value := testPrice(t, now)
			value.Prices[0].Value = "1"
			return value, nil
		})},
	} {
		t.Run(test.name, func(t *testing.T) {
			storeCalls := 0
			service, err := newService(test.workspace, test.price, test.image, admissionStoreFunc(func(context.Context, SealedEnvelope) (Record, bool, error) {
				storeCalls++
				return Record{}, false, nil
			}))
			if err != nil {
				t.Fatal(err)
			}
			if _, _, err := service.Admit(context.Background(), Request{SessionID: "session-1", RunID: "run-1", ProfileID: "gsxmail", WorkspaceRoot: "/work/gsxmail"}); err == nil || storeCalls != 0 {
				t.Fatalf("error=%v store calls=%d", err, storeCalls)
			}
		})
	}
}

func TestServiceAdmit_RejectsUnavailableAndMismatchedStore(t *testing.T) {
	var nilWorkspace *nilWorkspaceObserver
	if _, err := newService(nilWorkspace, nil, nil, nil); err == nil {
		t.Fatal("typed nil observer accepted")
	}
	if _, err := (SealedEnvelope{}).CanonicalBytes(); !errors.Is(err, ErrInvalid) {
		t.Fatalf("zero sealed envelope error = %v", err)
	}

	now := time.Date(2026, 8, 21, 22, 0, 0, 0, time.UTC)
	service, err := newService(
		workspaceObserverFunc(func(context.Context, WorkspaceRequest) (WorkspaceObservation, error) {
			return testWorkspace("/work/tqwebp"), nil
		}),
		priceObserverFunc(func(context.Context, PriceRequest) (launchcontract.FreePriceEvidence, error) {
			return testPrice(t, now), nil
		}),
		imageObserverFunc(func(context.Context, ImageRequest) (ImageObservation, error) { return testImage(), nil }),
		admissionStoreFunc(func(_ context.Context, sealed SealedEnvelope) (Record, bool, error) {
			record, recordErr := sealed.Record(now)
			if recordErr == nil {
				record.envelope.RunID = "other-run"
			}
			return record, true, recordErr
		}),
	)
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := service.Admit(context.Background(), Request{SessionID: "session-1", RunID: "run-1", ProfileID: "tqwebp", WorkspaceRoot: "/work/tqwebp"}); !errors.Is(err, ErrInvalid) {
		t.Fatalf("mismatched store error = %v", err)
	}
}

type nilWorkspaceObserver struct{}

func (*nilWorkspaceObserver) ObserveWorkspace(context.Context, WorkspaceRequest) (WorkspaceObservation, error) {
	return WorkspaceObservation{}, nil
}

func testWorkspace(root string) WorkspaceObservation {
	return WorkspaceObservation{
		Schema: launchcontract.WorkspaceEvidenceSchema, CanonicalRoot: root,
		RootSHA256: strings.Repeat("1", 64), HEAD: strings.Repeat("2", 40),
		ManifestSHA256: strings.Repeat("3", 64), PreflightSHA256: strings.Repeat("4", 64),
		LicenseID: "MIT", LicensePath: "LICENSE", LicenseSHA256: strings.Repeat("5", 64),
		LicenseManifestSHA256: strings.Repeat("6", 64),
	}
}

func testImage() ImageObservation {
	manifest := "sha256:" + strings.Repeat("7", 64)
	imageID := "sha256:" + strings.Repeat("8", 64)
	return ImageObservation{
		Schema: WorkerEvidenceSchema, Contract: WorkerContract,
		Reference: "127.0.0.1:5000/buckley/worker@" + manifest,
		ImageID:   imageID, ManifestDigest: manifest, ConfigDigest: imageID,
		SBOMSHA256: strings.Repeat("9", 64), ProvenanceSHA256: strings.Repeat("a", 64),
		OS: WorkerOS, Architecture: WorkerArchitecture,
		ContextSHA256: strings.Repeat("b", 64), ModuleLockSHA256: strings.Repeat("c", 64),
		ToolchainLockSHA256: strings.Repeat("d", 64), GoVersion: WorkerGoVersion, TinyGoVersion: WorkerTinyGoVersion,
	}
}

func testPrice(t *testing.T, observedAt time.Time) launchcontract.FreePriceEvidence {
	t.Helper()
	prices := []launchcontract.RawPriceDimension{
		{Name: "completion", Value: "0"}, {Name: "image", Value: "0"},
		{Name: "input_cache_read", Value: "0"}, {Name: "input_cache_write", Value: "0"},
		{Name: "internal_reasoning", Value: "0"}, {Name: "prompt", Value: "0"},
		{Name: "request", Value: "0"}, {Name: "web_search", Value: "0"},
	}
	receipt := launchcontract.CatalogReceipt{
		Schema: launchcontract.CatalogReceiptSchema, SourceID: launchcontract.OpenRouterCatalogSourceID,
		SourceURL: launchcontract.OpenRouterCatalogSourceURL, ObservedAt: observedAt,
		ResponseDigest: strings.Repeat("e", 64), ModelObjectDigest: strings.Repeat("f", 64),
	}
	receipt.Digest = testDigest(t, receipt)
	evidence := launchcontract.FreePriceEvidence{
		Schema: launchcontract.FreePriceEvidenceSchema, ProviderID: launchcontract.ProviderOpenRouter,
		SourceID: launchcontract.OpenRouterCatalogSourceID, SourceURL: launchcontract.OpenRouterCatalogSourceURL,
		CanonicalSlug: launchcontract.ModelOxAlpha, ObservedAt: observedAt, ExpiresAt: observedAt.Add(5 * time.Minute),
		Prices: prices, Receipt: receipt,
	}
	evidence.Digest = testDigest(t, evidence)
	if err := evidence.ValidateAt(observedAt); err != nil {
		t.Fatalf("price fixture: %v", err)
	}
	return evidence
}

func testDigest(t *testing.T, value any) string {
	t.Helper()
	data, err := json.Marshal(value)
	if err != nil {
		t.Fatal(err)
	}
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:])
}
