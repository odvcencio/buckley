package launchadmission

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"path/filepath"
	"reflect"
	"regexp"
	"strconv"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"m31labs.dev/buckley/pkg/launchcontract"
	"m31labs.dev/buckley/pkg/launchimage"
	"m31labs.dev/buckley/pkg/model"
	"m31labs.dev/buckley/pkg/workspaceguard"
)

const (
	EnvelopeSchema       = "buckley.launch.envelope.v1"
	WorkerEvidenceSchema = launchimage.WorkerEvidenceSchema
	WorkerContract       = launchimage.WorkerContract
	WorkerOS             = launchimage.WorkerOS
	WorkerArchitecture   = launchimage.WorkerArchitecture
	WorkerGoVersion      = launchimage.WorkerGoVersion
	WorkerTinyGoVersion  = launchimage.WorkerTinyGoVersion
	MaxEnvelopeBytes     = 64 << 10
	MaxWorkspacePath     = 4096
	MaxIdentifierBytes   = 256
)

var (
	ErrInvalid  = errors.New("launch admission is invalid")
	ErrConflict = errors.New("launch admission conflicts with durable identity")

	digestPattern    = regexp.MustCompile(`^[0-9a-f]{64}$`)
	headPattern      = regexp.MustCompile(`^[0-9a-f]{40}$`)
	imageIDPattern   = regexp.MustCompile(`^sha256:[0-9a-f]{64}$`)
	imageHostPattern = regexp.MustCompile(`^[a-z0-9]+(?:[.-][a-z0-9]+)*(?::[1-9][0-9]{0,4})?$`)
	imagePathPattern = regexp.MustCompile(`^[a-z0-9]+(?:[._-][a-z0-9]+)*$`)
	licensePaths     = map[string]struct{}{"LICENSE": {}, "LICENSE.txt": {}, "LICENSE.md": {}, "COPYING": {}, "COPYING.txt": {}, "COPYING.md": {}}
	licenseIDs       = map[string]struct{}{"MIT": {}, "Apache-2.0": {}}
)

type Request struct {
	SessionID     string
	RunID         string
	ProfileID     string
	WorkspaceRoot string
}

type WorkspaceRequest struct {
	Root    string
	Profile launchcontract.ProfileDescriptor
}

type PriceRequest struct {
	Profile launchcontract.ProfileDescriptor
}

type ImageRequest struct {
	WorkspaceRoot string
	Profile       launchcontract.ProfileDescriptor
}

type WorkspaceObservation struct {
	Schema                string `json:"schema"`
	CanonicalRoot         string `json:"canonical_root"`
	RootSHA256            string `json:"root_sha256"`
	HEAD                  string `json:"head"`
	ManifestSHA256        string `json:"manifest_sha256"`
	PreflightSHA256       string `json:"preflight_sha256"`
	LicenseID             string `json:"license_id"`
	LicensePath           string `json:"license_path"`
	LicenseSHA256         string `json:"license_sha256"`
	LicenseManifestSHA256 string `json:"license_manifest_sha256"`
}

type ImageObservation struct {
	Schema              string `json:"schema"`
	Contract            string `json:"contract"`
	Reference           string `json:"reference"`
	ImageID             string `json:"image_id"`
	ManifestDigest      string `json:"manifest_digest"`
	ConfigDigest        string `json:"config_digest"`
	SBOMSHA256          string `json:"sbom_sha256"`
	ProvenanceSHA256    string `json:"provenance_sha256"`
	OS                  string `json:"os"`
	Architecture        string `json:"architecture"`
	ContextSHA256       string `json:"context_sha256"`
	ModuleLockSHA256    string `json:"module_lock_sha256"`
	ToolchainLockSHA256 string `json:"toolchain_lock_sha256"`
	GoVersion           string `json:"go_version"`
	TinyGoVersion       string `json:"tinygo_version"`
}

type RouteObservation struct {
	ProviderID                   string `json:"provider_id"`
	ModelID                      string `json:"model_id"`
	CatalogSourceID              string `json:"catalog_source_id"`
	CatalogObservationDigest     string `json:"catalog_observation_digest"`
	ProviderPostAttempts         int    `json:"provider_post_attempts"`
	ManagerAffordabilityAttempts int    `json:"manager_affordability_attempts"`
	DurableRetryOwner            string `json:"durable_retry_owner"`
}

