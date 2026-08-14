// Package antigravitycatalog discovers Antigravity model metadata without exposing it publicly.
package antigravitycatalog

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v7/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/misc"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/registry"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/runtime/executor"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/util"
	sdkAuth "github.com/router-for-me/CLIProxyAPI/v7/sdk/auth"
	coreauth "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/auth"
	"github.com/router-for-me/CLIProxyAPI/v7/sdk/proxyutil"
	log "github.com/sirupsen/logrus"
)

const (
	DEFAULT_REFRESH_INTERVAL  = 6 * time.Hour
	DEFAULT_SNAPSHOT_FILE     = "antigravity-model-catalog.json"
	DEFAULT_MAX_VERIFICATIONS = 1
	MAX_RESPONSE_BYTES        = 4 << 20
	MODEL_CATALOG_VERSION     = 1
	ANTIGRAVITY_PROVIDER      = "antigravity"
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
	Enabled                bool
	VerifyEnabled          bool
	MaxVerificationsPerRun int
	AuthDirectory          string
	SnapshotPath           string
	RefreshInterval        string
	BaseDirectory          string
	Config                 *config.Config
}

// DiscoveredModel contains upstream model metadata that is safe to persist.
type DiscoveredModel struct {
	ID                        string
	DisplayName               string
	ContextLength             int
	MaxCompletionTokens       int
	SupportsWebSearch         bool
	AvailableAuthFingerprints []string
}

// CatalogModel is a discovered model plus its verification lifecycle state.
type CatalogModel struct {
	ID                              string            `json:"id"`
	DisplayName                     string            `json:"display_name"`
	ContextLength                   int               `json:"context_length,omitempty"`
	MaxCompletionTokens             int               `json:"max_completion_tokens,omitempty"`
	SupportsWebSearch               bool              `json:"supports_web_search,omitempty"`
	State                           VerificationState `json:"state"`
	LastSeenAt                      time.Time         `json:"last_seen_at"`
	LastVerifiedAt                  time.Time         `json:"last_verified_at,omitempty"`
	LastRejectedAt                  time.Time         `json:"last_rejected_at,omitempty"`
	VerificationError               string            `json:"verification_error,omitempty"`
	AvailableAuthFingerprints       []string          `json:"available_auth_fingerprints,omitempty"`
	LastVerificationAuthFingerprint string            `json:"last_verification_auth_fingerprint,omitempty"`
	RejectedAuthFingerprints        []string          `json:"rejected_auth_fingerprints,omitempty"`
	VerifiedAuthFingerprint         string            `json:"verified_auth_fingerprint,omitempty"`
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

// IModelVerifier verifies a candidate through the native Antigravity request path.
type IModelVerifier interface {
	Verify(context.Context, *config.Config, *coreauth.Auth, string) error
}

type catalogService struct {
	settings   catalogSettings
	fetcher    IFetcher
	authLoader IAuthLoader
	verifier   IModelVerifier
	store      snapshotStore
	now        func() time.Time
}

type catalogSettings struct {
	authDirectory          string
	snapshotPath           string
	refreshInterval        time.Duration
	verifyEnabled          bool
	maxVerificationsPerRun int
	config                 *config.Config
}

type snapshotStore struct {
	path string
}

type fileAuthLoader struct{}

type upstreamFetcher struct{}

type nativeModelVerifier struct{}

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
		verifier:   nativeModelVerifier{},
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
	snapshotPath := ResolveSnapshotPath(pOptions.BaseDirectory, pOptions.SnapshotPath)
	maxVerifications := pOptions.MaxVerificationsPerRun
	if maxVerifications <= 0 {
		maxVerifications = DEFAULT_MAX_VERIFICATIONS
	}
	return catalogSettings{
		authDirectory:          resolvedAuthDirectory,
		snapshotPath:           snapshotPath,
		refreshInterval:        refreshInterval,
		verifyEnabled:          pOptions.VerifyEnabled,
		maxVerificationsPerRun: maxVerifications,
		config:                 pOptions.Config,
	}, nil
}

