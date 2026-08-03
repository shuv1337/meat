package meat

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func withAuthFile(t *testing.T) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "auth.json")
	t.Setenv("MEAT_AUTH_FILE", path)
	return path
}

func TestOAuthStoreRoundTrip(t *testing.T) {
	withAuthFile(t)
	cred := OAuthCredential{
		Type:      "oauth",
		Access:    "access-1",
		Refresh:   "refresh-1",
		Expires:   time.Now().Add(time.Hour).UnixMilli(),
		AccountID: "acct_1",
	}
	if err := SaveOAuthCredential(OAuthProviderOpenAICodex, cred); err != nil {
		t.Fatal(err)
	}
	got, ok, err := LoadOAuthCredential(OAuthProviderOpenAICodex)
	if err != nil || !ok {
		t.Fatalf("load: ok=%v err=%v", ok, err)
	}
	if got.Access != "access-1" || got.AccountID != "acct_1" {
		t.Fatalf("got %+v", got)
	}
	if err := DeleteOAuthCredential(OAuthProviderOpenAICodex); err != nil {
		t.Fatal(err)
	}
	if _, ok, err := LoadOAuthCredential(OAuthProviderOpenAICodex); err != nil || ok {
		t.Fatalf("want deleted, ok=%v err=%v", ok, err)
	}
}

func TestGeneratePKCE(t *testing.T) {
	v, c, err := generatePKCE()
	if err != nil {
		t.Fatal(err)
	}
	if len(v) < 40 || c == "" {
		t.Fatalf("verifier/challenge too short: %q %q", v, c)
	}
	if strings.ContainsAny(v, "+/=") || strings.ContainsAny(c, "+/=") {
		t.Fatalf("want base64url without padding: %q %q", v, c)
	}
}

func TestParseAuthorizationInput(t *testing.T) {
	cases := []struct {
		in, state string
		code      string
	}{
		{"abc123", "st", "abc123"},
		{"code#state", "st", "code"},
		{"http://localhost:1455/auth/callback?code=xy&state=st", "st", "xy"},
		{"code=zz&state=st", "st", "zz"},
	}
	for _, tc := range cases {
		got, err := parseAuthorizationInput(tc.in, tc.state)
		if err != nil {
			t.Fatalf("%q: %v", tc.in, err)
		}
		if got.Code != tc.code {
			t.Fatalf("%q code=%q want %q", tc.in, got.Code, tc.code)
		}
	}
}

func TestChatgptAccountIDFromJWT(t *testing.T) {
	payload := map[string]any{
		openaiJWTClaimPath: map[string]any{"chatgpt_account_id": "acct_xyz"},
	}
	raw, _ := json.Marshal(payload)
	seg := base64.RawURLEncoding.EncodeToString(raw)
	tok := "hdr." + seg + ".sig"
	if got := chatgptAccountIDFromJWT(tok); got != "acct_xyz" {
		t.Fatalf("account id = %q", got)
	}
}

func TestNewOpenAIFromEnv_UsesOAuth(t *testing.T) {
	withAuthFile(t)
	t.Setenv("OPENAI_API_KEY", "")
	t.Setenv("OPENAI_BASE_URL", "")
	t.Setenv("MEAT_MODEL", "")
	old := exeDevMarkerPath
	exeDevMarkerPath = filepath.Join(t.TempDir(), "nope")
	t.Cleanup(func() { exeDevMarkerPath = old })

	payload := map[string]any{
		openaiJWTClaimPath: map[string]any{"chatgpt_account_id": "acct_oauth"},
	}
	raw, _ := json.Marshal(payload)
	access := "hdr." + base64.RawURLEncoding.EncodeToString(raw) + ".sig"
	if err := SaveOAuthCredential(OAuthProviderOpenAICodex, OAuthCredential{
		Type: "oauth", Access: access, Refresh: "r", Expires: time.Now().Add(time.Hour).UnixMilli(), AccountID: "acct_oauth",
	}); err != nil {
		t.Fatal(err)
	}

	m, err := NewOpenAIFromEnv(context.Background(), "")
	if err != nil {
		t.Fatal(err)
	}
	if !m.Codex || m.AccountID != "acct_oauth" || m.BaseURL != openaiCodexBaseURL {
		t.Fatalf("model = %+v", m)
	}
	if m.Model != DefaultOpenAICodexModel {
		t.Fatalf("model id = %q", m.Model)
	}
}

