package antigravitycatalog

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v7/internal/config"
	coreauth "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/auth"
)

type staticFetcher struct {
	models []DiscoveredModel
	err    error
}

func (f staticFetcher) Fetch(_ context.Context, _ *coreauth.Auth) ([]DiscoveredModel, error) {
	return f.models, f.err
}

type fetchResponse struct {
	models []DiscoveredModel
	err    error
}

type perAuthFetcher struct {
	responses map[string]fetchResponse
}

func (f perAuthFetcher) Fetch(_ context.Context, pAuth *coreauth.Auth) ([]DiscoveredModel, error) {
	response, exists := f.responses[AuthFingerprint(pAuth)]
	if !exists {
		return nil, errors.New("missing fetch response")
	}
	return response.models, response.err
}

type staticAuthLoader struct {
	auths []*coreauth.Auth
	err   error
}

type staticModelVerifier struct {
	err error
}

func (v staticModelVerifier) Verify(_ context.Context, _ *config.Config, _ *coreauth.Auth, _ string) error {
	return v.err
}

type recordingModelVerifier struct {
	verificationCalls []verificationCall
	errors            map[string]error
}

type verificationCall struct {
	modelID         string
	authFingerprint string
}

type modelNotFoundError struct{}

func (modelNotFoundError) Error() string {
	return "model not found"
}

func (modelNotFoundError) StatusCode() int {
	return 404
}

func (v *recordingModelVerifier) Verify(_ context.Context, _ *config.Config, pAuth *coreauth.Auth, pModelID string) error {
	authFingerprint := AuthFingerprint(pAuth)
	v.verificationCalls = append(v.verificationCalls, verificationCall{modelID: pModelID, authFingerprint: authFingerprint})
	return v.errors[authFingerprint+":"+pModelID]
}

func (l staticAuthLoader) Load(_ context.Context, _ string) ([]*coreauth.Auth, error) {
	return l.auths, l.err
}

func TestParseModelsResponseCreatesSortedSafeModels(t *testing.T) {
	response := []byte(`{"models":{"gemini-beta":{"displayName":"Gemini Beta","maxTokens":120000,"maxOutputTokens":8192},"gemini-alpha":{"maxTokens":64000}},"webSearchModelIds":["gemini-beta"]}`)

	models, errParse := parseModelsResponse(response)
	if errParse != nil {
		t.Fatalf("parseModelsResponse() error = %v", errParse)
	}
	if len(models) != 2 {
		t.Fatalf("model count = %d, want 2", len(models))
	}
	if models[0].ID != "gemini-alpha" || models[0].DisplayName != "gemini-alpha" {
		t.Fatalf("first model = %+v, want sorted fallback display name", models[0])
	}
	if !models[1].SupportsWebSearch || models[1].ContextLength != 120000 {
		t.Fatalf("second model = %+v, want web search metadata", models[1])
	}
}

func TestMergeModelsPreservesVerificationAndMarksMissingModelsStale(t *testing.T) {
	now := time.Date(2026, time.August, 13, 10, 0, 0, 0, time.UTC)
	existing := []CatalogModel{
		{ID: "gemini-verified", State: VerificationStateVerified},
		{ID: "gemini-removed", State: VerificationStatePending},
		{ID: "gemini-returned", State: VerificationStateStale},
	}
	discovered := []DiscoveredModel{
		{ID: "gemini-verified", DisplayName: "Verified"},
		{ID: "gemini-returned", DisplayName: "Returned"},
		{ID: "gemini-new", DisplayName: "New"},
	}

	models := mergeModels(existing, discovered, map[string]struct{}{}, now)
	states := make(map[string]VerificationState, len(models))
	for _, model := range models {
		states[model.ID] = model.State
		if model.ID != "gemini-removed" && !model.LastSeenAt.Equal(now) {
			t.Fatalf("last seen = %s for %s, want %s", model.LastSeenAt, model.ID, now)
		}
	}
	if states["gemini-verified"] != VerificationStateVerified {
		t.Fatalf("verified model state = %s", states["gemini-verified"])
	}
	if states["gemini-returned"] != VerificationStatePending {
		t.Fatalf("returned model state = %s", states["gemini-returned"])
	}
	if states["gemini-new"] != VerificationStatePending {
		t.Fatalf("new model state = %s", states["gemini-new"])
	}
	if states["gemini-removed"] != VerificationStateStale {
		t.Fatalf("removed model state = %s", states["gemini-removed"])
	}
}

