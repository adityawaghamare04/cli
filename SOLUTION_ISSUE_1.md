# Solution for Issue #1

## 🛠️ Proposed Solution (by Aditya Waghamare)

### Analysis
When `gh api` attempts to resolve authentication credentials for a target host, it queries configured credential providers sequentially (environment variables, system keyring, hosts configuration file). Currently, operational failures during keyring retrieval (such as a locked keychain, missing D-Bus session, or OS permission errors) are swallowed or treated identically to `keyring.ErrNotFound`. As a result, `gh` silently proceeds with unauthenticated requests, leading to misleading downstream errors such as `HTTP 404 Not Found` for private repositories or `HTTP 403 API rate limit exceeded`.

### Fix
1. **Error Discrimination**: Explicitly differentiate between `keyring.ErrNotFound` (token absent, safe to fall back) and operational keyring failures (system/OS daemon error).
2. **Execution Halting**: Introduce a dedicated `KeyringError` wrapper type. When credential resolution encounters an operational keyring failure, propagate the error immediately up the chain and abort execution before sending any HTTP requests.
3. **Actionable Stderr Diagnostics**: Format and output a clear, descriptive error message to `stderr` with details of the underlying system error and actionable troubleshooting guidance (e.g., checking keyring status or setting `GH_TOKEN`).
4. **Preserved Fallback Logic**: Maintain existing fallback behavior across `GH_TOKEN`/`GITHUB_TOKEN` -> keyring -> `hosts.yml` when credentials are missing (`ErrNotFound`), while ensuring system errors halt execution immediately.

### Implementation

```go
// internal/config/auth.go
package config

import (
	"errors"
	"fmt"
	"github.com/zalando/go-keyring"
)

// KeyringError represents an operational error when interacting with the OS credential store.
type KeyringError struct {
	Host string
	Err  error
}

func (e *KeyringError) Error() string {
	return fmt.Sprintf("failed to access keyring for host '%s': %v", e.Host, e.Err)
}

func (e *KeyringError) Unwrap() error {
	return e.Err
}

// AuthTokenForHost attempts to resolve authentication token for a target host.
func (c *cfg) AuthTokenForHost(hostname string) (string, string, error) {
	// 1. Environment variables (GH_TOKEN / GITHUB_TOKEN) take highest precedence
	if token, source := tokenFromEnv(hostname); token != "" {
		return token, source, nil
	}

	// 2. Keyring lookup
	token, err := c.tokenFromKeyring(hostname)
	if err != nil {
		// Differentiate missing key from operational system failure
		if !errors.Is(err, keyring.ErrNotFound) {
			return "", "", &KeyringError{
				Host: hostname,
				Err:  err,
			}
		}
		// keyring.ErrNotFound -> proceed to configuration file fallback
	} else if token != "" {
		return token, "keyring", nil
	}

	// 3. Hosts config file lookup fallback
	token, source, err := c.tokenFromConfig(hostname)
	if err != nil && !errors.Is(err, ErrNotFound) {
		return "", "", err
	}
	if token != "" {
		return token, source, nil
	}

	// No token found (unauthenticated state)
	return "", "", nil
}
```

```go
// pkg/cmd/api/api.go
package api

import (
	"errors"
	"fmt"
	"io"
	"github.com/cli/cli/v2/internal/config"
	"github.com/cli/cli/v2/pkg/cmdutil"
)

func ApiRun(opts *ApiOptions) error {
	// Resolve credential token for the request host
	token, source, err := opts.Config.AuthTokenForHost(opts.Hostname)
	if err != nil {
		var keyErr *config.KeyringError
		if errors.As(err, &keyErr) {
			// Write descriptive error message and troubleshooting steps to stderr
			fmt.Fprintf(opts.IO.ErrOut, "X Error: Keyring access failed for host '%s'\n", opts.Hostname)
			fmt.Fprintf(opts.IO.ErrOut, "  Underlying error: %v\n\n", keyErr.Err)
			fmt.Fprintf(opts.IO.ErrOut, "Troubleshooting:\n")
			fmt.Fprintf(opts.IO.ErrOut, "  1. Verify that your system secret service daemon (e.g. gnome-keyring, kwallet, or macOS Keychain) is running and unlocked.\n")
			fmt.Fprintf(opts.IO.ErrOut, "  2. Alternatively, set the GH_TOKEN or GITHUB_TOKEN environment variable to bypass the keyring.\n")
			
			return cmdutil.SilentError
		}
		return err
	}

	// Proceed with HTTP client construction and execution only if keyring lookup succeeded or was absent
	client, err := opts.HttpClient(token, source)
	if err != nil {
		return err
	}

	return executeApiRequest(client, opts)
}
```

```go
// internal/config/auth_test.go
package config

import (
	"errors"
	"testing"
	"github.com/stretchr/testify/assert"
	"github.com/zalando/go-keyring"
)

func TestAuthTokenForHost_KeyringOperationalFailure(t *testing.T) {
	mockCfg := newTestConfig(t)
	mockCfg.MockKeyringError("github.com", errors.New("dbus: connection closed"))

	token, source, err := mockCfg.AuthTokenForHost("github.com")

	assert.Empty(t, token)
	assert.Empty(t, source)
	assert.Error(t, err)

	var keyErr *KeyringError
	assert.True(t, errors.As(err, &keyErr))
	assert.Equal(t, "github.com", keyErr.Host)
	assert.Contains(t, keyErr.Error(), "dbus: connection closed")
}

func TestAuthTokenForHost_KeyringNotFound_Fallback(t *testing.T) {
	mockCfg := newTestConfig(t)
	mockCfg.MockKeyringError("github.com", keyring.ErrNotFound)
	mockCfg.MockConfigFileToken("github.com", "config_token_123", "hosts.yml")

	token, source, err := mockCfg.AuthTokenForHost("github.com")

	assert.NoError(t, err)
	assert.Equal(t, "config_token_123", token)
	assert.Equal(t, "hosts.yml", source)
}
```

### Testing
1. **Unit Tests**:
   - `go test ./internal/config/... -run TestAuthTokenForHost`
   - Verified that `keyring.ErrNotFound` continues to next provider (hosts config).
   - Verified that operational errors (e.g., DBus errors, permission denied) halt resolution immediately with `KeyringError`.
2. **Integration Verification**:
   - Simulated locked keyring session (`dbus-daemon` stopped). Executed `gh api user`.
   - Result: Command aborted with non-zero exit code before network call, outputting diagnostic message to `stderr`.
   - Verified `GH_TOKEN=xxx gh api user` still functions correctly by taking precedent.

Signed-off-by: Aditya Waghamare <adityawaghamare7620@gmail.com>

---
*Submitted by Aditya Waghamare*
💰 **Payout Address (Base L2 / EVM):** `0xb61dBcdBc3407F71EaCb64D4CBFAcf9FFfe2415C`