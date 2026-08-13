// Package antigravitycatalog discovers Antigravity model metadata without exposing it publicly.
package antigravitycatalog

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v7/internal/misc"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/util"
	sdkAuth "github.com/router-for-me/CLIProxyAPI/v7/sdk/auth"
	coreauth "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/auth"
	"github.com/router-for-me/CLIProxyAPI/v7/sdk/proxyutil"
	log "github.com/sirupsen/logrus"
)

const (
	DEFAULT_REFRESH_INTERVAL = 6 * time.Hour
	DEFAULT_SNAPSHOT_FILE    = "antigravity-model-catalog.json"
	MAX_RESPONSE_BYTES       = 4 << 20
	MODEL_CATALOG_VERSION    = 1
	ANTIGRAVITY_PROVIDER     = "antigravity"
)

var antigravityCatalogBaseURLs = []string{
	"https://cloudcode-pa.googleapis.com",
	"https://daily-cloudcode-pa.googleapis.com",
	"https://daily-cloudcode-pa.sandbox.googleapis.com",
}

// VerificationState describes the lifecycle of a discovered Antigravity model.
type VerificationState string

const (
	VerificationStatePending  VerificationState = "pending_verification"
	VerificationStateVerified VerificationState = "verified"
	VerificationStateRejected VerificationState = "rejected"
	VerificationStateStale    VerificationState = "stale"
)

// StartOptions contains the local-only discovery worker settings.
type StartOptions struct {
	Enabled         bool
	AuthDirectory   string
	SnapshotPath    string
	RefreshInterval string
	BaseDirectory   string
}

// DiscoveredModel contains upstream model metadata that is safe to persist.
type DiscoveredModel struct {
	ID                  string
	DisplayName         string
	ContextLength       int
	MaxCompletionTokens int
	SupportsWebSearch   bool
}

// CatalogModel is a discovered model plus its verification lifecycle state.
type CatalogModel struct {
	ID                  string            `json:"id"`
	DisplayName         string            `json:"display_name"`
	ContextLength       int               `json:"context_length,omitempty"`
	MaxCompletionTokens int               `json:"max_completion_tokens,omitempty"`
	SupportsWebSearch   bool              `json:"supports_web_search,omitempty"`
	State               VerificationState `json:"state"`
	LastSeenAt          time.Time         `json:"last_seen_at"`
	LastVerifiedAt      time.Time         `json:"last_verified_at,omitempty"`
	LastRejectedAt      time.Time         `json:"last_rejected_at,omitempty"`
	VerificationError   string            `json:"verification_error,omitempty"`
}

// Snapshot contains the redacted local catalog state.
type Snapshot struct {
	Version          int            `json:"version"`
	LastAttemptAt    time.Time      `json:"last_attempt_at,omitempty"`
	LastSuccessfulAt time.Time      `json:"last_successful_at,omitempty"`
	LastError        string         `json:"last_error,omitempty"`
	Models           []CatalogModel `json:"models"`
}

// IFetcher fetches safe Antigravity model metadata using one selected credential.
type IFetcher interface {
	Fetch(context.Context, *coreauth.Auth) ([]DiscoveredModel, error)
}

// IAuthLoader loads available credentials without modifying them.
type IAuthLoader interface {
	Load(context.Context, string) ([]*coreauth.Auth, error)
}

type catalogService struct {
	settings   catalogSettings
	fetcher    IFetcher
	authLoader IAuthLoader
	store      snapshotStore
	now        func() time.Time
}

type catalogSettings struct {
	authDirectory   string
	snapshotPath    string
	refreshInterval time.Duration
}

type snapshotStore struct {
	path string
}

type fileAuthLoader struct{}

type upstreamFetcher struct{}

type antigravityModelsResponse struct {
	Models            map[string]antigravityModelResponse `json:"models"`
	WebSearchModelIDs []string                            `json:"webSearchModelIds"`
}

type antigravityModelResponse struct {
	DisplayName     string `json:"displayName"`
	MaxTokens       int    `json:"maxTokens"`
	MaxOutputTokens int    `json:"maxOutputTokens"`
}

type antigravityModelsRequest struct {
	Project string `json:"project,omitempty"`
}

// Start launches the opt-in discovery worker and returns immediately.
func Start(pContext context.Context, pOptions StartOptions) error {
	if !pOptions.Enabled {
		return nil
	}
	settings, errSettings := parseSettings(pOptions)
	if errSettings != nil {
		return errSettings
	}
	service := &catalogService{
		settings:   settings,
		fetcher:    upstreamFetcher{},
		authLoader: fileAuthLoader{},
		store:      snapshotStore{path: settings.snapshotPath},
		now:        time.Now,
	}
	go service.run(pContext)
	return nil
}