func TestRefreshWritesRedactedPendingSnapshot(t *testing.T) {
	temporaryDirectory := t.TempDir()
	snapshotPath := filepath.Join(temporaryDirectory, "catalog.json")
	now := time.Date(2026, time.August, 13, 11, 0, 0, 0, time.UTC)
	service := catalogService{
		settings: catalogSettings{snapshotPath: snapshotPath},
		fetcher: staticFetcher{models: []DiscoveredModel{{
			ID:          "gemini-discovered",
			DisplayName: "Gemini Discovered",
		}}},
		authLoader: staticAuthLoader{auths: []*coreauth.Auth{{
			Provider: ANTIGRAVITY_PROVIDER,
			Metadata: map[string]interface{}{
				"access_token": "secret-token-must-not-be-persisted",
			},
		}}},
		store: snapshotStore{path: snapshotPath},
		now:   func() time.Time { return now },
	}

	service.refresh(context.Background())
	rawSnapshot, errRead := os.ReadFile(snapshotPath)
	if errRead != nil {
		t.Fatalf("read snapshot: %v", errRead)
	}
	if strings.Contains(string(rawSnapshot), "secret-token-must-not-be-persisted") || strings.Contains(string(rawSnapshot), "access_token") {
		t.Fatalf("snapshot contains credential data: %s", rawSnapshot)
	}
	snapshot, errLoad := service.store.load()
	if errLoad != nil {
		t.Fatalf("load snapshot: %v", errLoad)
	}
	if len(snapshot.Models) != 1 || snapshot.Models[0].State != VerificationStatePending {
		t.Fatalf("snapshot models = %+v, want one pending model", snapshot.Models)
	}
}

func TestRefreshKeepsExistingSnapshotWhenFetchFails(t *testing.T) {
	temporaryDirectory := t.TempDir()
	snapshotPath := filepath.Join(temporaryDirectory, "catalog.json")
	store := snapshotStore{path: snapshotPath}
	existing := Snapshot{Version: MODEL_CATALOG_VERSION, Models: []CatalogModel{{ID: "verified", State: VerificationStateVerified}}}
	if errSave := store.save(existing); errSave != nil {
		t.Fatalf("save fixture snapshot: %v", errSave)
	}
	service := catalogService{
		settings:   catalogSettings{snapshotPath: snapshotPath},
		fetcher:    staticFetcher{err: errors.New("upstream unavailable")},
		authLoader: staticAuthLoader{auths: []*coreauth.Auth{{Provider: ANTIGRAVITY_PROVIDER, Metadata: map[string]interface{}{"access_token": "token"}}}},
		store:      store,
		now:        time.Now,
	}

	service.refresh(context.Background())
	snapshot, errLoad := store.load()
	if errLoad != nil {
		t.Fatalf("load snapshot: %v", errLoad)
	}
	if len(snapshot.Models) != 1 || snapshot.Models[0].State != VerificationStateVerified {
		t.Fatalf("snapshot models = %+v, want preserved verified model", snapshot.Models)
	}
	if snapshot.LastError == "" {
		t.Fatal("LastError is empty after failed discovery")
	}
}

