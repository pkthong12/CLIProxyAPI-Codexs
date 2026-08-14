package cliproxy

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/router-for-me/CLIProxyAPI/v7/internal/codexs/antigravitycatalog"
	internalregistry "github.com/router-for-me/CLIProxyAPI/v7/internal/registry"
	coreauth "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/auth"
	"github.com/router-for-me/CLIProxyAPI/v7/sdk/config"
)

func TestAppendVerifiedAntigravityCatalogModelsScopesModelsToVerifiedCredential(t *testing.T) {
	temporaryDirectory := t.TempDir()
	configPath := filepath.Join(temporaryDirectory, "config.yaml")
	snapshotPath := filepath.Join(temporaryDirectory, "catalog.json")
	verifiedAuth := &coreauth.Auth{ID: "verified-auth", Provider: "antigravity"}
	otherAuth := &coreauth.Auth{ID: "other-auth", Provider: "antigravity"}
	snapshot := antigravitycatalog.Snapshot{
		Version: 1,
		Models: []antigravitycatalog.CatalogModel{{
			ID:                      "gemini-verified",
			DisplayName:             "Gemini Verified",
			ContextLength:           1000,
			MaxCompletionTokens:     10,
			State:                   antigravitycatalog.VerificationStateVerified,
			VerifiedAuthFingerprint: antigravitycatalog.AuthFingerprint(verifiedAuth),
		}},
	}
	rawSnapshot, errMarshal := json.Marshal(snapshot)
	if errMarshal != nil {
		t.Fatalf("marshal snapshot: %v", errMarshal)
	}
	if errWrite := os.WriteFile(snapshotPath, rawSnapshot, 0o600); errWrite != nil {
		t.Fatalf("write snapshot: %v", errWrite)
	}
	service := &Service{
		configPath: configPath,
		cfg:        &config.Config{},
	}
	service.cfg.Antigravity.ModelCatalog.ExposeVerified = true
	service.cfg.Antigravity.ModelCatalog.SnapshotPath = "catalog.json"
	verifiedModels := service.appendVerifiedAntigravityCatalogModels(nil, verifiedAuth)
	if len(verifiedModels) != 1 || verifiedModels[0].ID != "gemini-verified" {
		t.Fatalf("verified credential models = %+v", verifiedModels)
	}
	if models := service.appendVerifiedAntigravityCatalogModels(nil, otherAuth); len(models) != 0 {
		t.Fatalf("other credential models = %+v, want none", models)
	}
}

func TestRegisterStoredAuthModelsPublishesAllVerifiedAntigravityCatalogModels(t *testing.T) {
	temporaryDirectory := t.TempDir()
	configPath := filepath.Join(temporaryDirectory, "config.yaml")
	snapshotPath := filepath.Join(temporaryDirectory, "catalog.json")
	auth := &coreauth.Auth{ID: "verified-auth", Provider: "antigravity"}
	modelIDs := []string{"gemini-3.7-flash-high", "gemini-3.7-flash-medium", "gemini-3.7-flash-low"}
	models := make([]antigravitycatalog.CatalogModel, 0, len(modelIDs))
	for _, modelID := range modelIDs {
		models = append(models, antigravitycatalog.CatalogModel{
			ID:                      modelID,
			State:                   antigravitycatalog.VerificationStateVerified,
			VerifiedAuthFingerprint: antigravitycatalog.AuthFingerprint(auth),
		})
	}
	writeAntigravityCatalogSnapshot(t, snapshotPath, models)

	manager := coreauth.NewManager(nil, nil, nil)
	if _, errRegister := manager.Register(context.Background(), auth); errRegister != nil {
		t.Fatalf("register auth: %v", errRegister)
	}
	service := &Service{configPath: configPath, cfg: &config.Config{}, coreManager: manager}
	service.cfg.Antigravity.ModelCatalog.ExposeVerified = true
	service.cfg.Antigravity.ModelCatalog.SnapshotPath = "catalog.json"
	defer internalregistry.GetGlobalRegistry().UnregisterClient(auth.ID)

	service.registerStoredAuthModels(context.Background())
	for _, modelID := range modelIDs {
		if !GlobalModelRegistry().ClientSupportsModel(auth.ID, modelID) {
			t.Fatalf("stored auth does not support verified model %q", modelID)
		}
	}
}

func writeAntigravityCatalogSnapshot(t *testing.T, snapshotPath string, models []antigravitycatalog.CatalogModel) {
	t.Helper()
	rawSnapshot, errMarshal := json.Marshal(antigravitycatalog.Snapshot{Version: 1, Models: models})
	if errMarshal != nil {
		t.Fatalf("marshal snapshot: %v", errMarshal)
	}
	if errWrite := os.WriteFile(snapshotPath, rawSnapshot, 0o600); errWrite != nil {
		t.Fatalf("write snapshot: %v", errWrite)
	}
}
