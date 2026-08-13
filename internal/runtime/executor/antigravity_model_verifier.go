package executor

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"strings"

	"github.com/router-for-me/CLIProxyAPI/v7/internal/config"
	cliproxyauth "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/auth"
	log "github.com/sirupsen/logrus"
)

const (
	antigravityVerificationResponseLimit = 64 << 10
	antigravityVerificationPayload       = `{"request":{"contents":[{"role":"user","parts":[{"text":"Reply with OK."}]}],"generationConfig":{"maxOutputTokens":1,"temperature":0}}}`
)

type antigravityModelVerificationError struct {
	statusCode int
}

func (e antigravityModelVerificationError) Error() string {
	return fmt.Sprintf("Antigravity model verification returned status %d", e.statusCode)
}

func (e antigravityModelVerificationError) StatusCode() int {
	return e.statusCode
}

// VerifyAntigravityModel sends one minimal native request without refreshing or persisting credentials.
func VerifyAntigravityModel(pContext context.Context, pConfig *config.Config, pAuth *cliproxyauth.Auth, pModelID string) error {
	modelID := strings.TrimSpace(pModelID)
	if modelID == "" {
		return fmt.Errorf("Antigravity model verification requires a model ID")
	}
	accessToken := verificationAccessToken(pAuth)
	if accessToken == "" {
		return fmt.Errorf("Antigravity model verification requires an access token")
	}
	if verificationProjectID(pAuth) == "" {
		return fmt.Errorf("Antigravity model verification requires project_id")
	}
	if pContext == nil {
		pContext = context.Background()
	}
	executor := NewAntigravityExecutor(pConfig)
	httpClient := newAntigravityHTTPClient(pContext, pConfig, pAuth, 0)
	var lastError error
	for _, baseURL := range antigravityBaseURLFallbackOrder(pAuth) {
		lastError = verifyAntigravityModelAtURL(pContext, executor, httpClient, pAuth, accessToken, modelID, baseURL)
		if lastError == nil {
			return nil
		}
	}
	return lastError
}

func verificationAccessToken(pAuth *cliproxyauth.Auth) string {
	if pAuth == nil || pAuth.Metadata == nil {
		return ""
	}
	value, isPresent := pAuth.Metadata["access_token"]
	if !isPresent {
		return ""
	}
	token, isString := value.(string)
	if !isString {
		return ""
	}
	return strings.TrimSpace(token)
}

func verificationProjectID(pAuth *cliproxyauth.Auth) string {
	if pAuth == nil || pAuth.Metadata == nil {
		return ""
	}
	value, isPresent := pAuth.Metadata["project_id"]
	if !isPresent {
		return ""
	}
	projectID, isString := value.(string)
	if !isString {
		return ""
	}
	return strings.TrimSpace(projectID)
}

func verifyAntigravityModelAtURL(pContext context.Context, pExecutor *AntigravityExecutor, pHTTPClient *http.Client, pAuth *cliproxyauth.Auth, pAccessToken, pModelID, pBaseURL string) error {
	request, errRequest := pExecutor.buildRequest(pContext, pAuth, pAccessToken, pModelID, []byte(antigravityVerificationPayload), false, "", pBaseURL)
	if errRequest != nil {
		return fmt.Errorf("build Antigravity verification request: %w", errRequest)
	}
	response, errRequest := pHTTPClient.Do(request)
	if errRequest != nil {
		return fmt.Errorf("execute Antigravity verification request: %w", errRequest)
	}
	defer func() {
		if errClose := response.Body.Close(); errClose != nil {
			log.WithError(errClose).Debug("close Antigravity verification response")
		}
	}()
	_, errRead := io.ReadAll(io.LimitReader(response.Body, antigravityVerificationResponseLimit))
	if errRead != nil {
		return fmt.Errorf("read Antigravity verification response: %w", errRead)
	}
	if response.StatusCode >= http.StatusOK && response.StatusCode < http.StatusMultipleChoices {
		return nil
	}
	return antigravityModelVerificationError{statusCode: response.StatusCode}
}