func TestRefreshVerifiesPendingModelForSelectedCredential(t *testing.T) {
	temporaryDirectory := t.TempDir()
	snapshotPath := filepath.Join(temporaryDirectory, "catalog.json")
	now := time.Date(2026, time.August, 13, 12, 0, 0, 0, time.UTC)
	auth := &coreauth.Auth{
		ID:       "selected-auth",
		Provider: ANTIGRAVITY_PROVIDER,
		Metadata: map[string]interface{}{"access_token": "token", "project_id": "project"},
	}
	service := catalogService{
		settings: catalogSettings{
			snapshotPath:           snapshotPath,
			verifyEnabled:          true,
			maxVerificationsPerRun: 1,
		},
		fetcher:    staticFetcher{models: []DiscoveredModel{{ID: "gemini-verified", DisplayName: "Verified"}}},
		authLoader: staticAuthLoader{auths: []*coreauth.Auth{auth}},
		verifier:   staticModelVerifier{},
		store:      snapshotStore{path: snapshotPath},
		now:        func() time.Time { return now },
	}

	service.refresh(context.Background())
	snapshot, errLoad := service.store.load()
	if errLoad != nil {
		t.Fatalf("load snapshot: %v", errLoad)
	}
	if len(snapshot.Models) != 1 || snapshot.Models[0].State != VerificationStateVerified {
		t.Fatalf("snapshot models = %+v, want verified model", snapshot.Models)
	}
	if snapshot.Models[0].VerifiedAuthFingerprint != AuthFingerprint(auth) {
		t.Fatalf("verified auth fingerprint = %q", snapshot.Models[0].VerifiedAuthFingerprint)
	}
}

func TestVerifyPendingModelsPrioritizesUntriedModels(t *testing.T) {
	verifier := &recordingModelVerifier{}
	service := catalogService{
		settings: catalogSettings{maxVerificationsPerRun: 1},
		verifier: verifier,
	}
	snapshot := &Snapshot{Models: []CatalogModel{
		{ID: "retry-first", State: VerificationStatePending, VerificationError: "verification unavailable"},
		{ID: "untried-second", State: VerificationStatePending},
	}}

	auth := &coreauth.Auth{ID: "auth", Provider: ANTIGRAVITY_PROVIDER}
	snapshot.Models[0].AvailableAuthFingerprints = []string{AuthFingerprint(auth)}
	snapshot.Models[1].AvailableAuthFingerprints = []string{AuthFingerprint(auth)}
	service.verifyPendingModels(context.Background(), snapshot, []*coreauth.Auth{auth}, time.Now())

	if len(verifier.verificationCalls) != 1 || verifier.verificationCalls[0].modelID != "untried-second" {
		t.Fatalf("verified models = %v, want untried-second", verifier.verificationCalls)
	}
}

func TestRefreshMergesModelsAcrossCredentials(t *testing.T) {
	temporaryDirectory := t.TempDir()
	authA := &coreauth.Auth{ID: "auth-a", Provider: ANTIGRAVITY_PROVIDER, Metadata: map[string]interface{}{"access_token": "token-a"}}
	authB := &coreauth.Auth{ID: "auth-b", Provider: ANTIGRAVITY_PROVIDER, Metadata: map[string]interface{}{"access_token": "token-b"}}
	service := catalogService{
		settings: catalogSettings{snapshotPath: filepath.Join(temporaryDirectory, "catalog.json")},
		fetcher: perAuthFetcher{responses: map[string]fetchResponse{
			AuthFingerprint(authA): {models: []DiscoveredModel{{ID: "gemini-a"}}},
			AuthFingerprint(authB): {models: []DiscoveredModel{{ID: "gemini-b"}}},
		}},
		authLoader: staticAuthLoader{auths: []*coreauth.Auth{authA, authB}},
		store:      snapshotStore{path: filepath.Join(temporaryDirectory, "catalog.json")},
		now:        time.Now,
	}

	service.refresh(context.Background())
	snapshot, errLoad := service.store.load()
	if errLoad != nil {
		t.Fatalf("load snapshot: %v", errLoad)
	}
	if len(snapshot.Models) != 2 {
		t.Fatalf("model count = %d, want 2", len(snapshot.Models))
	}
	if snapshot.Models[0].ID != "gemini-a" || snapshot.Models[0].AvailableAuthFingerprints[0] != AuthFingerprint(authA) {
		t.Fatalf("first model = %+v", snapshot.Models[0])
	}
	if snapshot.Models[1].ID != "gemini-b" || snapshot.Models[1].AvailableAuthFingerprints[0] != AuthFingerprint(authB) {
		t.Fatalf("second model = %+v", snapshot.Models[1])
	}
}