type workspaceObserver interface {
	ObserveWorkspace(context.Context, WorkspaceRequest) (WorkspaceObservation, error)
}

type priceObserver interface {
	ObservePrice(context.Context, PriceRequest) (launchcontract.FreePriceEvidence, error)
}

type imageObserver interface {
	ObserveImage(context.Context, ImageRequest) (ImageObservation, error)
}

type Store interface {
	EnsureLaunchAdmission(context.Context, SealedEnvelope) (Record, bool, error)
}

type Service struct {
	workspace workspaceObserver
	price     priceObserver
	image     imageObserver
	store     Store
}

// NewService accepts only the concrete, package-opaque verification
// authorities. Arbitrary observers cannot enter the production admission
// path.
func NewService(workspace *workspaceguard.GitInspector, price *model.Manager, image *launchimage.Verifier, store Store) (*Service, error) {
	if workspace == nil || price == nil || image == nil || interfaceNil(store) {
		return nil, errors.New("launchadmission: admission capability is unavailable")
	}
	return newService(
		workspaceAuthority{verifier: workspace},
		priceAuthority{verifier: price},
		imageAuthority{verifier: image},
		store,
	)
}

func newService(workspace workspaceObserver, price priceObserver, image imageObserver, store Store) (*Service, error) {
	if interfaceNil(workspace) || interfaceNil(price) || interfaceNil(image) || interfaceNil(store) {
		return nil, errors.New("launchadmission: admission capability is unavailable")
	}
	return &Service{workspace: workspace, price: price, image: image, store: store}, nil
}

type workspaceAuthority struct{ verifier *workspaceguard.GitInspector }

func (a workspaceAuthority) ObserveWorkspace(ctx context.Context, request WorkspaceRequest) (WorkspaceObservation, error) {
	proof, err := a.verifier.VerifyLaunch(ctx, request.Root, request.Profile)
	if err != nil {
		return WorkspaceObservation{}, err
	}
	value := proof.Snapshot()
	return WorkspaceObservation{
		Schema: value.Schema, CanonicalRoot: value.CanonicalRoot,
		RootSHA256: value.RootSHA256, HEAD: value.HEAD,
		ManifestSHA256: value.ManifestSHA256, PreflightSHA256: value.PreflightSHA256,
		LicenseID: value.LicenseID, LicensePath: value.LicensePath,
		LicenseSHA256: value.LicenseSHA256, LicenseManifestSHA256: value.LicenseManifestSHA256,
	}, nil
}

type priceAuthority struct{ verifier *model.Manager }

func (a priceAuthority) ObservePrice(ctx context.Context, request PriceRequest) (launchcontract.FreePriceEvidence, error) {
	proof, err := a.verifier.VerifyLaunchPrice(ctx, request.Profile)
	if err != nil {
		return launchcontract.FreePriceEvidence{}, err
	}
	return proof.Snapshot(), nil
}

type imageAuthority struct{ verifier *launchimage.Verifier }

func (a imageAuthority) ObserveImage(ctx context.Context, request ImageRequest) (ImageObservation, error) {
	proof, err := a.verifier.Verify(ctx, request.WorkspaceRoot, request.Profile)
	if err != nil {
		return ImageObservation{}, err
	}
	value := proof.Snapshot()
	return ImageObservation{
		Schema: value.Schema, Contract: value.Contract,
		Reference: value.Reference, ImageID: value.ImageID,
		ManifestDigest: value.ManifestDigest, ConfigDigest: value.ConfigDigest,
		SBOMSHA256: value.SBOMSHA256, ProvenanceSHA256: value.ProvenanceSHA256,
		OS: value.OS, Architecture: value.Architecture,
		ContextSHA256: value.ContextSHA256, ModuleLockSHA256: value.ModuleLockSHA256,
		ToolchainLockSHA256: value.ToolchainLockSHA256,
		GoVersion:           value.GoVersion, TinyGoVersion: value.TinyGoVersion,
	}, nil
}

