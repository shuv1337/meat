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
	"strings"
	"time"
)

// OpenAI Codex / ChatGPT Plus-Pro OAuth (Codex CLI client id).
const (
	openaiCodexClientID           = "app_EMoamEEZ73f0CkXaXp7hrann"
	openaiAuthBaseURL             = "https://auth.openai.com"
	openaiAuthorizeURL            = openaiAuthBaseURL + "/oauth/authorize"
	openaiTokenURL                = openaiAuthBaseURL + "/oauth/token"
	openaiCodexRedirectURI        = "http://localhost:1455/auth/callback"
	openaiCodexCallbackPort       = 1455
	openaiCodexCallbackPath       = "/auth/callback"
	openaiDeviceUserCodeURL       = openaiAuthBaseURL + "/api/accounts/deviceauth/usercode"
	openaiDeviceTokenURL          = openaiAuthBaseURL + "/api/accounts/deviceauth/token"
	openaiDeviceVerificationURI   = openaiAuthBaseURL + "/codex/device"
	openaiDeviceRedirectURI       = openaiAuthBaseURL + "/deviceauth/callback"
	openaiDeviceCodeTimeoutSec    = 15 * 60
	openaiCodexScope              = "openid profile email offline_access"
	openaiJWTClaimPath            = "https://api.openai.com/auth"
	openaiCodexBaseURL            = "https://chatgpt.com/backend-api"
	openaiCodexOriginator         = "meat"
	DefaultOpenAICodexModel       = "gpt-5.4"
)

// LoginOpenAICodexOAuth runs ChatGPT subscription login (browser PKCE by default).
// method is "browser" or "device".
func LoginOpenAICodexOAuth(ctx context.Context, method string) (OAuthCredential, error) {
	method = strings.TrimSpace(strings.ToLower(method))
	if method == "" || method == "browser" {
		return loginOpenAICodexBrowser(ctx)
	}
	if method == "device" || method == "device_code" {
		return loginOpenAICodexDevice(ctx)
	}
	return OAuthCredential{}, fmt.Errorf("unknown openai login method %q (want browser or device)", method)
}

func loginOpenAICodexBrowser(ctx context.Context) (OAuthCredential, error) {
	verifier, challenge, err := generatePKCE()
	if err != nil {
		return OAuthCredential{}, err
	}
	state, err := randomHex(16)
	if err != nil {
		return OAuthCredential{}, err
	}

	params := url.Values{}
	params.Set("response_type", "code")
	params.Set("client_id", openaiCodexClientID)
	params.Set("redirect_uri", openaiCodexRedirectURI)
	params.Set("scope", openaiCodexScope)
	params.Set("code_challenge", challenge)
	params.Set("code_challenge_method", "S256")
	params.Set("state", state)
	params.Set("id_token_add_organizations", "true")
	params.Set("codex_cli_simplified_flow", "true")
	params.Set("originator", openaiCodexOriginator)
	authURL := openaiAuthorizeURL + "?" + params.Encode()

	manual := make(chan oauthCallbackResult, 1)
	go promptManualOAuthCode(ctx, openaiCodexRedirectURI, state, manual)

	fmt.Fprintln(osStderr, "Opening browser for OpenAI (ChatGPT Plus/Pro) login…")
	fmt.Fprintln(osStderr, authURL)
	_ = openBrowser(authURL)

	result, err := runOAuthCallbackServer(ctx, oauthCallbackHost(), openaiCodexCallbackPort, openaiCodexCallbackPath, state, manual)
	if err != nil {
		return OAuthCredential{}, err
	}
	fmt.Fprintln(osStderr, "Exchanging authorization code…")
	token, err := exchangeOpenAICodexCode(ctx, result.Code, verifier, openaiCodexRedirectURI)
	if err != nil {
		return OAuthCredential{}, err
	}
	cred, err := openaiCodexCredentialFromToken(token)
	if err != nil {
		return OAuthCredential{}, err
	}
	if err := SaveOAuthCredential(OAuthProviderOpenAICodex, cred); err != nil {
		return OAuthCredential{}, err
	}
	return cred, nil
}

func loginOpenAICodexDevice(ctx context.Context) (OAuthCredential, error) {
	device, err := startOpenAICodexDeviceAuth(ctx)
	if err != nil {
		return OAuthCredential{}, err
	}
	fmt.Fprintf(osStderr, "Open %s and enter code: %s\n", openaiDeviceVerificationURI, device.UserCode)
	_ = openBrowser(openaiDeviceVerificationURI)

	code, err := pollOpenAICodexDeviceAuth(ctx, device)
	if err != nil {
		return OAuthCredential{}, err
	}
	token, err := exchangeOpenAICodexCode(ctx, code.AuthorizationCode, code.CodeVerifier, openaiDeviceRedirectURI)
	if err != nil {
		return OAuthCredential{}, err
	}
	cred, err := openaiCodexCredentialFromToken(token)
	if err != nil {
		return OAuthCredential{}, err
	}
	if err := SaveOAuthCredential(OAuthProviderOpenAICodex, cred); err != nil {
		return OAuthCredential{}, err
	}
	return cred, nil
}

