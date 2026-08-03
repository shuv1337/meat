package main

import (
	"context"
	"fmt"
	"os"
	"strings"
	"time"

	"meat.dev/meat"
)

const authUsage = `meat login/logout/auth — subscription OAuth for OpenAI and Anthropic

Usage:
  meat login openai [--device]   Log in with ChatGPT Plus/Pro (Codex OAuth)
  meat login anthropic           Log in with Claude Pro/Max
  meat logout [openai|anthropic] Remove stored OAuth credentials (all if omitted)
  meat auth status               Show stored OAuth credential status

Credentials are stored in ~/.meat/auth.json (mode 0600). Explicit API keys
(OPENAI_API_KEY / ANTHROPIC_API_KEY) still take precedence over OAuth.
`

func tryAuthCommand(args []string) bool {
	if len(args) == 0 {
		return false
	}
	switch args[0] {
	case "login", "logout", "auth":
		if err := runAuthCommand(args); err != nil {
			fatal("%v", err)
		}
		return true
	default:
		return false
	}
}

func runAuthCommand(args []string) error {
	switch args[0] {
	case "login":
		return runLogin(args[1:])
	case "logout":
		return runLogout(args[1:])
	case "auth":
		if len(args) < 2 {
			return fmt.Errorf("usage: meat auth status")
		}
		switch args[1] {
		case "status":
			return runAuthStatus()
		case "-h", "--help", "help":
			fmt.Fprint(os.Stderr, authUsage)
			return nil
		default:
			return fmt.Errorf("unknown auth subcommand %q\n\n%s", args[1], authUsage)
		}
	default:
		return fmt.Errorf("unknown command %q", args[0])
	}
}

func runLogin(args []string) error {
	if len(args) == 0 || args[0] == "-h" || args[0] == "--help" {
		fmt.Fprint(os.Stderr, authUsage)
		if len(args) == 0 {
			return fmt.Errorf("usage: meat login openai|anthropic")
		}
		return nil
	}
	provider := strings.ToLower(args[0])
	rest := args[1:]
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Minute)
	defer cancel()

	switch provider {
	case "openai", "openai-codex", "codex", "chatgpt":
		method := "browser"
		for _, a := range rest {
			switch a {
			case "--device", "-device", "device":
				method = "device"
			case "-h", "--help":
				fmt.Fprint(os.Stderr, authUsage)
				return nil
			default:
				return fmt.Errorf("unknown login flag %q", a)
			}
		}
		cred, err := meat.LoginOpenAICodexOAuth(ctx, method)
		if err != nil {
			return err
		}
		fmt.Fprintf(os.Stderr, "Logged in to OpenAI (ChatGPT). account=%s expires=%s\n",
			cred.AccountID, formatExpiry(cred.Expires))
		return nil
	case "anthropic", "claude":
		if len(rest) > 0 {
			return fmt.Errorf("anthropic login takes no flags")
		}
		cred, err := meat.LoginAnthropicOAuth(ctx)
		if err != nil {
			return err
		}
		fmt.Fprintf(os.Stderr, "Logged in to Anthropic (Claude Pro/Max). expires=%s\n", formatExpiry(cred.Expires))
		return nil
	default:
		return fmt.Errorf("unknown provider %q (want openai or anthropic)", provider)
	}
}

func runLogout(args []string) error {
	if len(args) == 0 {
		if err := meat.DeleteOAuthCredential(meat.OAuthProviderOpenAICodex); err != nil {
			return err
		}
		if err := meat.DeleteOAuthCredential(meat.OAuthProviderAnthropic); err != nil {
			return err
		}
		fmt.Fprintln(os.Stderr, "Removed all stored OAuth credentials.")
		return nil
	}
	switch strings.ToLower(args[0]) {
	case "openai", "openai-codex", "codex", "chatgpt":
		if err := meat.DeleteOAuthCredential(meat.OAuthProviderOpenAICodex); err != nil {
			return err
		}
		fmt.Fprintln(os.Stderr, "Removed OpenAI OAuth credentials.")
	case "anthropic", "claude":
		if err := meat.DeleteOAuthCredential(meat.OAuthProviderAnthropic); err != nil {
			return err
		}
		fmt.Fprintln(os.Stderr, "Removed Anthropic OAuth credentials.")
	case "-h", "--help":
		fmt.Fprint(os.Stderr, authUsage)
	default:
		return fmt.Errorf("unknown provider %q (want openai or anthropic)", args[0])
	}
	return nil
}

func runAuthStatus() error {
	creds, err := meat.ListOAuthCredentials()
	if err != nil {
		return err
	}
	if len(creds) == 0 {
		fmt.Println("No stored OAuth credentials.")
		fmt.Println("Run: meat login openai | meat login anthropic")
		return nil
	}
	now := time.Now()
	for _, provider := range []string{meat.OAuthProviderOpenAICodex, meat.OAuthProviderAnthropic} {
		cred, ok := creds[provider]
		if !ok {
			continue
		}
		label := provider
		if provider == meat.OAuthProviderOpenAICodex {
			label = "openai (chatgpt)"
		}
		status := "ok"
		if cred.Expires > 0 && now.UnixMilli() >= cred.Expires {
			status = "expired"
		} else if cred.Expires > 0 && now.Add(5*time.Minute).UnixMilli() >= cred.Expires {
			status = "expiring_soon"
		}
		extra := ""
		if cred.AccountID != "" {
			extra = " account=" + cred.AccountID
		}
		fmt.Printf("%s: %s expires=%s%s\n", label, status, formatExpiry(cred.Expires), extra)
	}
	return nil
}

func formatExpiry(ms int64) string {
	if ms <= 0 {
		return "unknown"
	}
	return time.UnixMilli(ms).Local().Format(time.RFC3339)
}