// ResolveSnapshotPath returns the stable on-disk path for a catalog snapshot.
func ResolveSnapshotPath(pBaseDirectory, pSnapshotPath string) string {
	snapshotPath := strings.TrimSpace(pSnapshotPath)
	if snapshotPath == "" {
		snapshotPath = DEFAULT_SNAPSHOT_FILE
	}
	if !filepath.IsAbs(snapshotPath) {
		snapshotPath = filepath.Join(pBaseDirectory, snapshotPath)
	}
	return filepath.Clean(snapshotPath)
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
	previousModels := append([]CatalogModel(nil), snapshot.Models...)
	snapshot.LastAttemptAt = s.now().UTC()
	auths, errAuth := s.loadAuths(pContext)
	if errAuth != nil {
		s.saveFailure(snapshot, errAuth)
		return
	}
	normalizeSnapshotAuthFingerprints(&snapshot, auths)
	fetchResult, errFetch := s.fetchModels(pContext, auths)
	if errFetch != nil {
		s.saveFailure(snapshot, errFetch)
		return
	}
	now := s.now().UTC()
	snapshot.Models = mergeModels(snapshot.Models, fetchResult.models, fetchResult.unavailableAuthFingerprints, now)
	if s.settings.verifyEnabled {
		s.verifyPendingModels(pContext, &snapshot, auths, now)
	}
	snapshot.LastSuccessfulAt = s.now().UTC()
	snapshot.LastError = ""
	if errSave := s.store.save(snapshot); errSave != nil {
		log.WithError(errSave).Error("Codexs Antigravity catalog snapshot save failed")
		return
	}
	if hasEffectiveModelChange(previousModels, snapshot.Models) {
		registry.NotifyModelRefresh([]string{ANTIGRAVITY_PROVIDER})
	}
	log.WithField("model_count", len(fetchResult.models)).Info("Codexs Antigravity model discovery completed")
}

func (s *catalogService) verifyPendingModels(pContext context.Context, pSnapshot *Snapshot, pAuths []*coreauth.Auth, pNow time.Time) {
	if pSnapshot == nil {
		return
	}
	authsByFingerprint := indexAuthsByFingerprint(pAuths)
	for index, model := range pendingModelsForVerification(pSnapshot.Models) {
		if index >= s.settings.maxVerificationsPerRun {
			return
		}
		auth, fingerprint := selectVerificationAuth(model, authsByFingerprint)
		if auth == nil {
			continue
		}
		errVerify := s.verifier.Verify(pContext, s.settings.config, auth, model.ID)
		updateVerificationResult(model, fingerprint, pNow, errVerify)
	}
}

func indexAuthsByFingerprint(pAuths []*coreauth.Auth) map[string]*coreauth.Auth {
	authsByFingerprint := make(map[string]*coreauth.Auth, len(pAuths))
	for _, auth := range pAuths {
		fingerprint := AuthFingerprint(auth)
		if fingerprint != "" {
			authsByFingerprint[fingerprint] = auth
		}
	}
	return authsByFingerprint
}

func selectVerificationAuth(pModel *CatalogModel, pAuthsByFingerprint map[string]*coreauth.Auth) (*coreauth.Auth, string) {
	if pModel == nil {
		return nil, ""
	}
	for _, fingerprint := range pModel.AvailableAuthFingerprints {
		if containsString(pModel.RejectedAuthFingerprints, fingerprint) {
			continue
		}
		if fingerprint == pModel.LastVerificationAuthFingerprint {
			continue
		}
		if auth := pAuthsByFingerprint[fingerprint]; auth != nil {
			return auth, fingerprint
		}
	}
	for _, fingerprint := range pModel.AvailableAuthFingerprints {
		if containsString(pModel.RejectedAuthFingerprints, fingerprint) {
			continue
		}
		if auth := pAuthsByFingerprint[fingerprint]; auth != nil {
			return auth, fingerprint
		}
	}
	return nil, ""
}

func pendingModelsForVerification(pModels []CatalogModel) []*CatalogModel {
	pendingModels := make([]*CatalogModel, 0, len(pModels))
	for index := range pModels {
		if pModels[index].State == VerificationStatePending {
			pendingModels = append(pendingModels, &pModels[index])
		}
	}
	sort.SliceStable(pendingModels, func(pLeft, pRight int) bool {
		return strings.TrimSpace(pendingModels[pLeft].VerificationError) == "" && strings.TrimSpace(pendingModels[pRight].VerificationError) != ""
	})
	return pendingModels
}

func (nativeModelVerifier) Verify(pContext context.Context, pConfig *config.Config, pAuth *coreauth.Auth, pModelID string) error {
	return executor.VerifyAntigravityModel(pContext, pConfig, pAuth, pModelID)
}

