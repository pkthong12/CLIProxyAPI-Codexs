package cliproxy

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/router-for-me/CLIProxyAPI/v7/internal/codexs/antigravitycatalog"
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
