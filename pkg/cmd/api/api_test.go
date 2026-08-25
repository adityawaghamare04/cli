package api

import (
	"bytes"
	"errors"
	"io"
	"net/http"
	"strings"
	"testing"
)

type mockAuth struct {
	token string
	err   error
}

func (m *mockAuth) TokenForHost(hostname string) (string, error) {
	return m.token, m.err
}

type mockConfig struct {
	auth AuthConfig
}

func (m *mockConfig) Authentication() AuthConfig {
	return m.auth
}

type mockHTTPClient struct {
	resp *http.Response
	err  error
	reqs []*http.Request
}

func (m *mockHTTPClient) Do(req *http.Request) (*http.Response, error) {
	m.reqs = append(m.reqs, req)
	if m.err != nil {
		return nil, m.err
	}
	return m.resp, nil
}

func TestApiRun_MissingTokenReturnsError(t *testing.T) {
	t.Setenv("GH_TOKEN", "")
	t.Setenv("GITHUB_TOKEN", "")
	t.Setenv("GH_ENTERPRISE_TOKEN", "")

	opts := &ApiOptions{
		Endpoint: "user",
		TokenForHost: func(host string) (string, error) {
			return "", nil
		},
	}

	err := ApiRun(opts)
	if err == nil {
		t.Fatal("expected error for missing token, got nil")
	}

	expectedMsg := "no authentication token found for host github.com. To authenticate, run 'gh auth login -h github.com'"
	if err.Error() != expectedMsg {
		t.Errorf("expected error message %q, got %q", expectedMsg, err.Error())
	}
}

func TestApiRun_TokenResolutionErrorSurfaced(t *testing.T) {
	t.Setenv("GH_TOKEN", "")
	t.Setenv("GITHUB_TOKEN", "")
	t.Setenv("GH_ENTERPRISE_TOKEN", "")

	opts := &ApiOptions{
		Endpoint: "user",
		TokenForHost: func(host string) (string, error) {
			return "", errors.New("keyring locked: access denied")
		},
	}

	err := ApiRun(opts)
	if err == nil {
		t.Fatal("expected error when token resolution fails, got nil")
	}

	if !strings.Contains(err.Error(), "failed to get authentication token for host github.com") {
		t.Errorf("expected token resolution error prefix, got: %s", err.Error())
	}
	if !strings.Contains(err.Error(), "keyring locked: access denied") {
		t.Errorf("expected underlying error details, got: %s", err.Error())
	}
}

func TestApiRun_EnterpriseHostMissingToken(t *testing.T) {
	t.Setenv("GH_TOKEN", "")
	t.Setenv("GITHUB_TOKEN", "")
	t.Setenv("GH_ENTERPRISE_TOKEN", "")

	opts := &ApiOptions{
		Endpoint: "https://ghe.mycompany.com/api/v3/user",
		TokenForHost: func(host string) (string, error) {
			return "", nil
		},
	}

	err := ApiRun(opts)
	if err == nil {
		t.Fatal("expected error for missing enterprise token, got nil")
	}

	expectedMsg := "no authentication token found for host ghe.mycompany.com. To authenticate, run 'gh auth login -h ghe.mycompany.com'"
	if err.Error() != expectedMsg {
		t.Errorf("expected %q, got %q", expectedMsg, err.Error())
	}
}

func TestApiRun_ValidTokenSendsAuthenticatedRequest(t *testing.T) {
	t.Setenv("GH_TOKEN", "")
	t.Setenv("GITHUB_TOKEN", "")
	t.Setenv("GH_ENTERPRISE_TOKEN", "")

	client := &mockHTTPClient{
		resp: &http.Response{
			StatusCode: http.StatusOK,
			Body:       io.NopCloser(strings.NewReader(`{"login": "octocat"}`)),
		},
	}

	var out bytes.Buffer
	opts := &ApiOptions{
		IO:       &out,
		Endpoint: "user",
		HttpClient: func() (HTTPClient, error) {
			return client, nil
		},
		TokenForHost: func(host string) (string, error) {
			return "gho_secret_token_123", nil
		},
	}

	err := ApiRun(opts)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if len(client.reqs) != 1 {
		t.Fatalf("expected 1 HTTP request, got %d", len(client.reqs))
	}

	authHeader := client.reqs[0].Header.Get("Authorization")
	if authHeader != "token gho_secret_token_123" {
		t.Errorf("expected Authorization header 'token gho_secret_token_123', got: %s", authHeader)
	}

	if out.String() != `{"login": "octocat"}` {
		t.Errorf("unexpected output: %s", out.String())
	}
}