type openaiCodexToken struct {
	Access  string
	Refresh string
	Expires int64 // unix ms
}

func exchangeOpenAICodexCode(ctx context.Context, code, verifier, redirectURI string) (openaiCodexToken, error) {
	form := url.Values{}
	form.Set("grant_type", "authorization_code")
	form.Set("client_id", openaiCodexClientID)
	form.Set("code", code)
	form.Set("code_verifier", verifier)
	form.Set("redirect_uri", redirectURI)
	return postOpenAICodexToken(ctx, form, "exchange")
}

func refreshOpenAICodexOAuth(refreshToken string) (OAuthCredential, error) {
	form := url.Values{}
	form.Set("grant_type", "refresh_token")
	form.Set("refresh_token", refreshToken)
	form.Set("client_id", openaiCodexClientID)
	token, err := postOpenAICodexToken(context.Background(), form, "refresh")
	if err != nil {
		return OAuthCredential{}, err
	}
	return openaiCodexCredentialFromToken(token)
}

func postOpenAICodexToken(ctx context.Context, form url.Values, op string) (openaiCodexToken, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, openaiTokenURL, strings.NewReader(form.Encode()))
	if err != nil {
		return openaiCodexToken{}, err
	}
	req.Header.Set("content-type", "application/x-www-form-urlencoded")
	client := &http.Client{Timeout: 30 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return openaiCodexToken{}, err
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return openaiCodexToken{}, err
	}
	if resp.StatusCode != http.StatusOK {
		return openaiCodexToken{}, fmt.Errorf("openai codex token %s %d: %s", op, resp.StatusCode, truncateErrBody(body))
	}
	var data struct {
		AccessToken  string `json:"access_token"`
		RefreshToken string `json:"refresh_token"`
		ExpiresIn    int64  `json:"expires_in"`
	}
	if err := json.Unmarshal(body, &data); err != nil {
		return openaiCodexToken{}, fmt.Errorf("decode openai codex token %s: %w", op, err)
	}
	if data.AccessToken == "" || data.RefreshToken == "" || data.ExpiresIn <= 0 {
		return openaiCodexToken{}, fmt.Errorf("openai codex token %s missing fields", op)
	}
	return openaiCodexToken{
		Access:  data.AccessToken,
		Refresh: data.RefreshToken,
		Expires: time.Now().UnixMilli() + data.ExpiresIn*1000,
	}, nil
}

func openaiCodexCredentialFromToken(token openaiCodexToken) (OAuthCredential, error) {
	accountID := chatgptAccountIDFromJWT(token.Access)
	if accountID == "" {
		return OAuthCredential{}, fmt.Errorf("failed to extract chatgpt_account_id from access token")
	}
	return OAuthCredential{
		Type:      "oauth",
		Access:    token.Access,
		Refresh:   token.Refresh,
		Expires:   token.Expires,
		AccountID: accountID,
	}, nil
}

func chatgptAccountIDFromJWT(access string) string {
	parts := strings.Split(access, ".")
	if len(parts) != 3 {
		return ""
	}
	payload, err := base64.RawURLEncoding.DecodeString(parts[1])
	if err != nil {
		// Some tokens use standard base64 with padding.
		payload, err = base64.URLEncoding.DecodeString(parts[1])
		if err != nil {
			return ""
		}
	}
	var claims map[string]any
	if err := json.Unmarshal(payload, &claims); err != nil {
		return ""
	}
	auth, _ := claims[openaiJWTClaimPath].(map[string]any)
	if auth == nil {
		return ""
	}
	id, _ := auth["chatgpt_account_id"].(string)
	return id
}

type openaiDeviceAuth struct {
	DeviceAuthID   string
	UserCode       string
	IntervalSeconds float64
}

type openaiDeviceCodeResult struct {
	AuthorizationCode string
	CodeVerifier      string
}