func updateVerificationResult(pModel *CatalogModel, pAuthFingerprint string, pNow time.Time, pError error) {
	if pModel == nil {
		return
	}
	pModel.LastVerificationAuthFingerprint = pAuthFingerprint
	if pError == nil {
		pModel.State = VerificationStateVerified
		pModel.LastVerifiedAt = pNow
		pModel.VerifiedAuthFingerprint = pAuthFingerprint
		pModel.VerificationError = ""
		return
	}
	statusError, hasStatus := pError.(interface{ StatusCode() int })
	if hasStatus && statusError.StatusCode() == http.StatusNotFound {
		pModel.RejectedAuthFingerprints = uniqueSortedStrings(append(pModel.RejectedAuthFingerprints, pAuthFingerprint))
		if hasRemainingVerificationAuth(pModel) {
			pModel.VerificationError = "verification unavailable"
			return
		}
		pModel.State = VerificationStateRejected
		pModel.LastRejectedAt = pNow
		pModel.VerifiedAuthFingerprint = ""
		pModel.VerificationError = "model not found"
		return
	}
	pModel.VerificationError = "verification unavailable"
}

func (s *catalogService) saveFailure(pSnapshot Snapshot, pCause error) {
	pSnapshot.LastError = "discovery request failed"
	if errSave := s.store.save(pSnapshot); errSave != nil {
		log.WithError(errSave).Error("Codexs Antigravity catalog failure snapshot save failed")
	}
	log.WithError(pCause).Warn("Codexs Antigravity model discovery failed")
}

func (s *catalogService) loadAuths(pContext context.Context) ([]*coreauth.Auth, error) {
	auths, errLoad := s.authLoader.Load(pContext, s.settings.authDirectory)
	if errLoad != nil {
		return nil, fmt.Errorf("load Antigravity credentials: %w", errLoad)
	}
	usableAuths := make([]*coreauth.Auth, 0, len(auths))
	for _, auth := range auths {
		if isUsableAntigravityAuth(auth) {
			usableAuths = append(usableAuths, auth)
		}
	}
	if len(usableAuths) == 0 {
		return nil, fmt.Errorf("no enabled Antigravity credential with an access token is available")
	}
	sort.Slice(usableAuths, func(pLeft, pRight int) bool {
		return AuthFingerprint(usableAuths[pLeft]) < AuthFingerprint(usableAuths[pRight])
	})
	return usableAuths, nil
}

type fetchResult struct {
	models                      []DiscoveredModel
	unavailableAuthFingerprints map[string]struct{}
}

func (s *catalogService) fetchModels(pContext context.Context, pAuths []*coreauth.Auth) (fetchResult, error) {
	modelsByID := make(map[string]DiscoveredModel)
	unavailableAuthFingerprints := make(map[string]struct{})
	successfulFetches := 0
	for _, auth := range pAuths {
		authFingerprint := AuthFingerprint(auth)
		models, errFetch := s.fetcher.Fetch(pContext, auth)
		if errFetch != nil {
			unavailableAuthFingerprints[authFingerprint] = struct{}{}
			log.WithError(errFetch).WithField("auth_fingerprint", authFingerprint).Warn("Codexs Antigravity catalog fetch unavailable")
			continue
		}
		successfulFetches++
		mergeFetchedModels(modelsByID, models, authFingerprint)
	}
	if successfulFetches == 0 {
		return fetchResult{}, fmt.Errorf("Antigravity model catalog could not be fetched from any credential")
	}
	models := make([]DiscoveredModel, 0, len(modelsByID))
	for _, model := range modelsByID {
		model.AvailableAuthFingerprints = uniqueSortedStrings(model.AvailableAuthFingerprints)
		models = append(models, model)
	}
	sort.Slice(models, func(pLeft, pRight int) bool {
		return models[pLeft].ID < models[pRight].ID
	})
	return fetchResult{models: models, unavailableAuthFingerprints: unavailableAuthFingerprints}, nil
}

func mergeFetchedModels(pModelsByID map[string]DiscoveredModel, pModels []DiscoveredModel, pAuthFingerprint string) {
	if pAuthFingerprint == "" {
		return
	}
	for _, fetchedModel := range pModels {
		modelID := strings.TrimSpace(fetchedModel.ID)
		if modelID == "" {
			continue
		}
		existingModel, exists := pModelsByID[modelID]
		if !exists {
			existingModel = fetchedModel
		}
		existingModel.AvailableAuthFingerprints = append(existingModel.AvailableAuthFingerprints, pAuthFingerprint)
		pModelsByID[modelID] = existingModel
	}
}

func uniqueSortedStrings(pValues []string) []string {
	values := make(map[string]struct{}, len(pValues))
	for _, value := range pValues {
		value = strings.TrimSpace(value)
		if value != "" {
			values[value] = struct{}{}
		}
	}
	result := make([]string, 0, len(values))
	for value := range values {
		result = append(result, value)
	}
	sort.Strings(result)
	return result
}