func TestNewAnthropicFromEnv_UsesOAuth(t *testing.T) {
	withAuthFile(t)
	t.Setenv("ANTHROPIC_API_KEY", "")
	t.Setenv("ANTHROPIC_BASE_URL", "")
	old := exeDevMarkerPath
	exeDevMarkerPath = filepath.Join(t.TempDir(), "nope")
	t.Cleanup(func() { exeDevMarkerPath = old })

	if err := SaveOAuthCredential(OAuthProviderAnthropic, OAuthCredential{
		Type: "oauth", Access: "sk-ant-oat-test", Refresh: "r", Expires: time.Now().Add(time.Hour).UnixMilli(),
	}); err != nil {
		t.Fatal(err)
	}

	m, err := NewAnthropicFromEnv(context.Background(), "claude-sonnet-4-6")
	if err != nil {
		t.Fatal(err)
	}
	if !m.OAuth || m.APIKey != "sk-ant-oat-test" {
		t.Fatalf("model = %+v", m)
	}
}

func TestAnthropicOAuthRequestHeadersAndSystem(t *testing.T) {
	withAuthFile(t)
	var sawAuth, sawBeta, sawUA, sawXApp bool
	var body map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/messages" || r.URL.Query().Get("beta") != "true" {
			t.Errorf("path/query = %s?%s", r.URL.Path, r.URL.RawQuery)
		}
		if r.Header.Get("x-api-key") != "" {
			t.Errorf("x-api-key should be absent for oauth")
		}
		if strings.HasPrefix(r.Header.Get("authorization"), "Bearer sk-ant-oat") {
			sawAuth = true
		}
		if r.Header.Get("anthropic-beta") == anthropicOAuthBetas {
			sawBeta = true
		}
		if strings.Contains(r.Header.Get("user-agent"), "claude-cli/") {
			sawUA = true
		}
		if r.Header.Get("x-app") == "cli" {
			sawXApp = true
		}
		_ = json.NewDecoder(r.Body).Decode(&body)
		w.Header().Set("content-type", "application/json")
		_, _ = w.Write([]byte(`{"content":[{"type":"text","text":"ok"}],"stop_reason":"end_turn","usage":{"input_tokens":1,"output_tokens":1}}`))
	}))
	defer srv.Close()

	m := &AnthropicModel{
		APIKey:  "sk-ant-oat-test",
		Model:   "claude-sonnet-4-6",
		BaseURL: srv.URL,
		OAuth:   true,
	}
	_, err := m.Generate(context.Background(), "meat system", []Message{{Role: RoleUser, Content: []Block{{Type: "text", Text: "hi"}}}}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if !sawAuth || !sawBeta || !sawUA || !sawXApp {
		t.Fatalf("headers auth=%v beta=%v ua=%v xapp=%v", sawAuth, sawBeta, sawUA, sawXApp)
	}
	sys, ok := body["system"].([]any)
	if !ok || len(sys) != 2 {
		t.Fatalf("system = %#v", body["system"])
	}
	first, _ := sys[0].(map[string]any)
	if first["text"] != claudeCodeSystemIdentity {
		t.Fatalf("first system block = %#v", first)
	}
}

