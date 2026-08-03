package meat

import (
	"bufio"
	"context"
	"fmt"
	"io"
	"os"
	"strings"
)

// osStderr is where login progress is written (overridable in tests).
var osStderr io.Writer = os.Stderr

// promptManualOAuthCode reads a pasted code/URL from stdin and sends it on ch.
// It is cancelled when ctx ends.
func promptManualOAuthCode(ctx context.Context, placeholder, fallbackState string, ch chan<- oauthCallbackResult) {
	fmt.Fprintf(osStderr, "Complete login in your browser, or paste the authorization code / redirect URL here\n(placeholder: %s):\n", placeholder)
	lineCh := make(chan string, 1)
	go func() {
		scanner := bufio.NewScanner(os.Stdin)
		if scanner.Scan() {
			lineCh <- scanner.Text()
		} else {
			lineCh <- ""
		}
	}()
	select {
	case <-ctx.Done():
		return
	case line := <-lineCh:
		line = strings.TrimSpace(line)
		if line == "" {
			return
		}
		res, err := parseAuthorizationInput(line, fallbackState)
		if err != nil {
			fmt.Fprintf(osStderr, "invalid authorization input: %v\n", err)
			return
		}
		select {
		case ch <- res:
		case <-ctx.Done():
		}
	}
}
