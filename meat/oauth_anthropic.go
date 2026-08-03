package meat

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os/exec"
	"runtime"
	"strings"
	"time"
)

// Anthropic Claude Pro/Max OAuth (Claude Code client id + scopes).
// Client id is stored base64-obfuscated (same approach as shuvpi / Claude Code).
const (
	anthropicOAuthClientIDB64 = "OWQxYzI1MGEtZTYxYi00NGQ5LTg4ZWQtNTk0NGQxOTYyZjVl"
	anthropicAuthorizeURL     = "https://claude.ai/oauth/authorize"
	anthropicTokenURL         = "https://platform.claude.com/v1/oauth/token"
	anthropicCallbackPort     = 53692
	anthropicCallbackPath     = "/callback"
	anthropicOAuthScopes      = "org:create_api_key user:profile user:inference user:sessions:claude_code user:mcp_servers user:file_upload"
	claudeCodeSystemIdentity  = "You are Claude Code, Anthropic's official CLI for Claude."
	claudeCodeVersion         = "2.1.207"
	anthropicOAuthBetas       = "claude-code-20250219,oauth-2025-04-20"
)

func anthropicOAuthClientID() string {
	raw, err := base64.StdEncoding.DecodeString(anthropicOAuthClientIDB64)
	if err != nil {
		return ""
	}
	return string(raw)
}

func anthropicRedirectURI() string {
	return fmt.Sprintf("http://localhost:%d%s", anthropicCallbackPort, anthropicCallbackPath)
}

// LoginAnthropicOAuth runs the Claude Pro/Max browser PKCE login and stores tokens.
func LoginAnthropicOAuth(ctx context.Context) (OAuthCredential, error) {
	verifier, challenge, err := generatePKCE()
	if err != nil {
		return OAuthCredential{}, err
	}
	// Anthropic uses the PKCE verifier as OAuth state (Claude Code contract).
	state := verifier
	clientID := anthropicOAuthClientID()
	redirectURI := anthropicRedirectURI()

	params := url.Values{}
	params.Set("code", "true")
	params.Set("client_id", clientID)
	params.Set("response_type", "code")
	params.Set("redirect_uri", redirectURI)
	params.Set("scope", anthropicOAuthScopes)
	params.Set("code_challenge", challenge)
	params.Set("code_challenge_method", "S256")
	params.Set("state", state)
	authURL := anthropicAuthorizeURL + "?" + params.Encode()

	manual := make(chan oauthCallbackResult, 1)
	go promptManualOAuthCode(ctx, redirectURI, state, manual)

	fmt.Fprintln(osStderr, "Opening browser for Anthropic (Claude Pro/Max) login…")
	fmt.Fprintln(osStderr, authURL)
	_ = openBrowser(authURL)

	result, err := runOAuthCallbackServer(ctx, oauthCallbackHost(), anthropicCallbackPort, anthropicCallbackPath, state, manual)
	if err != nil {
		return OAuthCredential{}, err
	}
	if result.State != "" && result.State != state {
		return OAuthCredential{}, fmt.Errorf("oauth state mismatch")
	}
	fmt.Fprintln(osStderr, "Exchanging authorization code…")
	cred, err := exchangeAnthropicCode(ctx, result.Code, result.State, verifier, redirectURI)
	if err != nil {
		return OAuthCredential{}, err
	}
	if err := SaveOAuthCredential(OAuthProviderAnthropic, cred); err != nil {
		return OAuthCredential{}, err
	}
	return cred, nil
}

func exchangeAnthropicCode(ctx context.Context, code, state, verifier, redirectURI string) (OAuthCredential, error) {
	body := map[string]string{
		"grant_type":    "authorization_code",
		"client_id":     anthropicOAuthClientID(),
		"code":          code,
		"state":         state,
		"redirect_uri":  redirectURI,
		"code_verifier": verifier,
	}
	return postAnthropicToken(ctx, body)
}

func refreshAnthropicOAuth(refreshToken string) (OAuthCredential, error) {
	body := map[string]string{
		"grant_type":    "refresh_token",
		"client_id":     anthropicOAuthClientID(),
		"refresh_token": refreshToken,
	}
	return postAnthropicToken(context.Background(), body)
}

func postAnthropicToken(ctx context.Context, body map[string]string) (OAuthCredential, error) {
	raw, err := json.Marshal(body)
	if err != nil {
		return OAuthCredential{}, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, anthropicTokenURL, bytes.NewReader(raw))
	if err != nil {
		return OAuthCredential{}, err
	}
	req.Header.Set("content-type", "application/json")
	req.Header.Set("accept", "application/json")
	client := &http.Client{Timeout: 30 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return OAuthCredential{}, err
	}
	defer resp.Body.Close()
	respBody, err := io.ReadAll(resp.Body)
	if err != nil {
		return OAuthCredential{}, err
	}
	if resp.StatusCode != http.StatusOK {
		return OAuthCredential{}, fmt.Errorf("anthropic token endpoint %d: %s", resp.StatusCode, truncateErrBody(respBody))
	}
	var data struct {
		AccessToken  string `json:"access_token"`
		RefreshToken string `json:"refresh_token"`
		ExpiresIn    int64  `json:"expires_in"`
	}
	if err := json.Unmarshal(respBody, &data); err != nil {
		return OAuthCredential{}, fmt.Errorf("decode anthropic token response: %w", err)
	}
	if data.AccessToken == "" || data.RefreshToken == "" || data.ExpiresIn <= 0 {
		return OAuthCredential{}, fmt.Errorf("anthropic token response missing fields")
	}
	// 5-minute skew so we refresh before the token is actually dead.
	expires := time.Now().UnixMilli() + data.ExpiresIn*1000 - int64(5*time.Minute/time.Millisecond)
	return OAuthCredential{
		Type:    "oauth",
		Access:  data.AccessToken,
		Refresh: data.RefreshToken,
		Expires: expires,
	}, nil
}

// ResolveAnthropicOAuth returns a fresh access token for Claude subscription auth.
func ResolveAnthropicOAuth(force bool) (OAuthCredential, error) {
	return refreshOAuth(OAuthProviderAnthropic, force, refreshAnthropicOAuth)
}

func isAnthropicOAuthToken(token string) bool {
	return strings.Contains(token, "sk-ant-oat")
}

// openBrowser tries to open url in the default browser. Failures are ignored;
// the URL is always printed for manual open.
func openBrowser(rawURL string) error {
	var cmd *exec.Cmd
	switch runtime.GOOS {
	case "linux":
		cmd = exec.Command("xdg-open", rawURL)
	case "darwin":
		cmd = exec.Command("open", rawURL)
	case "windows":
		cmd = exec.Command("rundll32", "url.dll,FileProtocolHandler", rawURL)
	default:
		return fmt.Errorf("unsupported OS for browser open")
	}
	return cmd.Start()
}