func (s *Service) Admit(ctx context.Context, request Request) (Record, bool, error) {
	if s == nil || interfaceNil(s.workspace) || interfaceNil(s.price) || interfaceNil(s.image) || interfaceNil(s.store) {
		return Record{}, false, errors.New("launchadmission: admission capability is unavailable")
	}
	if ctx == nil {
		ctx = context.Background()
	}
	if err := ctx.Err(); err != nil {
		return Record{}, false, err
	}
	if !validIdentifier(request.SessionID) || !validIdentifier(request.RunID) || request.ProfileID != strings.TrimSpace(request.ProfileID) || request.WorkspaceRoot == "" {
		return Record{}, false, fmt.Errorf("%w: request identity", ErrInvalid)
	}
	profile, err := launchcontract.ResolveProfile(request.ProfileID)
	if err != nil {
		return Record{}, false, fmt.Errorf("%w: profile", ErrInvalid)
	}
	workspace, err := s.workspace.ObserveWorkspace(ctx, WorkspaceRequest{Root: request.WorkspaceRoot, Profile: cloneProfile(profile)})
	if err != nil {
		return Record{}, false, fmt.Errorf("launchadmission: observe workspace: %w", err)
	}
	if err := validateWorkspace(workspace); err != nil || workspace.CanonicalRoot != request.WorkspaceRoot {
		return Record{}, false, fmt.Errorf("%w: workspace observation", ErrInvalid)
	}
	image, err := s.image.ObserveImage(ctx, ImageRequest{WorkspaceRoot: workspace.CanonicalRoot, Profile: cloneProfile(profile)})
	if err != nil {
		return Record{}, false, fmt.Errorf("launchadmission: observe image: %w", err)
	}
	if err := validateImage(image); err != nil {
		return Record{}, false, fmt.Errorf("%w: image observation", ErrInvalid)
	}
	price, err := s.price.ObservePrice(ctx, PriceRequest{Profile: cloneProfile(profile)})
	if err != nil {
		return Record{}, false, fmt.Errorf("launchadmission: observe price: %w", err)
	}
	if err := price.ValidateAt(price.ObservedAt); err != nil {
		return Record{}, false, fmt.Errorf("%w: price observation", ErrInvalid)
	}
	profileDigest, err := profile.Digest()
	if err != nil {
		return Record{}, false, fmt.Errorf("%w: profile digest", ErrInvalid)
	}
	envelope := Envelope{
		Schema: EnvelopeSchema, RunID: request.RunID, SessionID: request.SessionID,
		Profile: cloneProfile(profile), ProfileDigest: profileDigest,
		Workspace: cloneWorkspace(workspace), PriceEvidence: clonePrice(price),
		Route: RouteObservation{
			ProviderID: profile.Provider, ModelID: profile.Model,
			CatalogSourceID:              profile.PriceGuard.CatalogSourceID,
			CatalogObservationDigest:     price.Receipt.Digest,
			ProviderPostAttempts:         profile.ProviderPostAttempts,
			ManagerAffordabilityAttempts: profile.ManagerAffordabilityAttempts,
			DurableRetryOwner:            profile.RetryOwner,
		},
		Image: cloneImage(image),
	}
	sealed, err := seal(envelope)
	if err != nil {
		return Record{}, false, err
	}
	recorded, inserted, err := s.store.EnsureLaunchAdmission(ctx, sealed)
	if err != nil {
		return Record{}, false, err
	}
	if !sealed.matches(recorded) {
		return Record{}, false, fmt.Errorf("%w: store returned a mismatched record", ErrInvalid)
	}
	return recorded.clone(), inserted, nil
}

// SealedEnvelope is the only value accepted by a launch admission Store. Its
// private state is created by Service.Admit after all trusted observations.
type SealedEnvelope struct{ envelope Envelope }

func (s SealedEnvelope) Snapshot() Envelope { return cloneEnvelope(s.envelope) }

func (s SealedEnvelope) CanonicalBytes() ([]byte, error) {
	if err := validateEnvelope(s.envelope); err != nil {
		return nil, err
	}
	return marshalBounded(s.envelope)
}

