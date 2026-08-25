package api

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"regexp"
	"strings"
	"time"
)

// AuthConfig interface abstracts token lookup from configuration or credential helpers.
type AuthConfig interface {
	TokenForHost(hostname string) (string, error)
}

// Config interface provides access to authentication configuration.
type Config interface {
	Authentication() AuthConfig
}

// HTTPClient is an interface for sending HTTP requests.
type HTTPClient interface {
	Do(req *http.Request) (*http.Response, error)
}

// ApiOptions contains options for the `api` command.
type ApiOptions struct {
	IO             io.Writer
	ErrIO          io.Writer
	Config         func() (Config, error)
	HttpClient     func() (HTTPClient, error)
	TokenForHost   func(string) (string, error)

	RequestMethod  string
	Endpoint       string
	Headers        []string
	RawFields      []string
	RequestRepo    string
	Hostname       string
	InputFile      string
	RawInput       string
	Silent         bool
	Paginate       bool
	Slurp          bool
	Template       string
	Cache          time.Duration
	PreviewNames   []string
	IncludeHeaders bool
	Verbose        bool
	Token          string
}

// DetermineHost inspects endpoint, request repo, and hostname option to determine the target host.
func DetermineHost(opts *ApiOptions) (string, error) {
	if opts.Hostname != "" {
		return opts.Hostname, nil
	}
	if strings.HasPrefix(opts.Endpoint, "http://") || strings.HasPrefix(opts.Endpoint, "https://") {
		u, err := url.Parse(opts.Endpoint)
		if err != nil {
			return "", fmt.Errorf("invalid endpoint URL: %w", err)
		}
		return u.Hostname(), nil
	}
	if opts.RequestRepo != "" {
		parts := strings.Split(opts.RequestRepo, "/")
		if len(parts) == 3 {
			return parts[0], nil
		}
	}
	return "github.com", nil
}

// ResolveToken retrieves the authentication token for the given hostname.
func ResolveToken(opts *ApiOptions, hostname string) (string, error) {
	if opts.Token != "" {
		return opts.Token, nil
	}

	// Check environment variables first
	if envToken := os.Getenv("GH_TOKEN"); envToken != "" {
		return envToken, nil
	}
	if envToken := os.Getenv("GITHUB_TOKEN"); envToken != "" {
		return envToken, nil
	}
	if envToken := os.Getenv("GH_ENTERPRISE_TOKEN"); envToken != "" && hostname != "github.com" {
		return envToken, nil
	}

	if opts.TokenForHost != nil {
		return opts.TokenForHost(hostname)
	}

	if opts.Config != nil {
		cfg, err := opts.Config()
		if err != nil {
			return "", fmt.Errorf("failed to load configuration: %w", err)
		}
		if cfg != nil && cfg.Authentication() != nil {
			return cfg.Authentication().TokenForHost(hostname)
		}
	}

	return "", nil
}

// ApiRun executes the API command with strict token verification.
func ApiRun(opts *ApiOptions) error {
	hostname, err := DetermineHost(opts)
	if err != nil {
		return err
	}

	token, err := ResolveToken(opts, hostname)
	if err != nil {
		return fmt.Errorf("failed to get authentication token for host %s: %w", hostname, err)
	}
	if token == "" {
		return fmt.Errorf("no authentication token found for host %s. To authenticate, run 'gh auth login -h %s'", hostname, hostname)
	}

	var httpClient HTTPClient
	if opts.HttpClient != nil {
		client, err := opts.HttpClient()
		if err != nil {
			return fmt.Errorf("failed to initialize HTTP client: %w", err)
		}
		httpClient = client
	} else {
		httpClient = http.DefaultClient
	}

	reqURL := opts.Endpoint
	if !strings.HasPrefix(reqURL, "http://") && !strings.HasPrefix(reqURL, "https://") {
		if hostname == "github.com" {
			reqURL = "https://api.github.com/" + strings.TrimPrefix(reqURL, "/")
		} else {
			reqURL = fmt.Sprintf("https://%s/api/v3/%s", hostname, strings.TrimPrefix(reqURL, "/"))
		}
	}

	method := opts.RequestMethod
	if method == "" {
		method = "GET"
	}

	var reqBody io.Reader
	if opts.RawInput != "" {
		reqBody = strings.NewReader(opts.RawInput)
	}

	req, err := http.NewRequestWithContext(context.Background(), method, reqURL, reqBody)
	if err != nil {
		return fmt.Errorf("failed to create HTTP request: %w", err)
	}

	req.Header.Set("Authorization", "token "+token)
	req.Header.Set("Accept", "application/vnd.github.v3+json")
	for _, header := range opts.Headers {
		parts := strings.SplitN(header, ":", 2)
		if len(parts) == 2 {
			req.Header.Set(strings.TrimSpace(parts[0]), strings.TrimSpace(parts[1]))
		}
	}

	resp, err := httpClient.Do(req)
	if err != nil {
		return fmt.Errorf("HTTP request failed: %w", err)
	}
	defer resp.Body.Close()

	bodyBytes, err := io.ReadAll(resp.Body)
	if err != nil {
		return fmt.Errorf("failed to read response body: %w", err)
	}

	if resp.StatusCode >= 400 {
		return fmt.Errorf("HTTP %d: %s (%s)", resp.StatusCode, http.StatusText(resp.StatusCode), string(bodyBytes))
	}

	if !opts.Silent && opts.IO != nil {
		_, err = opts.IO.Write(bodyBytes)
		if err != nil {
			return err
		}
	}

	return nil
}