func TestRefreshRetainsModelWhenItsCredentialFetchIsUnavailable(t *testing.T) {
	temporaryDirectory := t.TempDir()
	snapshotPath := filepath.Join(temporaryDirectory, "catalog.json")
	authA := &coreauth.Auth{ID: "auth-a", Provider: ANTIGRAVITY_PROVIDER, Metadata: map[string]interface{}{"access_token": "token-a"}}
	authB := &coreauth.Auth{ID: "auth-b", Provider: ANTIGRAVITY_PROVIDER, Metadata: map[string]interface{}{"access_token": "token-b"}}
	store := snapshotStore{path: snapshotPath}
	existing := Snapshot{Version: MODEL_CATALOG_VERSION, Models: []CatalogModel{{
		ID:                        "gemini-new",
		State:                     VerificationStateVerified,
		VerifiedAuthFingerprint:   AuthFingerprint(authB),
		AvailableAuthFingerprints: []string{AuthFingerprint(authB)},
	}}}
	if errSave := store.save(existing); errSave != nil {
		t.Fatalf("save fixture snapshot: %v", errSave)
	}
	service := catalogService{
		settings: catalogSettings{snapshotPath: snapshotPath},
		fetcher: perAuthFetcher{responses: map[string]fetchResponse{
			AuthFingerprint(authA): {models: []DiscoveredModel{{ID: "gemini-other"}}},
			AuthFingerprint(authB): {err: errors.New("temporary upstream error")},
		}},
		authLoader: staticAuthLoader{auths: []*coreauth.Auth{authA, authB}},
		store:      store,
		now:        time.Now,
	}

	service.refresh(context.Background())
	snapshot, errLoad := store.load()
	if errLoad != nil {
		t.Fatalf("load snapshot: %v", errLoad)
	}
	for _, model := range snapshot.Models {
		if model.ID == "gemini-new" && model.State == VerificationStateVerified {
			return
		}
	}
	t.Fatalf("snapshot models = %+v, want verified gemini-new retained", snapshot.Models)
}

func TestMergeModelsRequiresReverificationWhenVerifiedCredentialStopsAdvertising(t *testing.T) {
	authA := &coreauth.Auth{ID: "auth-a", Provider: ANTIGRAVITY_PROVIDER}
	authB := &coreauth.Auth{ID: "auth-b", Provider: ANTIGRAVITY_PROVIDER}
	models := mergeModels(
		[]CatalogModel{{
			ID:                        "gemini-new",
			State:                     VerificationStateVerified,
			AvailableAuthFingerprints: []string{AuthFingerprint(authA), AuthFingerprint(authB)},
			VerifiedAuthFingerprint:   AuthFingerprint(authB),
		}},
		[]DiscoveredModel{{
			ID:                        "gemini-new",
			AvailableAuthFingerprints: []string{AuthFingerprint(authA)},
		}},
		map[string]struct{}{},
		time.Now(),
	)
	if len(models) != 1 || models[0].State != VerificationStatePending {
		t.Fatalf("models = %+v, want pending model", models)
	}
	if models[0].VerifiedAuthFingerprint != "" {
		t.Fatalf("verified auth fingerprint = %q, want empty", models[0].VerifiedAuthFingerprint)
	}
}