func (s SealedEnvelope) Record(createdAt time.Time) (Record, error) {
	if !launchcontract.CanonicalTime(createdAt) {
		return Record{}, fmt.Errorf("%w: created time", ErrInvalid)
	}
	if err := s.envelope.PriceEvidence.ValidateAt(createdAt); err != nil {
		return Record{}, fmt.Errorf("%w: price evidence is not fresh", ErrInvalid)
	}
	if _, err := s.CanonicalBytes(); err != nil {
		return Record{}, err
	}
	return Record{envelope: cloneEnvelope(s.envelope), createdAt: createdAt}, nil
}

func (s SealedEnvelope) matches(record Record) bool {
	if record.createdAt.IsZero() {
		return false
	}
	left, leftErr := s.CanonicalBytes()
	right, rightErr := marshalBounded(record.envelope)
	if leftErr != nil || rightErr != nil || validateEnvelope(record.envelope) != nil || !bytes.Equal(left, right) {
		return false
	}
	return record.ValidateAt(record.createdAt) == nil
}

// Record is the immutable read projection returned after the Store assigns
// database time. Accessors always detach slice-backed contract values.
type Record struct {
	envelope  Envelope
	createdAt time.Time
}

func (r Record) Snapshot() Envelope   { return cloneEnvelope(r.envelope) }
func (r Record) CreatedAt() time.Time { return r.createdAt }
func (r Record) Digest() string       { return r.envelope.EnvelopeDigest }
func (r Record) SessionID() string    { return r.envelope.SessionID }
func (r Record) RunID() string        { return r.envelope.RunID }

func (r Record) ValidateAt(now time.Time) error {
	if !launchcontract.CanonicalTime(r.createdAt) || !launchcontract.CanonicalTime(now) || now.Before(r.createdAt) {
		return fmt.Errorf("%w: record time", ErrInvalid)
	}
	if err := validateEnvelope(r.envelope); err != nil {
		return err
	}
	if err := r.envelope.PriceEvidence.ValidateAt(now); err != nil {
		return fmt.Errorf("%w: price evidence is not fresh", ErrInvalid)
	}
	return nil
}

func (r Record) CanonicalBytes() ([]byte, error) {
	if err := r.ValidateAt(r.createdAt); err != nil {
		return nil, err
	}
	stored := storedEnvelope{Envelope: cloneEnvelope(r.envelope), CreatedAt: r.createdAt}
	return marshalBounded(stored)
}

func (r Record) clone() Record {
	return Record{envelope: cloneEnvelope(r.envelope), createdAt: r.createdAt}
}

type Envelope struct {
	Schema         string                           `json:"schema"`
	RunID          string                           `json:"run_id"`
	SessionID      string                           `json:"session_id"`
	Profile        launchcontract.ProfileDescriptor `json:"profile"`
	ProfileDigest  string                           `json:"profile_digest"`
	Workspace      WorkspaceObservation             `json:"workspace"`
	PriceEvidence  launchcontract.FreePriceEvidence `json:"price_evidence"`
	Route          RouteObservation                 `json:"route"`
	Image          ImageObservation                 `json:"image"`
	EnvelopeDigest string                           `json:"envelope_digest"`
}

type storedEnvelope struct {
	Envelope
	CreatedAt time.Time `json:"created_at"`
}

func DecodeRecord(data []byte) (Record, error) {
	if len(data) == 0 || len(data) > MaxEnvelopeBytes {
		return Record{}, fmt.Errorf("%w: payload bound", ErrInvalid)
	}
	if err := launchcontract.RejectDuplicateJSONKeys(data); err != nil {
		return Record{}, fmt.Errorf("%w: duplicate payload fields", ErrInvalid)
	}
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	var stored storedEnvelope
	if err := decoder.Decode(&stored); err != nil {
		return Record{}, fmt.Errorf("%w: malformed payload", ErrInvalid)
	}
	var extra any
	if err := decoder.Decode(&extra); err != io.EOF {
		return Record{}, fmt.Errorf("%w: trailing payload", ErrInvalid)
	}
	record := Record{envelope: cloneEnvelope(stored.Envelope), createdAt: stored.CreatedAt}
	if err := record.ValidateAt(record.createdAt); err != nil {
		return Record{}, err
	}
	return record, nil
}

