package meat

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"time"
)

// OAuth provider ids stored in auth.json.
const (
	OAuthProviderAnthropic   = "anthropic"
	OAuthProviderOpenAICodex = "openai-codex"
)

// OAuthCredential is a stored subscription OAuth token set.
type OAuthCredential struct {
	Type      string `json:"type"` // "oauth"
	Access    string `json:"access"`
	Refresh   string `json:"refresh"`
	Expires   int64  `json:"expires"` // unix ms
	AccountID string `json:"accountId,omitempty"`
}

// minOAuthValidity is how long an access token must remain valid to be used
// without a proactive refresh (matches shuvpi's 5-minute floor).
const minOAuthValidity = 5 * time.Minute

// authFilePath is the path to the credential store. Overridable via MEAT_AUTH_FILE
// (tests) or defaults to ~/.meat/auth.json.
func authFilePath() string {
	if v, ok := os.LookupEnv("MEAT_AUTH_FILE"); ok {
		return v
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return ""
	}
	return filepath.Join(home, ".meat", "auth.json")
}

var authMu sync.Mutex

type authFile map[string]OAuthCredential

func readAuthFile(path string) (authFile, error) {
	if path == "" {
		return nil, fmt.Errorf("oauth store path is empty")
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return authFile{}, nil
		}
		return nil, err
	}
	if len(raw) == 0 {
		return authFile{}, nil
	}
	var file authFile
	if err := json.Unmarshal(raw, &file); err != nil {
		return nil, fmt.Errorf("parse oauth store: %w", err)
	}
	if file == nil {
		file = authFile{}
	}
	return file, nil
}

func writeAuthFile(path string, file authFile) error {
	if path == "" {
		return fmt.Errorf("oauth store path is empty")
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	raw, err := json.MarshalIndent(file, "", "  ")
	if err != nil {
		return err
	}
	raw = append(raw, '\n')
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, raw, 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}

// LoadOAuthCredential returns the stored credential for provider, or false.
func LoadOAuthCredential(provider string) (OAuthCredential, bool, error) {
	authMu.Lock()
	defer authMu.Unlock()
	file, err := readAuthFile(authFilePath())
	if err != nil {
		return OAuthCredential{}, false, err
	}
	cred, ok := file[provider]
	if !ok || cred.Access == "" {
		return OAuthCredential{}, false, nil
	}
	return cred, true, nil
}

// SaveOAuthCredential writes credential for provider.
func SaveOAuthCredential(provider string, cred OAuthCredential) error {
	if cred.Type == "" {
		cred.Type = "oauth"
	}
	authMu.Lock()
	defer authMu.Unlock()
	path := authFilePath()
	file, err := readAuthFile(path)
	if err != nil {
		return err
	}
	file[provider] = cred
	return writeAuthFile(path, file)
}

// DeleteOAuthCredential removes provider from the store. No-op if absent.
func DeleteOAuthCredential(provider string) error {
	authMu.Lock()
	defer authMu.Unlock()
	path := authFilePath()
	file, err := readAuthFile(path)
	if err != nil {
		return err
	}
	if _, ok := file[provider]; !ok {
		return nil
	}
	delete(file, provider)
	return writeAuthFile(path, file)
}

// ListOAuthCredentials returns a copy of the store keyed by provider id.
func ListOAuthCredentials() (map[string]OAuthCredential, error) {
	authMu.Lock()
	defer authMu.Unlock()
	file, err := readAuthFile(authFilePath())
	if err != nil {
		return nil, err
	}
	out := make(map[string]OAuthCredential, len(file))
	for k, v := range file {
		out[k] = v
	}
	return out, nil
}

// oauthNeedsRefresh reports whether the credential should be refreshed before use.
func oauthNeedsRefresh(cred OAuthCredential, now time.Time) bool {
	if cred.Expires <= 0 {
		return true
	}
	return now.Add(minOAuthValidity).UnixMilli() >= cred.Expires
}

// refreshOAuthLocked loads provider, refreshes if needed (or always if force),
// persists, and returns a usable credential. Caller must NOT hold authMu.
func refreshOAuth(provider string, force bool, refresher func(refreshToken string) (OAuthCredential, error)) (OAuthCredential, error) {
	authMu.Lock()
	defer authMu.Unlock()

	path := authFilePath()
	file, err := readAuthFile(path)
	if err != nil {
		return OAuthCredential{}, err
	}
	cred, ok := file[provider]
	if !ok || cred.Refresh == "" {
		return OAuthCredential{}, fmt.Errorf("no oauth credential for %s; run: meat login %s", providerLabel(provider), providerLabel(provider))
	}
	if !force && !oauthNeedsRefresh(cred, time.Now()) {
		return cred, nil
	}
	next, err := refresher(cred.Refresh)
	if err != nil {
		return OAuthCredential{}, fmt.Errorf("refresh %s oauth token: %w", provider, err)
	}
	if next.Type == "" {
		next.Type = "oauth"
	}
	// Preserve account id if refresher omitted it.
	if next.AccountID == "" {
		next.AccountID = cred.AccountID
	}
	file[provider] = next
	if err := writeAuthFile(path, file); err != nil {
		return OAuthCredential{}, err
	}
	return next, nil
}

func providerLabel(provider string) string {
	switch provider {
	case OAuthProviderOpenAICodex:
		return "openai"
	default:
		return provider
	}
}