func containsString(pValues []string, pValue string) bool {
	for _, value := range pValues {
		if value == pValue {
			return true
		}
	}
	return false
}

func hasRemainingVerificationAuth(pModel *CatalogModel) bool {
	if pModel == nil {
		return false
	}
	for _, fingerprint := range pModel.AvailableAuthFingerprints {
		if !containsString(pModel.RejectedAuthFingerprints, fingerprint) {
			return true
		}
	}
	return false
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

func mergeModels(pExisting []CatalogModel, pDiscovered []DiscoveredModel, pUnavailableAuthFingerprints map[string]struct{}, pNow time.Time) []CatalogModel {
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
		model.AvailableAuthFingerprints = mergeAvailableAuthFingerprints(
			discoveredModel.AvailableAuthFingerprints,
			model.AvailableAuthFingerprints,
			pUnavailableAuthFingerprints,
		)
		model.RejectedAuthFingerprints = retainedRejectedAuthFingerprints(model.RejectedAuthFingerprints, model.AvailableAuthFingerprints)
		resetVerificationWhenCredentialIsUnavailable(&model, pNow)
		model.LastSeenAt = pNow
		if model.State == VerificationStateStale {
			model.State = VerificationStatePending
			model.LastVerifiedAt = time.Time{}
			model.LastRejectedAt = time.Time{}
			model.VerifiedAuthFingerprint = ""
			model.VerificationError = ""
			model.LastVerificationAuthFingerprint = ""
			model.RejectedAuthFingerprints = nil
		}
		models = append(models, model)
		seenModels[model.ID] = true
	}
	for _, model := range pExisting {
		if seenModels[model.ID] {
			continue
		}
		if hasUnavailableAuthFingerprint(model.AvailableAuthFingerprints, pUnavailableAuthFingerprints) {
			models = append(models, model)
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

func mergeAvailableAuthFingerprints(pCurrent, pPrevious []string, pUnavailableAuthFingerprints map[string]struct{}) []string {
	availableAuthFingerprints := append([]string(nil), pCurrent...)
	for _, fingerprint := range pPrevious {
		if _, isUnavailable := pUnavailableAuthFingerprints[fingerprint]; isUnavailable {
			availableAuthFingerprints = append(availableAuthFingerprints, fingerprint)
		}
	}
	return uniqueSortedStrings(availableAuthFingerprints)
}

func hasUnavailableAuthFingerprint(pAvailableAuthFingerprints []string, pUnavailableAuthFingerprints map[string]struct{}) bool {
	for _, fingerprint := range pAvailableAuthFingerprints {
		if _, isUnavailable := pUnavailableAuthFingerprints[fingerprint]; isUnavailable {
			return true
		}
	}
	return false
}

func resetVerificationWhenCredentialIsUnavailable(pModel *CatalogModel, pNow time.Time) {
	if pModel == nil || pModel.State != VerificationStateVerified {
		return
	}
	if strings.TrimSpace(pModel.VerifiedAuthFingerprint) == "" {
		return
	}
	if containsString(pModel.AvailableAuthFingerprints, pModel.VerifiedAuthFingerprint) {
		return
	}
	pModel.State = VerificationStatePending
	pModel.LastVerifiedAt = time.Time{}
	pModel.VerifiedAuthFingerprint = ""
	pModel.LastVerificationAuthFingerprint = ""
	pModel.VerificationError = ""
	if !hasRemainingVerificationAuth(pModel) {
		pModel.State = VerificationStateRejected
		pModel.LastRejectedAt = pNow
		pModel.VerificationError = "model not found"
	}
}

func retainedRejectedAuthFingerprints(pRejected, pAvailable []string) []string {
	retained := make([]string, 0, len(pRejected))
	for _, fingerprint := range pRejected {
		if containsString(pAvailable, fingerprint) {
			retained = append(retained, fingerprint)
		}
	}
	return uniqueSortedStrings(retained)
}

func normalizeSnapshotAuthFingerprints(pSnapshot *Snapshot, pAuths []*coreauth.Auth) {
	if pSnapshot == nil || len(pSnapshot.Models) == 0 || len(pAuths) == 0 {
		return
	}
	fingerprints := make(map[string]string, len(pAuths))
	for _, auth := range pAuths {
		legacyFingerprint := legacyAuthFingerprint(auth)
		currentFingerprint := AuthFingerprint(auth)
		if legacyFingerprint != "" && currentFingerprint != "" {
			fingerprints[legacyFingerprint] = currentFingerprint
		}
	}
	for index := range pSnapshot.Models {
		model := &pSnapshot.Models[index]
		model.AvailableAuthFingerprints = normalizeFingerprints(model.AvailableAuthFingerprints, fingerprints)
		model.RejectedAuthFingerprints = normalizeFingerprints(model.RejectedAuthFingerprints, fingerprints)
		model.LastVerificationAuthFingerprint = normalizeFingerprint(model.LastVerificationAuthFingerprint, fingerprints)
		model.VerifiedAuthFingerprint = normalizeFingerprint(model.VerifiedAuthFingerprint, fingerprints)
	}
}

func normalizeFingerprints(pFingerprints []string, pMappings map[string]string) []string {
	if len(pFingerprints) == 0 || len(pMappings) == 0 {
		return pFingerprints
	}
	result := make([]string, 0, len(pFingerprints))
	for _, fingerprint := range pFingerprints {
		result = append(result, normalizeFingerprint(fingerprint, pMappings))
	}
	return uniqueSortedStrings(result)
}

func normalizeFingerprint(pFingerprint string, pMappings map[string]string) string {
	if replacement, exists := pMappings[pFingerprint]; exists {
		return replacement
	}
	return pFingerprint
}

func hasEffectiveModelChange(pBefore, pAfter []CatalogModel) bool {
	if len(pBefore) != len(pAfter) {
		return true
	}
	beforeByID := make(map[string]CatalogModel, len(pBefore))
	for _, model := range pBefore {
		beforeByID[model.ID] = model
	}
	for _, model := range pAfter {
		previous, exists := beforeByID[model.ID]
		if !exists || previous.DisplayName != model.DisplayName || previous.ContextLength != model.ContextLength || previous.MaxCompletionTokens != model.MaxCompletionTokens || previous.SupportsWebSearch != model.SupportsWebSearch || previous.State != model.State || previous.VerifiedAuthFingerprint != model.VerifiedAuthFingerprint {
			return true
		}
	}
	return false
}

// AuthFingerprint returns a stable non-reversible credential identity for local snapshots.
func AuthFingerprint(pAuth *coreauth.Auth) string {
	if pAuth == nil {
		return ""
	}
	identity := strings.Join([]string{
		strings.ToLower(strings.TrimSpace(pAuth.Provider)),
		strings.TrimSpace(pAuth.ID),
	}, "|")
	if identity == "|" {
		return ""
	}
	return fingerprintIdentity(identity)
}

func legacyAuthFingerprint(pAuth *coreauth.Auth) string {
	if pAuth == nil {
		return ""
	}
	fileName := strings.TrimSpace(pAuth.FileName)
	if fileName == "" {
		fileName = strings.TrimSpace(pAuth.ID)
	}
	identity := strings.Join([]string{
		strings.ToLower(strings.TrimSpace(pAuth.Provider)),
		strings.TrimSpace(pAuth.ID),
		fileName,
	}, "|")
	if identity == "||" {
		return ""
	}
	return fingerprintIdentity(identity)
}

func fingerprintIdentity(pIdentity string) string {
	digest := sha256.Sum256([]byte(pIdentity))
	return hex.EncodeToString(digest[:])
}

func authFingerprintMatches(pStoredFingerprint string, pAuth *coreauth.Auth) bool {
	if pStoredFingerprint == "" || pAuth == nil {
		return false
	}
	return pStoredFingerprint == AuthFingerprint(pAuth) || pStoredFingerprint == legacyAuthFingerprint(pAuth)
}

// LoadVerifiedModels returns only models verified with the supplied credential.
func LoadVerifiedModels(pSnapshotPath string, pAuth *coreauth.Auth) []DiscoveredModel {
	if AuthFingerprint(pAuth) == "" {
		return nil
	}
	snapshot, errLoad := (snapshotStore{path: pSnapshotPath}).load()
	if errLoad != nil {
		return nil
	}
	models := make([]DiscoveredModel, 0, len(snapshot.Models))
	for _, model := range snapshot.Models {
		if model.State != VerificationStateVerified || !authFingerprintMatches(model.VerifiedAuthFingerprint, pAuth) {
			continue
		}
		models = append(models, DiscoveredModel{
			ID:                  model.ID,
			DisplayName:         model.DisplayName,
			ContextLength:       model.ContextLength,
			MaxCompletionTokens: model.MaxCompletionTokens,
			SupportsWebSearch:   model.SupportsWebSearch,
		})
	}
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