func TestVerifyPendingModelsTriesAnotherCredentialAfterNotFound(t *testing.T) {
	authA := &coreauth.Auth{ID: "auth-a", Provider: ANTIGRAVITY_PROVIDER}
	authB := &coreauth.Auth{ID: "auth-b", Provider: ANTIGRAVITY_PROVIDER}
	authFingerprints := uniqueSortedStrings([]string{AuthFingerprint(authA), AuthFingerprint(authB)})
	firstFingerprint := authFingerprints[0]
	secondFingerprint := authFingerprints[1]
	verifier := &recordingModelVerifier{errors: map[string]error{
		firstFingerprint + ":gemini-new": modelNotFoundError{},
	}}
	service := catalogService{settings: catalogSettings{maxVerificationsPerRun: 1}, verifier: verifier}
	snapshot := &Snapshot{Models: []CatalogModel{{
		ID:                        "gemini-new",
		State:                     VerificationStatePending,
		AvailableAuthFingerprints: authFingerprints,
	}}}

	service.verifyPendingModels(context.Background(), snapshot, []*coreauth.Auth{authA, authB}, time.Now())
	if snapshot.Models[0].State != VerificationStatePending {
		t.Fatalf("state after first verification = %s, want pending", snapshot.Models[0].State)
	}
	service.verifyPendingModels(context.Background(), snapshot, []*coreauth.Auth{authA, authB}, time.Now())
	if snapshot.Models[0].State != VerificationStateVerified {
		t.Fatalf("state after second verification = %s, want verified", snapshot.Models[0].State)
	}
	if len(verifier.verificationCalls) != 2 || verifier.verificationCalls[1].authFingerprint != secondFingerprint {
		t.Fatalf("verification calls = %+v, want second credential", verifier.verificationCalls)
	}
}

func TestLoadVerifiedModelsUsesSuccessfulCredentialOnly(t *testing.T) {
	temporaryDirectory := t.TempDir()
	snapshotPath := filepath.Join(temporaryDirectory, "catalog.json")
	authA := &coreauth.Auth{ID: "auth-a", Provider: ANTIGRAVITY_PROVIDER}
	authB := &coreauth.Auth{ID: "auth-b", Provider: ANTIGRAVITY_PROVIDER}
	store := snapshotStore{path: snapshotPath}
	fixture := Snapshot{Version: MODEL_CATALOG_VERSION, Models: []CatalogModel{{
		ID:                        "gemini-new",
		State:                     VerificationStateVerified,
		AvailableAuthFingerprints: []string{AuthFingerprint(authA), AuthFingerprint(authB)},
		VerifiedAuthFingerprint:   AuthFingerprint(authB),
	}}}
	if errSave := store.save(fixture); errSave != nil {
		t.Fatalf("save fixture snapshot: %v", errSave)
	}
	if models := LoadVerifiedModels(snapshotPath, authA); len(models) != 0 {
		t.Fatalf("models for unverified credential = %+v, want none", models)
	}
	if models := LoadVerifiedModels(snapshotPath, authB); len(models) != 1 || models[0].ID != "gemini-new" {
		t.Fatalf("models for verified credential = %+v", models)
	}
}

func TestLoadVerifiedModelsFiltersByCredentialFingerprint(t *testing.T) {
	temporaryDirectory := t.TempDir()
	snapshotPath := filepath.Join(temporaryDirectory, "catalog.json")
	verifiedAuth := &coreauth.Auth{ID: "verified-auth", Provider: ANTIGRAVITY_PROVIDER}
	otherAuth := &coreauth.Auth{ID: "other-auth", Provider: ANTIGRAVITY_PROVIDER}
	store := snapshotStore{path: snapshotPath}
	fixture := Snapshot{
		Version: MODEL_CATALOG_VERSION,
		Models: []CatalogModel{{
			ID:                      "gemini-verified",
			DisplayName:             "Verified",
			State:                   VerificationStateVerified,
			VerifiedAuthFingerprint: AuthFingerprint(verifiedAuth),
		}},
	}
	if errSave := store.save(fixture); errSave != nil {
		t.Fatalf("save fixture snapshot: %v", errSave)
	}
	if models := LoadVerifiedModels(snapshotPath, otherAuth); len(models) != 0 {
		t.Fatalf("other credential models = %+v, want none", models)
	}
	models := LoadVerifiedModels(snapshotPath, verifiedAuth)
	if len(models) != 1 || models[0].ID != "gemini-verified" {
		t.Fatalf("verified credential models = %+v", models)
	}
}

