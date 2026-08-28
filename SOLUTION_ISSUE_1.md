# Solution for Issue #1

## 🛠️ Proposed Solution (by Aditya Waghamare)

### Analysis
When executing `gh api` or other commands requiring authentication, `cli` resolves auth tokens through a hierarchy of credential providers: environment variables (`GH_TOKEN`/`GITHUB_TOKEN`), system keyring, and configuration files (`hosts.yml`).

Currently, operational errors encountered while reading from system secret stores (such as D-Bus session disconnects, locked keychains, OS permission denials, or broken secret services) are swallowed or converted to empty token strings. Consequently, `gh api` treats keyring access failures as if no credentials exist and silently proceeds with unauthenticated HTTP requests. This leads to misleading API errors such as `HTTP 404 Not Found` for private resources or `HTTP 403 API rate limit exceeded`, masking the true underlying system keyring error.

### Fix
1. **Error Discrimination**: Explicitly differentiate between `keyring.ErrNotFound` (valid indication that no secret is stored) and system/operational errors (keychain locked, service unavailable, permissions denied).
2. **Keyring Operation Error**: Introduce a specialized error type `KeyringOpError` wrapping the underlying system error and target host/account context.
3. **Chain Halting**: In the credential resolution pipeline, missing credentials allow fallback to the next provider (`env` -> `keyring` -> `config`), whereas operational keyring errors immediately abort resolution and bubble up.
4. **Early Command Abort**: In `pkg/cmd/api` and `pkg/cmd/factory`, detect operational keyring errors before constructing or executing the HTTP request, aborting command execution and printing a clear, actionable diagnostic to `stderr`.

---

### Implementation

#### 1. Custom Keyring Error Type (`internal/config/keyring.go` / `pkg/cmd/factory`)

```go
package config

import (
	"errors"
	"fmt"
	"github.com/zalando/go-keyring"
)

// KeyringOpError represents an operational error accessing the system keyring,
// distinct from a missing secret (ErrNotFound).
type KeyringOpError struct {
	Host  string
	Err   error
}

func (e *KeyringOpError) Error() string {
	return fmt.Sprintf("failed to access system keyring for host %s: %v", e.Host, e.Err)
}

func (e *KeyringOpError) Unwrap() error {
	return e.Err
}

// GetTokenFromKeyring fetches token for host, properly distinguishing missing secret vs operational error.
func GetTokenFromKeyring(service, account string) (string, error) {
	token, err := keyring.Get(service, account)
	if err != nil {
		if errors.Is(err, keyring.ErrNotFound) {
			return "", keyring.ErrNotFound
		}
		return "", &KeyringOpError{
			Host: account,
			Err:  err,
		}
	}
	return token, nil
}
```

#### 2. Auth Resolution with Operational Error Handling (`internal/config/config.go`)

```go
package config

import (
	"errors"
	"fmt"
	"os"
	"github.com/zalando/go-keyring"
)

// AuthTokenForHost resolves authentication tokens for a target host, halting on operational errors.
func (c *Config) AuthTokenForHost(host string) (string, string, error) {
	// 1. Check environment variables
	if token := os.Getenv("GH_TOKEN"); token != "" {
		return token, "GH_TOKEN", nil
	}
	if token := os.Getenv("GITHUB_TOKEN"); token != "" {
		return token, "GITHUB_TOKEN", nil
	}

	// 2. Check system keyring
	token, err := GetTokenFromKeyring("gh:"+host, "oauth_token")
	if err == nil && token != "" {
		return token, "keyring", nil
	}
	if err != nil && !errors.Is(err, keyring.ErrNotFound) {
		// Operational keyring failure (e.g., locked keychain, D-Bus error) — halt resolution
		return "", "", err
	}

	// 3. Fallback to config file (hosts.yml) if token wasn't in keyring
	if hostConfig, err := c.GetHostConfig(host); err == nil && hostConfig.Token != "" {
		return hostConfig.Token, "config", nil
	}

	return "", "", nil
}
```

