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
	verifiedModelIDs []string
}

func (v *recordingModelVerifier) Verify(_ context.Context, _ *config.Config, _ *coreauth.Auth, pModelID string) error {
	v.verifiedModelIDs = append(v.verifiedModelIDs, pModelID)
	return nil
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

	models := mergeModels(existing, discovered, now)
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

	service.verifyPendingModels(context.Background(), snapshot, &coreauth.Auth{ID: "auth", Provider: ANTIGRAVITY_PROVIDER}, time.Now())

	if len(verifier.verifiedModelIDs) != 1 || verifier.verifiedModelIDs[0] != "untried-second" {
		t.Fatalf("verified models = %v, want untried-second", verifier.verifiedModelIDs)
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

func TestParseSettingsRejectsInvalidIntervals(t *testing.T) {
	_, errSettings := parseSettings(StartOptions{AuthDirectory: t.TempDir(), RefreshInterval: "invalid", BaseDirectory: t.TempDir()})
	if errSettings == nil {
		t.Fatal("parseSettings() error = nil, want invalid interval error")
	}
}