func seal(envelope Envelope) (SealedEnvelope, error) {
	envelope = cloneEnvelope(envelope)
	if envelope.EnvelopeDigest != "" {
		return SealedEnvelope{}, fmt.Errorf("%w: caller supplied envelope digest", ErrInvalid)
	}
	if err := validateEnvelopeShape(envelope); err != nil {
		return SealedEnvelope{}, err
	}
	digest, err := expectedEnvelopeDigest(envelope)
	if err != nil {
		return SealedEnvelope{}, err
	}
	envelope.EnvelopeDigest = digest
	return SealedEnvelope{envelope: envelope}, nil
}

func validateEnvelope(envelope Envelope) error {
	if err := validateEnvelopeShape(envelope); err != nil {
		return err
	}
	if !digestPattern.MatchString(envelope.EnvelopeDigest) {
		return fmt.Errorf("%w: envelope digest", ErrInvalid)
	}
	expected, err := expectedEnvelopeDigest(envelope)
	if err != nil || expected != envelope.EnvelopeDigest {
		return fmt.Errorf("%w: envelope digest", ErrInvalid)
	}
	return nil
}

func validateEnvelopeShape(envelope Envelope) error {
	if envelope.Schema != EnvelopeSchema || !validIdentifier(envelope.SessionID) || !validIdentifier(envelope.RunID) {
		return fmt.Errorf("%w: envelope identity", ErrInvalid)
	}
	profileDigest, err := envelope.Profile.Digest()
	if err != nil || envelope.ProfileDigest != profileDigest || !digestPattern.MatchString(envelope.ProfileDigest) {
		return fmt.Errorf("%w: profile binding", ErrInvalid)
	}
	if err := validateWorkspace(envelope.Workspace); err != nil {
		return err
	}
	if err := envelope.PriceEvidence.ValidateAt(envelope.PriceEvidence.ObservedAt); err != nil {
		return fmt.Errorf("%w: price evidence", ErrInvalid)
	}
	if err := validateRoute(envelope.Route, envelope.Profile, envelope.PriceEvidence); err != nil {
		return err
	}
	if err := validateImage(envelope.Image); err != nil {
		return err
	}
	return nil
}

func validateWorkspace(value WorkspaceObservation) error {
	if value.Schema != launchcontract.WorkspaceEvidenceSchema || !safeText(value.CanonicalRoot, MaxWorkspacePath) || !filepath.IsAbs(value.CanonicalRoot) || filepath.Clean(value.CanonicalRoot) != value.CanonicalRoot {
		return fmt.Errorf("%w: workspace identity", ErrInvalid)
	}
	for _, digest := range []string{value.RootSHA256, value.ManifestSHA256, value.PreflightSHA256, value.LicenseSHA256, value.LicenseManifestSHA256} {
		if !digestPattern.MatchString(digest) {
			return fmt.Errorf("%w: workspace digest", ErrInvalid)
		}
	}
	if !headPattern.MatchString(value.HEAD) {
		return fmt.Errorf("%w: workspace HEAD", ErrInvalid)
	}
	if _, ok := licenseIDs[value.LicenseID]; !ok {
		return fmt.Errorf("%w: license identity", ErrInvalid)
	}
	if _, ok := licensePaths[value.LicensePath]; !ok {
		return fmt.Errorf("%w: license path", ErrInvalid)
	}
	return nil
}

func validateImage(value ImageObservation) error {
	if value.Schema != WorkerEvidenceSchema || value.Contract != WorkerContract || !validImageReference(value.Reference) || !imageIDPattern.MatchString(value.ImageID) || value.OS != WorkerOS || value.Architecture != WorkerArchitecture || value.GoVersion != WorkerGoVersion || value.TinyGoVersion != WorkerTinyGoVersion {
		return fmt.Errorf("%w: image identity", ErrInvalid)
	}
	_, manifest, _ := strings.Cut(value.Reference, "@")
	if value.ManifestDigest != manifest || value.ConfigDigest != value.ImageID || !imageIDPattern.MatchString(value.ManifestDigest) || !imageIDPattern.MatchString(value.ConfigDigest) {
		return fmt.Errorf("%w: image linkage", ErrInvalid)
	}
	for _, digest := range []string{value.SBOMSHA256, value.ProvenanceSHA256, value.ContextSHA256, value.ModuleLockSHA256, value.ToolchainLockSHA256} {
		if !digestPattern.MatchString(digest) {
			return fmt.Errorf("%w: image digest", ErrInvalid)
		}
	}
	return nil
}

