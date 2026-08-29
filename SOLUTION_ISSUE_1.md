# Solution for Issue #1

## 🛠️ Proposed Solution (by Aditya Waghamare)

### Analysis
When `gh api` resolves authentication credentials for a target host, it queries configured credential providers in sequence: environment variables (`GH_TOKEN`/`GITHUB_TOKEN`), system keyring, and the `hosts.yml` config file. Currently, errors returned during keyring lookup (such as D-Bus disconnects, locked keychains, or OS permission errors) are caught and ignored or treated identically to token absence (`keyring.ErrNotFound`).

This causes `gh api` to silently fail back to unauthenticated API calls, resulting in misleading `HTTP 404 Not Found` (for private resources) or `HTTP 403 API rate limit exceeded` errors rather than informing the user that keyring access failed.

### Fix
1. **Differentiate Token Absence vs Operational Keyring Errors**: In token lookup (`pkg/cmd/factory` and `internal/config`), check if the error returned by the keyring is `errors.Is(err, keyring.ErrNotFound)`. If `ErrNotFound`, proceed with standard fallback logic. If any other operational error occurs, return a wrapped `KeyringAccessError`.
2. **Immediate Abort with Descriptive Error**: In `pkg/cmd/api/api.go` (and HTTP client factory), catch `KeyringAccessError` during client creation/token resolution, print a clear diagnostic error message to `stderr`, and exit immediately with a non-zero exit code.
3. **Troubleshooting Guidance**: Include clear instructions in the error output advising users to check their system keyring/secret service or export `GH_TOKEN` as a workaround.

---

### Implementation

#### 1. Define `KeyringAccessError` and Update Keyring Lookup (`internal/config/auth.go` / `pkg/cmd/factory/factory.go`)

```go
package config

import (
	"errors"
	"fmt"
	"github.com/zalando/go-keyring"
)

// KeyringAccessError represents an operational error when communicating with the OS secret store.
type KeyringAccessError struct {
	Host string
	Err  error
}

func (e *KeyringAccessError) Error() string {
	return fmt.Sprintf("failed to access system keyring for host %q: %v", e.Host, e.Err)
}

func (e *KeyringAccessError) Unwrap() error {
	return e.Err
}

// TokenFromKeyring attempts to fetch the auth token from system keyring for the given host.
func TokenFromKeyring(service, host string) (string, error) {
	token, err := keyring.Get(service, host)
	if err != nil {
		if errors.Is(err, keyring.ErrNotFound) {
			// Token does not exist in keyring; continue to fallback options
			return "", nil
		}
		// Operational error (e.g. locked keychain, D-Bus session unavailable, permission denied)
		return "", &KeyringAccessError{
			Host: host,
			Err:  err,
		}
	}
	return token, nil
}
```

#### 2. Update Auth Token Resolution Logic (`internal/config/config.go`)

```go
// AuthTokenForHost returns the authentication token for a host, preserving operational errors.
func (c *Config) AuthTokenForHost(host string) (string, string, error) {
	// 1. Check Environment Variables
	if token, source := tokenFromEnv(host); token != "" {
		return token, source, nil
	}

	// 2. Check System Keyring
	token, err := TokenFromKeyring("gh:github.com", host)
	if err != nil {
		// Do not swallow operational keyring errors!
		return "", "", err
	}
	if token != "" {
		return token, "keyring", nil
	}

	// 3. Fallback to hosts config file
	token, source := c.tokenFromHostsConfig(host)
	if token != "" {
		return token, source, nil
	}

	return "", "", nil
}
```

#### 3. Update API Command Execution (`pkg/cmd/api/api.go`)

```go
package api

import (
	"errors"
	"fmt"
	"os"

	"github.com/cli/cli/v2/internal/config"
	"github.com/cli/cli/v2/pkg/cmdutil"
	"github.com/spf13/cobra"
)

func NewCmdApi(f *cmdutil.Factory, runF func(*ApiOptions) error) *cobra.Command {
	opts := &ApiOptions{
		HttpClient: f.HttpClient,
		Config:     f.Config,
	}

	cmd := &cobra.Command{
		Use:   "api <endpoint>",
		Short: "Make an authenticated HTTP request to the GitHub API",
		RunE: func(cmd *cobra.Command, args []string) error {
			if len(args) > 0 {
				opts.Endpoint = args[0]
			}

			if runF != nil {
				return runF(opts)
			}
			return apiRun(opts)
		},
	}

	return cmd
}

func apiRun(opts *ApiOptions) error {
	client, err := opts.HttpClient()
	if err != nil {
		var keyringErr *config.KeyringAccessError
		if errors.As(err, &keyringErr) {
			return fmt.Errorf(
				"error: system keyring operational failure\n\n"+
					"Could not retrieve token for host %q: %v\n\n"+
					"Troubleshooting:\n"+
					"  - Ensure your system keyring daemon (gnome-keyring, KWallet, macOS Keychain) is running and unlocked.\n"+
					"  - Alternatively, set GH_TOKEN or GITHUB_TOKEN environment variable to bypass keyring access.\n",
				keyringErr.Host, keyringErr.Err,
			)
		}
		return fmt.Errorf("could not create HTTP client: %w", err)
	}

	// Proceed with API request execution using authenticated client...
	return executeApiRequest(client, opts)
}
```

---

### Testing

1. **Unit Test - Operational Keyring Error Propagation**:
   - Mock `keyring.Get` to return `errors.New("dbus: connection closed")`.
   - Verify `AuthTokenForHost` returns `KeyringAccessError` instead of falling through to unauthenticated request.
2. **Unit Test - Missing Token Fallback**:
   - Mock `keyring.Get` to return `keyring.ErrNotFound`.
   - Verify `AuthTokenForHost` falls back to config file or returns empty token without error.
3. **Integration Test - CLI Execution (`gh api`)**:
   - Lock system keychain or simulate D-Bus failure.
   - Run `gh api user`.
   - Verify non-zero exit code and expected error message output on `stderr`.
   - Set `GH_TOKEN=ghp_xxx gh api user` and verify successful request (bypassing keyring operational error).

Signed-off-by: Aditya Waghamare <adityawaghamare7620@gmail.com>

---
*Submitted by Aditya Waghamare*
💰 **Payout Address (Base L2 / EVM):** `0xb61dBcdBc3407F71EaCb64D4CBFAcf9FFfe2415C`