func TestOpenAICodexRequestPathAndHeaders(t *testing.T) {
	withAuthFile(t)
	payload := map[string]any{
		openaiJWTClaimPath: map[string]any{"chatgpt_account_id": "acct_req"},
	}
	raw, _ := json.Marshal(payload)
	access := "hdr." + base64.RawURLEncoding.EncodeToString(raw) + ".sig"
	if err := SaveOAuthCredential(OAuthProviderOpenAICodex, OAuthCredential{
		Type: "oauth", Access: access, Refresh: "r", Expires: time.Now().Add(time.Hour).UnixMilli(), AccountID: "acct_req",
	}); err != nil {
		t.Fatal(err)
	}

	var sawPath, sawAccount, sawOriginator, sawBeta bool
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/codex/responses" {
			sawPath = true
		}
		if r.Header.Get("chatgpt-account-id") == "acct_req" {
			sawAccount = true
		}
		if r.Header.Get("originator") == openaiCodexOriginator {
			sawOriginator = true
		}
		if r.Header.Get("OpenAI-Beta") == "responses=experimental" {
			sawBeta = true
		}
		if got := r.Header.Get("authorization"); !strings.HasPrefix(got, "Bearer ") {
			t.Errorf("authorization = %q", got)
		}
		w.Header().Set("content-type", "text/event-stream")
		writeOpenAIEvent(w, outputItemDone(0, map[string]any{
			"type": "message",
			"id":   "m1",
			"content": []any{
				map[string]any{"type": "output_text", "text": "hi"},
			},
		}))
		writeOpenAIEvent(w, openAICompleted("resp", 1, 1))
	}))
	defer srv.Close()

	m := &OpenAIModel{
		APIKey:    access,
		AccountID: "acct_req",
		Model:     DefaultOpenAICodexModel,
		BaseURL:   srv.URL,
		Codex:     true,
	}
	_, err := m.Generate(context.Background(), "sys", []Message{{Role: RoleUser, Content: []Block{{Type: "text", Text: "hi"}}}}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if !sawPath || !sawAccount || !sawOriginator || !sawBeta {
		t.Fatalf("path=%v account=%v originator=%v beta=%v", sawPath, sawAccount, sawOriginator, sawBeta)
	}
}

func TestAuthFilePermissions(t *testing.T) {
	path := withAuthFile(t)
	if err := SaveOAuthCredential(OAuthProviderAnthropic, OAuthCredential{
		Type: "oauth", Access: "a", Refresh: "r", Expires: time.Now().Add(time.Hour).UnixMilli(),
	}); err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if perm := info.Mode().Perm(); perm != 0o600 {
		t.Fatalf("auth.json mode = %o, want 0600", perm)
	}
}

func TestOAuthNeedsRefresh(t *testing.T) {
	now := time.Now()
	if !oauthNeedsRefresh(OAuthCredential{Expires: now.Add(time.Minute).UnixMilli()}, now) {
		t.Fatal("want refresh when under 5m remaining")
	}
	if oauthNeedsRefresh(OAuthCredential{Expires: now.Add(time.Hour).UnixMilli()}, now) {
		t.Fatal("do not refresh when >5m remaining")
	}
}

func TestRefreshOAuthPersists(t *testing.T) {
	withAuthFile(t)
	if err := SaveOAuthCredential(OAuthProviderAnthropic, OAuthCredential{
		Type: "oauth", Access: "old", Refresh: "refresh-tok", Expires: time.Now().Add(time.Minute).UnixMilli(),
	}); err != nil {
		t.Fatal(err)
	}
	got, err := refreshOAuth(OAuthProviderAnthropic, true, func(refresh string) (OAuthCredential, error) {
		if refresh != "refresh-tok" {
			t.Fatalf("refresh = %q", refresh)
		}
		return OAuthCredential{
			Type: "oauth", Access: "new", Refresh: "refresh-2", Expires: time.Now().Add(time.Hour).UnixMilli(),
		}, nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if got.Access != "new" {
		t.Fatalf("got %+v", got)
	}
	loaded, ok, err := LoadOAuthCredential(OAuthProviderAnthropic)
	if err != nil || !ok || loaded.Access != "new" || loaded.Refresh != "refresh-2" {
		t.Fatalf("loaded %+v ok=%v err=%v", loaded, ok, err)
	}
}

func TestExplicitKeyBeatsOAuth(t *testing.T) {
	withAuthFile(t)
	t.Setenv("ANTHROPIC_API_KEY", "sk-explicit")
	t.Setenv("ANTHROPIC_BASE_URL", "")
	if err := SaveOAuthCredential(OAuthProviderAnthropic, OAuthCredential{
		Type: "oauth", Access: "sk-ant-oat-x", Refresh: "r", Expires: time.Now().Add(time.Hour).UnixMilli(),
	}); err != nil {
		t.Fatal(err)
	}
	m, err := NewAnthropicFromEnv(context.Background(), "")
	if err != nil {
		t.Fatal(err)
	}
	if m.APIKey != "sk-explicit" || m.OAuth {
		t.Fatalf("want explicit key, got %+v", m)
	}
}