#### 3. Aborting `gh api` Execution on Keyring Errors (`pkg/cmd/api/api.go`)

```go
package api

import (
	"errors"
	"fmt"
	"io"
	"os"

	"github.com/cli/cli/v2/internal/config"
	"github.com/spf13/cobra"
)

func NewCmdApi(f *cmdutil.Factory, runF func(*ApiOptions) error) *cobra.Command {
	opts := &ApiOptions{
		HttpClient: f.HttpClient,
		Config:     f.Config,
		IO:         f.IOStreams,
	}

	cmd := &cobra.Command{
		Use:   "api <endpoint>",
		Short: "Make an authenticated HTTP request to the GitHub API",
		RunE: func(cmd *cobra.Command, args []string) error {
			opts.Endpoint = args[0]

			// Resolve token to verify keyring status prior to network execution
			cfg, err := opts.Config()
			if err != nil {
				return err
			}

			hostname := opts.Host
			if hostname == "" {
				hostname, _ = cfg.DefaultHost()
			}

			token, source, err := cfg.AuthTokenForHost(hostname)
			var keyringErr *config.KeyringOpError
			if errors.As(err, &keyringErr) {
				// Abort immediately with a descriptive error message on stderr
				fmt.Fprintf(opts.IO.ErrOut, "error: system keyring failure encountered while authenticating for %s\n\n", hostname)
				fmt.Fprintf(opts.IO.ErrOut, "Details: %v\n\n", keyringErr.Err)
				fmt.Fprintf(opts.IO.ErrOut, "Troubleshooting:\n")
				fmt.Fprintf(opts.IO.ErrOut, "  - Verify system keyring daemon status (e.g. gnome-keyring, D-Bus, secret-service)\n")
				fmt.Fprintf(opts.IO.ErrOut, "  - Unlock your login keychain if locked\n")
				fmt.Fprintf(opts.IO.ErrOut, "  - Or set the GH_TOKEN environment variable to bypass keyring lookup\n\n")
				return fmt.Errorf("keyring access failed for %s: %w", hostname, keyringErr.Err)
			}

			if runF != nil {
				return runF(opts)
			}
			return apiRun(opts)
		},
	}
	return cmd
}
```

---

### Testing

Unit test additions (`internal/config/config_test.go`):

```go
package config_test

import (
	"errors"
	"testing"

	"github.com/cli/cli/v2/internal/config"
	"github.com/stretchr/testify/assert"
	"github.com/zalando/go-keyring"
)

type mockKeyringStore struct {
	getFunc func(service, user string) (string, error)
}

func TestAuthTokenForHost_KeyringOperationalError(t *testing.T) {
	// Mock operational keyring error (e.g. D-Bus disconnected)
	dbusErr := errors.New("dbus: connection closed")
	
	keyringErr := &config.KeyringOpError{
		Host: "github.com",
		Err:  dbusErr,
	}

	assert.True(t, errors.Is(keyringErr, dbusErr))
	assert.Contains(t, keyringErr.Error(), "failed to access system keyring")
}

func TestAuthTokenForHost_NotFoundAllowsFallback(t *testing.T) {
	// ErrNotFound should allow resolution to proceed to hosts.yml config file
	cfg := config.NewBlankConfig()
	cfg.SetHostToken("github.com", "config_token_123")

	token, source, err := cfg.AuthTokenForHost("github.com")
	assert.NoError(t, err)
	assert.Equal(t, "config_token_123", token)
	assert.Equal(t, "config", source)
}
```

Signed-off-by: Aditya Waghamare <adityawaghamare7620@gmail.com>

---
*Submitted by Aditya Waghamare*
💰 **Payout Address (Base L2 / EVM):** `0xb61dBcdBc3407F71EaCb64D4CBFAcf9FFfe2415C`