func TestAuthFingerprintIgnoresFileName(t *testing.T) {
	catalogAuth := &coreauth.Auth{
		ID:       "antigravity-account.json",
		Provider: ANTIGRAVITY_PROVIDER,
		FileName: "antigravity-account.json",
	}
	watcherAuth := catalogAuth.Clone()
	watcherAuth.FileName = ""

	if AuthFingerprint(catalogAuth) != AuthFingerprint(watcherAuth) {
		t.Fatal("auth fingerprint changed when only FileName changed")
	}
}

func TestLoadVerifiedModelsAcceptsLegacyFileNameFingerprint(t *testing.T) {
	temporaryDirectory := t.TempDir()
	snapshotPath := filepath.Join(temporaryDirectory, "catalog.json")
	catalogAuth := &coreauth.Auth{
		ID:       "antigravity-account.json",
		Provider: ANTIGRAVITY_PROVIDER,
		FileName: "antigravity-account.json",
	}
	watcherAuth := catalogAuth.Clone()
	watcherAuth.FileName = ""
	store := snapshotStore{path: snapshotPath}
	fixture := Snapshot{
		Version: MODEL_CATALOG_VERSION,
		Models: []CatalogModel{{
			ID:                      "gemini-3.7-flash-medium",
			State:                   VerificationStateVerified,
			VerifiedAuthFingerprint: legacyAuthFingerprint(catalogAuth),
		}},
	}
	if errSave := store.save(fixture); errSave != nil {
		t.Fatalf("save fixture snapshot: %v", errSave)
	}

	models := LoadVerifiedModels(snapshotPath, watcherAuth)
	if len(models) != 1 || models[0].ID != "gemini-3.7-flash-medium" {
		t.Fatalf("legacy verified models = %+v", models)
	}
}

func TestNormalizeSnapshotAuthFingerprintsMigratesLegacyFingerprints(t *testing.T) {
	auth := &coreauth.Auth{
		ID:       "antigravity-account.json",
		Provider: ANTIGRAVITY_PROVIDER,
		FileName: "antigravity-account.json",
	}
	legacyFingerprint := legacyAuthFingerprint(auth)
	currentFingerprint := AuthFingerprint(auth)
	snapshot := Snapshot{Models: []CatalogModel{{
		AvailableAuthFingerprints:       []string{legacyFingerprint},
		RejectedAuthFingerprints:        []string{legacyFingerprint},
		LastVerificationAuthFingerprint: legacyFingerprint,
		VerifiedAuthFingerprint:         legacyFingerprint,
	}}}

	normalizeSnapshotAuthFingerprints(&snapshot, []*coreauth.Auth{auth})
	model := snapshot.Models[0]
	if model.VerifiedAuthFingerprint != currentFingerprint {
		t.Fatalf("verified fingerprint = %q, want %q", model.VerifiedAuthFingerprint, currentFingerprint)
	}
	if model.LastVerificationAuthFingerprint != currentFingerprint {
		t.Fatalf("last verification fingerprint = %q, want %q", model.LastVerificationAuthFingerprint, currentFingerprint)
	}
	if len(model.AvailableAuthFingerprints) != 1 || model.AvailableAuthFingerprints[0] != currentFingerprint {
		t.Fatalf("available fingerprints = %v", model.AvailableAuthFingerprints)
	}
	if len(model.RejectedAuthFingerprints) != 1 || model.RejectedAuthFingerprints[0] != currentFingerprint {
		t.Fatalf("rejected fingerprints = %v", model.RejectedAuthFingerprints)
	}
}

func TestParseSettingsRejectsInvalidIntervals(t *testing.T) {
	_, errSettings := parseSettings(StartOptions{AuthDirectory: t.TempDir(), RefreshInterval: "invalid", BaseDirectory: t.TempDir()})
	if errSettings == nil {
		t.Fatal("parseSettings() error = nil, want invalid interval error")
	}
}
