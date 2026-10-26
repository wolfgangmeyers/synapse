package managed

import (
	"context"
	"crypto"
	"crypto/rsa"
	"crypto/sha256"
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"encoding/pem"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

var (
	vertexAuthMu sync.Mutex
	// vertexGenerateContentURLTemplate can be overridden in tests
	vertexGenerateContentURLTemplate = "https://%s/v1/projects/%s/locations/%s/publishers/google/models/%s:streamGenerateContent?alt=sse"
)

func vertexHost(location string) string {
	if location == "" || location == "global" {
		return "aiplatform.googleapis.com"
	}
	return location + "-aiplatform.googleapis.com"
}

func vertexModelRequiresGlobal(modelID string) bool {
	m := strings.ToLower(strings.TrimSpace(modelID))
	return strings.Contains(m, "3.8") || strings.Contains(m, "gemini-3") || strings.Contains(m, "gemini-4") || strings.Contains(m, "argon")
}

type vertexTokenCache struct {
	accessToken string
	expiresAt   time.Time
	projectID   string
	location    string
}

var currentVertexToken vertexTokenCache

type vertexADCFile struct {
	Type           string `json:"type"`
	ClientEmail    string `json:"client_email"`
	PrivateKey     string `json:"private_key"`
	ProjectID      string `json:"project_id"`
	ClientID       string `json:"client_id"`
	ClientSecret   string `json:"client_secret"`
	RefreshToken   string `json:"refresh_token"`
	QuotaProjectID string `json:"quota_project_id"`
	TokenURI       string `json:"token_uri"`
}

// resolveVertexADCInfo locates ADC credentials path, project, and location.
func (m *Manager) resolveVertexADCInfo(modelID ...string) (adcPath, projectID, location string, err error) {
	model := ""
	if len(modelID) > 0 {
		model = modelID[0]
	}
	userHome, _ := os.UserHomeDir()
	paths := []string{
		filepath.Join(m.config.PiAgentDir, "auth.json"),
		filepath.Join(m.config.ManagedHome, "auth.json"),
		filepath.Join(userHome, ".pi", "agent", "auth.json"),
		filepath.Join(os.Getenv("HOME"), ".pi", "agent", "auth.json"),
	}

	for _, p := range paths {
		if p == "" {
			continue
		}
		data, readErr := os.ReadFile(p)
		if readErr == nil && len(data) > 0 {
			var allAuth map[string]any
			if json.Unmarshal(data, &allAuth) == nil {
				if vRaw, ok := allAuth["google-vertex"].(map[string]any); ok && vRaw != nil {
					if envMap, ok := vRaw["env"].(map[string]any); ok && envMap != nil {
						if p, _ := envMap["GOOGLE_CLOUD_PROJECT"].(string); p != "" {
							projectID = p
						}
						if l, _ := envMap["GOOGLE_CLOUD_LOCATION"].(string); l != "" {
							location = l
						}
						if adc, _ := envMap["GOOGLE_APPLICATION_CREDENTIALS"].(string); adc != "" {
							adcPath = adc
						}
					}
				}
			}
			if adcPath != "" || projectID != "" {
				break
			}
		}
	}

	if adcPath == "" {
		if envADC := os.Getenv("GOOGLE_APPLICATION_CREDENTIALS"); envADC != "" {
			adcPath = envADC
		} else if hostHome := os.Getenv("MECHA_HOST_HOME"); hostHome != "" {
			candidate := filepath.Join(hostHome, ".config", "gcloud", "application_default_credentials.json")
			if _, err := os.Stat(candidate); err == nil {
				adcPath = candidate
			}
		} else if userHome != "" {
			candidate := filepath.Join(userHome, ".config", "gcloud", "application_default_credentials.json")
			if _, err := os.Stat(candidate); err == nil {
				adcPath = candidate
			}
		}
	}

	if projectID == "" {
		projectID = os.Getenv("GOOGLE_CLOUD_PROJECT")
		if projectID == "" {
			projectID = os.Getenv("GCLOUD_PROJECT")
		}
	}

	if location == "" {
		location = os.Getenv("GOOGLE_CLOUD_LOCATION")
	}
	if vertexModelRequiresGlobal(model) || location == "" {
		location = "global"
	}

	return adcPath, projectID, location, nil
}

// embeddedVertexAuth loads or refreshes an access token for Google Vertex AI using ADC.
func (m *Manager) embeddedVertexAuth(ctx context.Context, forceRefresh bool, modelID ...string) (token, projectID, location string, err error) {
	vertexAuthMu.Lock()
	defer vertexAuthMu.Unlock()

	model := ""
	if len(modelID) > 0 {
		model = modelID[0]
	}

	adcPath, resolvedProj, resolvedLoc, err := m.resolveVertexADCInfo(modelID...)
	if err != nil {
		return "", "", "", err
	}

	now := time.Now()
	if !forceRefresh && currentVertexToken.accessToken != "" && currentVertexToken.expiresAt.After(now.Add(60*time.Second)) {
		if resolvedProj == "" {
			resolvedProj = currentVertexToken.projectID
		}
		if resolvedLoc == "" {
			resolvedLoc = currentVertexToken.location
		}
		if vertexModelRequiresGlobal(model) || resolvedLoc == "" {
			resolvedLoc = "global"
		}
		return currentVertexToken.accessToken, resolvedProj, resolvedLoc, nil
	}

	if adcPath == "" {
		return "", "", "", errors.New("Google Application Default Credentials (ADC) path not found")
	}

	data, err := os.ReadFile(adcPath)
	if err != nil {
		return "", "", "", fmt.Errorf("failed to read Google ADC file %s: %w", adcPath, err)
	}

	var adc vertexADCFile
	if err := json.Unmarshal(data, &adc); err != nil {
		return "", "", "", fmt.Errorf("failed to parse Google ADC file: %w", err)
	}

	if resolvedProj == "" {
		if adc.ProjectID != "" {
			resolvedProj = adc.ProjectID
		} else if adc.QuotaProjectID != "" {
			resolvedProj = adc.QuotaProjectID
		}
	}

	var accessToken string
	var expiresIn int64

	switch adc.Type {
	case "service_account":
		accessToken, expiresIn, err = exchangeServiceAccountKey(ctx, &adc)
		if err != nil {
			return "", "", "", fmt.Errorf("service account token exchange failed: %w", err)
		}
	case "authorized_user":
		accessToken, expiresIn, err = exchangeAuthorizedUserToken(ctx, &adc)
		if err != nil {
			return "", "", "", fmt.Errorf("authorized user token exchange failed: %w", err)
		}
	default:
		// If type is empty or standard refresh_token exists, attempt refresh token exchange
		if adc.RefreshToken != "" {
			accessToken, expiresIn, err = exchangeAuthorizedUserToken(ctx, &adc)
			if err != nil {
				return "", "", "", fmt.Errorf("ADC refresh token exchange failed: %w", err)
			}
		} else {
			return "", "", "", fmt.Errorf("unsupported Google ADC credential type: %q", adc.Type)
		}
	}

	if resolvedProj == "" {
		resolvedProj = "default-project"
	}
	if vertexModelRequiresGlobal(model) || resolvedLoc == "" {
		resolvedLoc = "global"
	}

	currentVertexToken = vertexTokenCache{
		accessToken: accessToken,
		expiresAt:   now.Add(time.Duration(expiresIn) * time.Second),
		projectID:   resolvedProj,
		location:    resolvedLoc,
	}

	return accessToken, resolvedProj, resolvedLoc, nil
}

func exchangeAuthorizedUserToken(ctx context.Context, adc *vertexADCFile) (string, int64, error) {
	if adc.RefreshToken == "" {
		return "", 0, errors.New("missing refresh_token in authorized user credentials")
	}

	tokenURI := adc.TokenURI
	if tokenURI == "" {
		tokenURI = "https://oauth2.googleapis.com/token"
	}

	data := url.Values{}
	data.Set("client_id", adc.ClientID)
	data.Set("client_secret", adc.ClientSecret)
	data.Set("refresh_token", adc.RefreshToken)
	data.Set("grant_type", "refresh_token")

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, tokenURI, strings.NewReader(data.Encode()))
	if err != nil {
		return "", 0, fmt.Errorf("failed to create refresh request: %w", err)
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")

	client := &http.Client{Timeout: 30 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return "", 0, fmt.Errorf("oauth token request failed: %w", err)
	}
	defer resp.Body.Close()

	bodyBytes, err := io.ReadAll(resp.Body)
	if err != nil {
		return "", 0, fmt.Errorf("failed to read oauth token response: %w", err)
	}

	if resp.StatusCode != http.StatusOK {
		return "", 0, fmt.Errorf("oauth token request returned status %d: %s", resp.StatusCode, string(bodyBytes))
	}

	var tokenResp struct {
		AccessToken string `json:"access_token"`
		ExpiresIn   int64  `json:"expires_in"`
		TokenType   string `json:"token_type"`
	}
	if err := json.Unmarshal(bodyBytes, &tokenResp); err != nil {
		return "", 0, fmt.Errorf("failed to parse oauth token response: %w", err)
	}

	if tokenResp.AccessToken == "" {
		return "", 0, errors.New("empty access_token received from Google OAuth")
	}
	expiresIn := tokenResp.ExpiresIn
	if expiresIn <= 0 {
		expiresIn = 3600
	}
	return tokenResp.AccessToken, expiresIn, nil
}

func exchangeServiceAccountKey(ctx context.Context, adc *vertexADCFile) (string, int64, error) {
	if adc.ClientEmail == "" || adc.PrivateKey == "" {
		return "", 0, errors.New("service account missing client_email or private_key")
	}

	tokenURI := adc.TokenURI
	if tokenURI == "" {
		tokenURI = "https://oauth2.googleapis.com/token"
	}

	assertion, err := createServiceAccountJWT(adc.ClientEmail, adc.PrivateKey, tokenURI)
	if err != nil {
		return "", 0, fmt.Errorf("failed to create service account JWT: %w", err)
	}

	data := url.Values{}
	data.Set("grant_type", "urn:ietf:params:oauth:grant-type:jwt-bearer")
	data.Set("assertion", assertion)

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, tokenURI, strings.NewReader(data.Encode()))
	if err != nil {
		return "", 0, fmt.Errorf("failed to create jwt bearer request: %w", err)
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")

	client := &http.Client{Timeout: 30 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return "", 0, fmt.Errorf("service account token request failed: %w", err)
	}
	defer resp.Body.Close()

	bodyBytes, err := io.ReadAll(resp.Body)
	if err != nil {
		return "", 0, fmt.Errorf("failed to read service account token response: %w", err)
	}

	if resp.StatusCode != http.StatusOK {
		return "", 0, fmt.Errorf("service account token request returned status %d: %s", resp.StatusCode, string(bodyBytes))
	}

	var tokenResp struct {
		AccessToken string `json:"access_token"`
		ExpiresIn   int64  `json:"expires_in"`
		TokenType   string `json:"token_type"`
	}
	if err := json.Unmarshal(bodyBytes, &tokenResp); err != nil {
		return "", 0, fmt.Errorf("failed to parse service account token response: %w", err)
	}

	if tokenResp.AccessToken == "" {
		return "", 0, errors.New("empty access_token received from service account exchange")
	}
	expiresIn := tokenResp.ExpiresIn
	if expiresIn <= 0 {
		expiresIn = 3600
	}
	return tokenResp.AccessToken, expiresIn, nil
}

// createServiceAccountJWT creates and signs a JWT for Google OAuth2 using RS256.
func createServiceAccountJWT(clientEmail, privateKeyPEM, audience string) (string, error) {
	block, _ := pem.Decode([]byte(privateKeyPEM))
	if block == nil {
		return "", errors.New("failed to decode private key PEM")
	}

	var parsedKey any
	var parseErr error
	if key, err := x509.ParsePKCS8PrivateKey(block.Bytes); err == nil {
		parsedKey = key
	} else if key, err := x509.ParsePKCS1PrivateKey(block.Bytes); err == nil {
		parsedKey = key
	} else {
		parseErr = err
	}

	rsaKey, ok := parsedKey.(*rsa.PrivateKey)
	if !ok || parseErr != nil {
		return "", fmt.Errorf("invalid RSA private key in service account: %w", parseErr)
	}

	now := time.Now().Unix()
	headerJSON := `{"alg":"RS256","typ":"JWT"}`
	headerEnc := base64.RawURLEncoding.EncodeToString([]byte(headerJSON))

	payload := map[string]any{
		"iss":   clientEmail,
		"sub":   clientEmail,
		"aud":   audience,
		"iat":   now,
		"exp":   now + 3600,
		"scope": "https://www.googleapis.com/auth/cloud-platform",
	}
	payloadBytes, err := json.Marshal(payload)
	if err != nil {
		return "", err
	}
	payloadEnc := base64.RawURLEncoding.EncodeToString(payloadBytes)

	signingInput := headerEnc + "." + payloadEnc

	h := sha256.Sum256([]byte(signingInput))
	sig, err := rsa.SignPKCS1v15(nil, rsaKey, crypto.SHA256, h[:])
	if err != nil {
		return "", fmt.Errorf("failed to sign JWT: %w", err)
	}

	sigEnc := base64.RawURLEncoding.EncodeToString(sig)
	return signingInput + "." + sigEnc, nil
}