func parseSettings(pOptions StartOptions) (catalogSettings, error) {
	resolvedAuthDirectory, errDirectory := util.ResolveAuthDir(pOptions.AuthDirectory)
	if errDirectory != nil {
		return catalogSettings{}, fmt.Errorf("resolve Antigravity catalog auth directory: %w", errDirectory)
	}
	refreshInterval := DEFAULT_REFRESH_INTERVAL
	if strings.TrimSpace(pOptions.RefreshInterval) != "" {
		parsedInterval, errInterval := time.ParseDuration(pOptions.RefreshInterval)
		if errInterval != nil || parsedInterval <= 0 {
			return catalogSettings{}, fmt.Errorf("antigravity.model-catalog.refresh-interval must be positive")
		}
		refreshInterval = parsedInterval
	}
	snapshotPath := strings.TrimSpace(pOptions.SnapshotPath)
	if snapshotPath == "" {
		snapshotPath = DEFAULT_SNAPSHOT_FILE
	}
	if !filepath.IsAbs(snapshotPath) {
		snapshotPath = filepath.Join(pOptions.BaseDirectory, snapshotPath)
	}
	return catalogSettings{
		authDirectory:   resolvedAuthDirectory,
		snapshotPath:    filepath.Clean(snapshotPath),
		refreshInterval: refreshInterval,
	}, nil
}

func (s *catalogService) run(pContext context.Context) {
	s.refresh(pContext)
	ticker := time.NewTicker(s.settings.refreshInterval)
	defer ticker.Stop()
	log.WithField("interval", s.settings.refreshInterval).Info("Codexs Antigravity model discovery started")
	for {
		select {
		case <-pContext.Done():
			return
		case <-ticker.C:
			s.refresh(pContext)
		}
	}
}

func (s *catalogService) refresh(pContext context.Context) {
	snapshot, errLoad := s.store.load()
	if errLoad != nil {
		log.WithError(errLoad).Error("Codexs Antigravity catalog snapshot is unavailable")
		return
	}
	snapshot.LastAttemptAt = s.now().UTC()
	auth, errAuth := s.loadAuth(pContext)
	if errAuth != nil {
		s.saveFailure(snapshot, errAuth)
		return
	}
	models, errFetch := s.fetcher.Fetch(pContext, auth)
	if errFetch != nil {
		s.saveFailure(snapshot, errFetch)
		return
	}
	snapshot.Models = mergeModels(snapshot.Models, models, s.now().UTC())
	snapshot.LastSuccessfulAt = s.now().UTC()
	snapshot.LastError = ""
	if errSave := s.store.save(snapshot); errSave != nil {
		log.WithError(errSave).Error("Codexs Antigravity catalog snapshot save failed")
		return
	}
	log.WithField("model_count", len(models)).Info("Codexs Antigravity model discovery completed")
}

func (s *catalogService) saveFailure(pSnapshot Snapshot, pCause error) {
	pSnapshot.LastError = "discovery request failed"
	if errSave := s.store.save(pSnapshot); errSave != nil {
		log.WithError(errSave).Error("Codexs Antigravity catalog failure snapshot save failed")
	}
	log.WithError(pCause).Warn("Codexs Antigravity model discovery failed")
}

func (s *catalogService) loadAuth(pContext context.Context) (*coreauth.Auth, error) {
	auths, errLoad := s.authLoader.Load(pContext, s.settings.authDirectory)
	if errLoad != nil {
		return nil, fmt.Errorf("load Antigravity credentials: %w", errLoad)
	}
	for _, auth := range auths {
		if isUsableAntigravityAuth(auth) {
			return auth, nil
		}
	}
	return nil, fmt.Errorf("no enabled Antigravity credential with an access token is available")
}

func (fileAuthLoader) Load(pContext context.Context, pDirectory string) ([]*coreauth.Auth, error) {
	store := sdkAuth.NewFileTokenStore()
	store.SetBaseDir(pDirectory)
	return store.List(pContext)
}

func isUsableAntigravityAuth(pAuth *coreauth.Auth) bool {
	if pAuth == nil || pAuth.Disabled || !strings.EqualFold(strings.TrimSpace(pAuth.Provider), ANTIGRAVITY_PROVIDER) {
		return false
	}
	return getAccessToken(pAuth) != ""
}

