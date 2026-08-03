package meat

import (
	"context"
	"fmt"
	"net"
	"net/http"
	"net/url"
	"os"
	"strings"
	"sync"
	"time"
)

const oauthSuccessHTML = `<!DOCTYPE html><html><head><meta charset="utf-8"><title>meat</title></head>
<body style="font-family:system-ui,sans-serif;max-width:32rem;margin:3rem auto;padding:0 1rem">
<h1>Authentication complete</h1>
<p>You can close this window and return to the terminal.</p>
</body></html>`

const oauthErrorHTML = `<!DOCTYPE html><html><head><meta charset="utf-8"><title>meat</title></head>
<body style="font-family:system-ui,sans-serif;max-width:32rem;margin:3rem auto;padding:0 1rem">
<h1>Authentication failed</h1>
<p>%s</p>
</body></html>`

// oauthCallbackResult is the authorization code delivered to the local server.
type oauthCallbackResult struct {
	Code  string
	State string
}

// runOAuthCallbackServer listens on host:port until a matching callback arrives,
// ctx is cancelled, or the optional manual channel yields a code.
func runOAuthCallbackServer(ctx context.Context, host string, port int, path string, expectedState string, manual <-chan oauthCallbackResult) (oauthCallbackResult, error) {
	resultCh := make(chan oauthCallbackResult, 1)
	errCh := make(chan error, 1)

	mux := http.NewServeMux()
	mux.HandleFunc(path, func(w http.ResponseWriter, r *http.Request) {
		q := r.URL.Query()
		if errParam := q.Get("error"); errParam != "" {
			w.Header().Set("content-type", "text/html; charset=utf-8")
			w.WriteHeader(http.StatusBadRequest)
			fmt.Fprintf(w, oauthErrorHTML, "Error: "+htmlEscape(errParam))
			return
		}
		code := q.Get("code")
		state := q.Get("state")
		if code == "" || state == "" {
			w.Header().Set("content-type", "text/html; charset=utf-8")
			w.WriteHeader(http.StatusBadRequest)
			fmt.Fprintf(w, oauthErrorHTML, "Missing code or state parameter.")
			return
		}
		if expectedState != "" && state != expectedState {
			w.Header().Set("content-type", "text/html; charset=utf-8")
			w.WriteHeader(http.StatusBadRequest)
			fmt.Fprintf(w, oauthErrorHTML, "State mismatch.")
			return
		}
		w.Header().Set("content-type", "text/html; charset=utf-8")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(oauthSuccessHTML))
		select {
		case resultCh <- oauthCallbackResult{Code: code, State: state}:
		default:
		}
	})

	ln, err := net.Listen("tcp", fmt.Sprintf("%s:%d", host, port))
	if err != nil {
		return oauthCallbackResult{}, fmt.Errorf("listen on %s:%d: %w (is another login already running?)", host, port, err)
	}

	srv := &http.Server{Handler: mux, ReadHeaderTimeout: 10 * time.Second}
	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		if err := srv.Serve(ln); err != nil && err != http.ErrServerClosed {
			select {
			case errCh <- err:
			default:
			}
		}
	}()
	defer func() {
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		_ = srv.Shutdown(shutdownCtx)
		wg.Wait()
	}()

	select {
	case <-ctx.Done():
		return oauthCallbackResult{}, ctx.Err()
	case err := <-errCh:
		return oauthCallbackResult{}, err
	case res := <-resultCh:
		return res, nil
	case res, ok := <-manual:
		if !ok {
			return oauthCallbackResult{}, fmt.Errorf("manual authorization input closed")
		}
		return res, nil
	}
}

func htmlEscape(s string) string {
	replacer := strings.NewReplacer("&", "&amp;", "<", "&lt;", ">", "&gt;", `"`, "&quot;")
	return replacer.Replace(s)
}

// parseAuthorizationInput accepts a bare code, code#state, query string, or full redirect URL.
func parseAuthorizationInput(input, fallbackState string) (oauthCallbackResult, error) {
	value := strings.TrimSpace(input)
	if value == "" {
		return oauthCallbackResult{}, fmt.Errorf("empty authorization input")
	}
	if u, err := url.Parse(value); err == nil && u.Scheme != "" && u.Host != "" {
		code := u.Query().Get("code")
		state := u.Query().Get("state")
		if code != "" {
			if state == "" {
				state = fallbackState
			}
			return oauthCallbackResult{Code: code, State: state}, nil
		}
	}
	if strings.Contains(value, "code=") {
		q := value
		if i := strings.Index(value, "?"); i >= 0 {
			q = value[i+1:]
		}
		vals, err := url.ParseQuery(q)
		if err == nil && vals.Get("code") != "" {
			state := vals.Get("state")
			if state == "" {
				state = fallbackState
			}
			return oauthCallbackResult{Code: vals.Get("code"), State: state}, nil
		}
	}
	if strings.Contains(value, "#") {
		code, state, _ := strings.Cut(value, "#")
		if code != "" {
			if state == "" {
				state = fallbackState
			}
			return oauthCallbackResult{Code: code, State: state}, nil
		}
	}
	return oauthCallbackResult{Code: value, State: fallbackState}, nil
}

func oauthCallbackHost() string {
	if v := strings.TrimSpace(os.Getenv("MEAT_OAUTH_CALLBACK_HOST")); v != "" {
		return v
	}
	return "127.0.0.1"
}