func startOpenAICodexDeviceAuth(ctx context.Context) (openaiDeviceAuth, error) {
	raw, _ := json.Marshal(map[string]string{"client_id": openaiCodexClientID})
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, openaiDeviceUserCodeURL, bytes.NewReader(raw))
	if err != nil {
		return openaiDeviceAuth{}, err
	}
	req.Header.Set("content-type", "application/json")
	client := &http.Client{Timeout: 30 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return openaiDeviceAuth{}, err
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return openaiDeviceAuth{}, err
	}
	if resp.StatusCode == http.StatusNotFound {
		return openaiDeviceAuth{}, fmt.Errorf("openai codex device code login is not enabled; use browser login")
	}
	if resp.StatusCode != http.StatusOK {
		return openaiDeviceAuth{}, fmt.Errorf("openai device usercode %d: %s", resp.StatusCode, truncateErrBody(body))
	}
	var data struct {
		DeviceAuthID string  `json:"device_auth_id"`
		UserCode     string  `json:"user_code"`
		Interval     float64 `json:"interval"`
	}
	if err := json.Unmarshal(body, &data); err != nil {
		return openaiDeviceAuth{}, err
	}
	if data.DeviceAuthID == "" || data.UserCode == "" {
		return openaiDeviceAuth{}, fmt.Errorf("invalid openai device usercode response")
	}
	if data.Interval <= 0 {
		data.Interval = 5
	}
	return openaiDeviceAuth{
		DeviceAuthID:    data.DeviceAuthID,
		UserCode:        data.UserCode,
		IntervalSeconds: data.Interval,
	}, nil
}

func pollOpenAICodexDeviceAuth(ctx context.Context, device openaiDeviceAuth) (openaiDeviceCodeResult, error) {
	deadline := time.Now().Add(time.Duration(openaiDeviceCodeTimeoutSec) * time.Second)
	interval := time.Duration(device.IntervalSeconds * float64(time.Second))
	if interval < time.Second {
		interval = 5 * time.Second
	}
	client := &http.Client{Timeout: 30 * time.Second}
	for {
		if time.Now().After(deadline) {
			return openaiDeviceCodeResult{}, fmt.Errorf("openai device code login timed out")
		}
		select {
		case <-ctx.Done():
			return openaiDeviceCodeResult{}, ctx.Err()
		default:
		}

		raw, _ := json.Marshal(map[string]string{
			"device_auth_id": device.DeviceAuthID,
			"user_code":      device.UserCode,
		})
		req, err := http.NewRequestWithContext(ctx, http.MethodPost, openaiDeviceTokenURL, bytes.NewReader(raw))
		if err != nil {
			return openaiDeviceCodeResult{}, err
		}
		req.Header.Set("content-type", "application/json")
		resp, err := client.Do(req)
		if err != nil {
			return openaiDeviceCodeResult{}, err
		}
		body, _ := io.ReadAll(resp.Body)
		resp.Body.Close()

		if resp.StatusCode == http.StatusOK {
			var data struct {
				AuthorizationCode string `json:"authorization_code"`
				CodeVerifier      string `json:"code_verifier"`
			}
			if err := json.Unmarshal(body, &data); err != nil {
				return openaiDeviceCodeResult{}, err
			}
			if data.AuthorizationCode == "" || data.CodeVerifier == "" {
				return openaiDeviceCodeResult{}, fmt.Errorf("invalid openai device token response")
			}
			return openaiDeviceCodeResult{
				AuthorizationCode: data.AuthorizationCode,
				CodeVerifier:      data.CodeVerifier,
			}, nil
		}
		if resp.StatusCode == http.StatusForbidden || resp.StatusCode == http.StatusNotFound {
			// pending
		} else {
			var errBody struct {
				Error any `json:"error"`
			}
			_ = json.Unmarshal(body, &errBody)
			code := ""
			switch v := errBody.Error.(type) {
			case string:
				code = v
			case map[string]any:
				if c, ok := v["code"].(string); ok {
					code = c
				}
			}
			switch code {
			case "deviceauth_authorization_pending":
				// pending
			case "slow_down":
				interval += 5 * time.Second
			default:
				return openaiDeviceCodeResult{}, fmt.Errorf("openai device auth %d: %s", resp.StatusCode, truncateErrBody(body))
			}
		}

		timer := time.NewTimer(interval)
		select {
		case <-ctx.Done():
			timer.Stop()
			return openaiDeviceCodeResult{}, ctx.Err()
		case <-timer.C:
		}
	}
}

// ResolveOpenAICodexOAuth returns a fresh Codex subscription credential.
func ResolveOpenAICodexOAuth(force bool) (OAuthCredential, error) {
	return refreshOAuth(OAuthProviderOpenAICodex, force, refreshOpenAICodexOAuth)
}

func truncateErrBody(body []byte) string {
	s := strings.TrimSpace(string(body))
	if len(s) > 400 {
		return s[:400] + "…"
	}
	return s
}

func openaiCodexResponsesURL(base string) string {
	base = strings.TrimRight(base, "/")
	if strings.HasSuffix(base, "/codex/responses") {
		return base
	}
	return base + "/codex/responses"
}