func getAccessToken(pAuth *coreauth.Auth) string {
	if pAuth == nil || pAuth.Metadata == nil {
		return ""
	}
	value, hasValue := pAuth.Metadata["access_token"]
	if !hasValue {
		return ""
	}
	token, isString := value.(string)
	if !isString {
		return ""
	}
	return strings.TrimSpace(token)
}

func (upstreamFetcher) Fetch(pContext context.Context, pAuth *coreauth.Auth) ([]DiscoveredModel, error) {
	accessToken := getAccessToken(pAuth)
	if accessToken == "" {
		return nil, fmt.Errorf("Antigravity credential has no access token")
	}
	requestBody, errMarshal := json.Marshal(antigravityModelsRequest{Project: getProjectID(pAuth)})
	if errMarshal != nil {
		return nil, fmt.Errorf("marshal Antigravity model catalog request: %w", errMarshal)
	}
	httpClient, errClient := createHTTPClient(pAuth)
	if errClient != nil {
		return nil, errClient
	}
	for _, baseURL := range antigravityCatalogBaseURLs {
		models, errFetch := fetchModelsFromEndpoint(pContext, httpClient, baseURL, accessToken, requestBody)
		if errFetch == nil {
			return models, nil
		}
	}
	return nil, fmt.Errorf("Antigravity model catalog could not be fetched from any upstream endpoint")
}

func createHTTPClient(pAuth *coreauth.Auth) (*http.Client, error) {
	httpClient := &http.Client{}
	transport, _, errTransport := proxyutil.BuildHTTPTransport(pAuth.ProxyURL)
	if errTransport != nil {
		return nil, fmt.Errorf("build Antigravity catalog transport: %w", errTransport)
	}
	if transport != nil {
		httpClient.Transport = transport
	}
	return httpClient, nil
}

func getProjectID(pAuth *coreauth.Auth) string {
	if pAuth == nil || pAuth.Metadata == nil {
		return ""
	}
	value, hasValue := pAuth.Metadata["project_id"]
	if !hasValue {
		return ""
	}
	projectID, isString := value.(string)
	if !isString {
		return ""
	}
	return strings.TrimSpace(projectID)
}

func fetchModelsFromEndpoint(pContext context.Context, pClient *http.Client, pBaseURL, pAccessToken string, pRequestBody []byte) ([]DiscoveredModel, error) {
	endpoint := strings.TrimRight(pBaseURL, "/") + "/v1internal:fetchAvailableModels"
	request, errRequest := http.NewRequestWithContext(pContext, http.MethodPost, endpoint, bytes.NewReader(pRequestBody))
	if errRequest != nil {
		return nil, fmt.Errorf("create Antigravity model catalog request: %w", errRequest)
	}
	request.Header.Set("Authorization", "Bearer "+pAccessToken)
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("User-Agent", misc.AntigravityUserAgent())
	response, errRequest := pClient.Do(request)
	if errRequest != nil {
		return nil, fmt.Errorf("execute Antigravity model catalog request: %w", errRequest)
	}
	defer func() {
		if errClose := response.Body.Close(); errClose != nil {
			log.WithError(errClose).Debug("close Antigravity model catalog response")
		}
	}()
	if response.StatusCode < http.StatusOK || response.StatusCode >= http.StatusMultipleChoices {
		return nil, fmt.Errorf("Antigravity model catalog returned status %d", response.StatusCode)
	}
	responseBody, errRead := io.ReadAll(io.LimitReader(response.Body, MAX_RESPONSE_BYTES))
	if errRead != nil {
		return nil, fmt.Errorf("read Antigravity model catalog response: %w", errRead)
	}
	return parseModelsResponse(responseBody)
}

func parseModelsResponse(pResponseBody []byte) ([]DiscoveredModel, error) {
	var response antigravityModelsResponse
	if errUnmarshal := json.Unmarshal(pResponseBody, &response); errUnmarshal != nil {
		return nil, fmt.Errorf("decode Antigravity model catalog response: %w", errUnmarshal)
	}
	if len(response.Models) == 0 {
		return nil, fmt.Errorf("Antigravity model catalog response contains no models")
	}
	webSearchModels := make(map[string]bool, len(response.WebSearchModelIDs))
	for _, modelID := range response.WebSearchModelIDs {
		webSearchModels[strings.TrimSpace(modelID)] = true
	}
	modelIDs := make([]string, 0, len(response.Models))
	for modelID := range response.Models {
		if strings.TrimSpace(modelID) != "" {
			modelIDs = append(modelIDs, modelID)
		}
	}
	sort.Strings(modelIDs)
	models := make([]DiscoveredModel, 0, len(modelIDs))
	for _, modelID := range modelIDs {
		metadata := response.Models[modelID]
		displayName := strings.TrimSpace(metadata.DisplayName)
		if displayName == "" {
			displayName = modelID
		}
		models = append(models, DiscoveredModel{
			ID:                  modelID,
			DisplayName:         displayName,
			ContextLength:       metadata.MaxTokens,
			MaxCompletionTokens: metadata.MaxOutputTokens,
			SupportsWebSearch:   webSearchModels[modelID],
		})
	}
	return models, nil
}