func validateRoute(route RouteObservation, profile launchcontract.ProfileDescriptor, price launchcontract.FreePriceEvidence) error {
	if route.ProviderID != profile.Provider || route.ProviderID != launchcontract.ProviderOpenRouter || route.ModelID != profile.Model || route.ModelID != launchcontract.ModelOxAlpha || route.CatalogSourceID != profile.PriceGuard.CatalogSourceID || route.CatalogSourceID != price.SourceID || route.CatalogObservationDigest != price.Receipt.Digest || !digestPattern.MatchString(route.CatalogObservationDigest) || route.ProviderPostAttempts != launchcontract.ProviderPostAttempts || route.ProviderPostAttempts != profile.ProviderPostAttempts || route.ManagerAffordabilityAttempts != launchcontract.ManagerAffordabilityAttempts || route.ManagerAffordabilityAttempts != profile.ManagerAffordabilityAttempts || route.DurableRetryOwner != launchcontract.RetryOwnerDapr || route.DurableRetryOwner != profile.RetryOwner {
		return fmt.Errorf("%w: route binding", ErrInvalid)
	}
	return nil
}

func expectedEnvelopeDigest(envelope Envelope) (string, error) {
	copy := cloneEnvelope(envelope)
	copy.EnvelopeDigest = ""
	data, err := marshalBounded(copy)
	if err != nil {
		return "", err
	}
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:]), nil
}

func validIdentifier(value string) bool { return safeText(value, MaxIdentifierBytes) }

func safeText(value string, limit int) bool {
	return value != "" && len(value) <= limit && strings.TrimSpace(value) == value && utf8.ValidString(value) && strings.IndexFunc(value, unicode.IsControl) < 0
}

func validImageReference(value string) bool {
	if len(value) > 340 || strings.Count(value, "@") != 1 {
		return false
	}
	repository, digest, ok := strings.Cut(value, "@")
	if !ok || !imageIDPattern.MatchString(digest) || repository == "" || strings.HasPrefix(repository, "/") || strings.HasSuffix(repository, "/") {
		return false
	}
	parts := strings.Split(repository, "/")
	if len(parts) < 2 || !imageHostPattern.MatchString(parts[0]) {
		return false
	}
	if colon := strings.LastIndexByte(parts[0], ':'); colon >= 0 {
		port, err := strconv.ParseUint(parts[0][colon+1:], 10, 16)
		if err != nil || port == 0 {
			return false
		}
	}
	for _, part := range parts[1:] {
		if !imagePathPattern.MatchString(part) {
			return false
		}
	}
	return true
}

func cloneProfile(profile launchcontract.ProfileDescriptor) launchcontract.ProfileDescriptor {
	profile.License.AllowedIDs = append([]string(nil), profile.License.AllowedIDs...)
	return profile
}

func clonePrice(price launchcontract.FreePriceEvidence) launchcontract.FreePriceEvidence {
	price.Prices = append([]launchcontract.RawPriceDimension(nil), price.Prices...)
	return price
}

func cloneWorkspace(value WorkspaceObservation) WorkspaceObservation { return value }
func cloneImage(value ImageObservation) ImageObservation             { return value }

func cloneEnvelope(envelope Envelope) Envelope {
	envelope.Profile = cloneProfile(envelope.Profile)
	envelope.PriceEvidence = clonePrice(envelope.PriceEvidence)
	return envelope
}

func marshalBounded(value any) ([]byte, error) {
	data, err := json.Marshal(value)
	if err != nil || len(data) == 0 || len(data) > MaxEnvelopeBytes {
		return nil, fmt.Errorf("%w: payload bound", ErrInvalid)
	}
	return data, nil
}

func interfaceNil(value any) bool {
	if value == nil {
		return true
	}
	reflected := reflect.ValueOf(value)
	switch reflected.Kind() {
	case reflect.Chan, reflect.Func, reflect.Interface, reflect.Map, reflect.Pointer, reflect.Slice:
		return reflected.IsNil()
	default:
		return false
	}
}
