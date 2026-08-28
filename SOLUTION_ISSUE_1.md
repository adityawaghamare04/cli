# Solution for Issue #1

## 🛠️ Proposed Solution (by Aditya Waghamare)

### Analysis
When `gh` resolves authentication tokens across providers (environment variables -> keyring -> config file), operational keyring failures (e.g., locked keychain, D-Bus disconnection, OS permission denied) are currently caught and treated as `keyring.ErrNotFound`. This causes token resolution to silently return an empty string, leading `gh api` to issue unauthenticated HTTP requests that result in confusing downstream errors like `HTTP 404` (for private repos) or `HTTP 403` (rate limiting).

### Fix
1. Distinguish between a missing token (`errors.Is(err, keyring.ErrNotFound)`) and operational keyring failures (e.g., system daemon errors, permission issues).
2. Wrap keyring operational errors into a distinct error type (`KeyringError`) containing actionable troubleshooting instructions (verifying service status or setting `GH_TOKEN`).
3. Propagate operational errors up the credential resolution chain in `internal/config` and `pkg/cmd/factory` so that execution halts immediately before any HTTP request is constructed or executed.

### Implementation

#### 1. Credential Resolution & Keyring Error Propagation (`internal/config/auth.go` & `pkg/authtoken/authtoken.go`)

```go
package config

import (
	"errors"
	"fmt"
	"os"

	"github.com/zalando/go-keyring"
)

// KeyringError represents an operational failure when communicating with the OS credential store.
type KeyringError struct {
	Host string
	Err  error
}

func (e *KeyringError) Error() string {
	return fmt.Sprintf("failed to access keyring for host %s: %v", e.Host, e.Err)
}

func (e *KeyringError) Unwrap() error {
	return e.Err
}

// AuthTokenForHost resolves authentication tokens for the given host.
// It checks: 1. Environment variables, 2. Keyring credential store, 3. Config file.
// Operational errors during keyring access halt execution immediately.
func (c *Config) AuthTokenForHost(host string) (string, string, error) {
	// 1. Environment Variable check
	if host == "github.com" {
		if token := os.Getenv("GH_TOKEN"); token != "" {
			return token, "GH_TOKEN", nil
		}
		if token := os.Getenv("GITHUB_TOKEN"); token != "" {
			return token, "GITHUB_TOKEN", nil
		}
	}

	// 2. System Keyring resolution
	token, err := c.getkeyringToken(host)
	if err == nil && token != "" {
		return token, "keyring", nil
	}
	if err != nil && !errors.Is(err, keyring.ErrNotFound) {
		return "", "keyring", &KeyringError{
			Host: host,
			Err:  err,
		}
	}

	// 3. Hosts Configuration File fallback
	token, err = c.getHostsConfigToken(host)
	if err != nil {
		return "", "config", err
	}
	if token != "" {
		return token, "config", nil
	}

	return "", "", nil
}
```

#### 2. Factory & HTTP Client Token Error Handling (`pkg/cmd/factory/http.go`)

```go
package factory

import (
	"fmt"
	"net/http"

	"github.com/cli/cli/v2/internal/config"
)

func NewHTTPClient(cfg config.Config, host string) (*http.Client, error) {
	token, source, err := cfg.AuthTokenForHost(host)
	if err != nil {
		var keyringErr *config.KeyringError
		if errors.As(err, &keyringErr) {
			return nil, fmt.Errorf("error: keyring operational failure for host %q: %w\n"+
				"Troubleshooting:\n"+
				"  - Check if your OS credential service (e.g., Secret Service, D-Bus, Keychain) is running and unlocked.\n"+
				"  - Alternatively, bypass keyring access by setting the GH_TOKEN environment variable.",
				host, keyringErr.Err)
		}
		return nil, fmt.Errorf("failed to resolve credentials for host %q: %w", host, err)
	}

	// Construct HTTP client with optional auth token
	return newClientWithToken(token, source), nil
}
```

#### 3. API Command Abort on Auth Failure (`pkg/cmd/api/api.go`)

```go
package api

import (
	"fmt"
	"github.com/spf13/cobra"
	"github.com/cli/cli/v2/pkg/cmdutil"
)

func NewCmdApi(f *cmdutil.Factory, runF func(*ApiOptions) error) *cobra.Command {
	opts := &ApiOptions{}

	cmd := &cobra.Command{
		Use:   "api <endpoint>",
		Short: "Make an authenticated API request",
		RunE: func(cmd *cobra.Command, args []string) error {
			opts.Endpoint = args[0]
			
			// Resolve HTTP client before executing request
			httpClient, err := f.HttpClient()
			if err != nil {
				// Immediately surface keyring / auth resolution error to stderr and halt
				fmt.Fprintf(f.IOStreams.ErrOut, "%v\n", err)
				return cmdutil.SilentError
			}
			opts.HttpClient = httpClient

			if runF != nil {
				return runF(opts)
			}
			return apiRun(opts)
		},
	}
	return cmd
}
```

### Testing

#### Unit Test: Keyring Failure Aborts Execution (`internal/config/config_test.go`)

```go
package config

import (
	"errors"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/zalando/go-keyring"
)

type mockKeyring struct {
	err error
}

func (m *mockKeyring) Get(service, user string) (string, error) {
	return "", m.err
}

func TestAuthTokenForHost_KeyringOperationalError(t *testing.T) {
	cfg := &Config{
		keyringBackend: &mockKeyring{err: errors.New("dbus: connection closed")},
	}

	token, source, err := cfg.AuthTokenForHost("github.com")

	assert.Empty(t, token)
	assert.Equal(t, "keyring", source)
	assert.Error(t, err)

	var keyringErr *KeyringError
	assert.True(t, errors.As(err, &keyringErr))
	assert.Contains(t, err.Error(), "dbus: connection closed")
}

func TestAuthTokenForHost_KeyringNotFound_FallsBack(t *testing.T) {
	cfg := &Config{
		keyringBackend: &mockKeyring{err: keyring.ErrNotFound},
	}

	token, source, err := cfg.AuthTokenForHost("github.com")

	assert.NoError(t, err)
	assert.Empty(t, token)
}
```

Signed-off-by: Aditya Waghamare <adityawaghamare7620@gmail.com>

---
*Submitted by Aditya Waghamare*
💰 **Payout Address (Base L2 / EVM):** `0xb61dBcdBc3407F71EaCb64D4CBFAcf9FFfe2415C`