func mergeModels(pExisting []CatalogModel, pDiscovered []DiscoveredModel, pNow time.Time) []CatalogModel {
	existingModels := make(map[string]CatalogModel, len(pExisting))
	for _, model := range pExisting {
		existingModels[model.ID] = model
	}
	models := make([]CatalogModel, 0, len(pExisting)+len(pDiscovered))
	seenModels := make(map[string]bool, len(pDiscovered))
	for _, discoveredModel := range pDiscovered {
		model, exists := existingModels[discoveredModel.ID]
		if !exists {
			model = CatalogModel{ID: discoveredModel.ID, State: VerificationStatePending}
		}
		model.DisplayName = discoveredModel.DisplayName
		model.ContextLength = discoveredModel.ContextLength
		model.MaxCompletionTokens = discoveredModel.MaxCompletionTokens
		model.SupportsWebSearch = discoveredModel.SupportsWebSearch
		model.LastSeenAt = pNow
		if model.State == VerificationStateStale {
			model.State = VerificationStatePending
		}
		models = append(models, model)
		seenModels[model.ID] = true
	}
	for _, model := range pExisting {
		if seenModels[model.ID] {
			continue
		}
		model.State = VerificationStateStale
		models = append(models, model)
	}
	sort.Slice(models, func(pLeft, pRight int) bool {
		return models[pLeft].ID < models[pRight].ID
	})
	return models
}

func (s snapshotStore) load() (Snapshot, error) {
	rawSnapshot, errRead := os.ReadFile(s.path)
	if os.IsNotExist(errRead) {
		return Snapshot{Version: MODEL_CATALOG_VERSION, Models: []CatalogModel{}}, nil
	}
	if errRead != nil {
		return Snapshot{}, fmt.Errorf("read snapshot: %w", errRead)
	}
	var snapshot Snapshot
	if errUnmarshal := json.Unmarshal(rawSnapshot, &snapshot); errUnmarshal != nil {
		return Snapshot{}, fmt.Errorf("decode snapshot: %w", errUnmarshal)
	}
	if snapshot.Version != MODEL_CATALOG_VERSION {
		return Snapshot{}, fmt.Errorf("unsupported snapshot version %d", snapshot.Version)
	}
	return snapshot, nil
}

func (s snapshotStore) save(pSnapshot Snapshot) error {
	pSnapshot.Version = MODEL_CATALOG_VERSION
	if pSnapshot.Models == nil {
		pSnapshot.Models = []CatalogModel{}
	}
	rawSnapshot, errMarshal := json.MarshalIndent(pSnapshot, "", "  ")
	if errMarshal != nil {
		return fmt.Errorf("encode snapshot: %w", errMarshal)
	}
	if errDirectory := os.MkdirAll(filepath.Dir(s.path), 0o700); errDirectory != nil {
		return fmt.Errorf("create snapshot directory: %w", errDirectory)
	}
	temporaryFile, errTemporary := os.CreateTemp(filepath.Dir(s.path), ".antigravity-model-catalog-*.tmp")
	if errTemporary != nil {
		return fmt.Errorf("create snapshot temporary file: %w", errTemporary)
	}
	temporaryPath := temporaryFile.Name()
	defer func() {
		_ = os.Remove(temporaryPath)
	}()
	if errChmod := temporaryFile.Chmod(0o600); errChmod != nil {
		return fmt.Errorf("set snapshot temporary file permissions: %w", errChmod)
	}
	if _, errWrite := temporaryFile.Write(rawSnapshot); errWrite != nil {
		return fmt.Errorf("write snapshot temporary file: %w", errWrite)
	}
	if errClose := temporaryFile.Close(); errClose != nil {
		return fmt.Errorf("close snapshot temporary file: %w", errClose)
	}
	if errRename := os.Rename(temporaryPath, s.path); errRename == nil {
		return nil
	}
	if errWrite := os.WriteFile(s.path, rawSnapshot, 0o600); errWrite != nil {
		return fmt.Errorf("replace snapshot: %w", errWrite)
	}
	return nil
}
