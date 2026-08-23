package cluster

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"time"
)

type Client struct {
	baseURL    string
	apiKey     string
	httpClient *http.Client
}

func NewClient(baseURL, apiKey string, timeout time.Duration) *Client {
	return &Client{
		baseURL: baseURL,
		apiKey:  apiKey,
		httpClient: &http.Client{
			Timeout: timeout,
		},
	}
}

func (c *Client) Do(ctx context.Context, method, path string, body io.Reader) (*http.Response, error) {
	return c.DoWithHeaders(ctx, method, path, nil, body)
}

func (c *Client) DoWithHeaders(ctx context.Context, method, path string, headers http.Header, body io.Reader) (*http.Response, error) {
	reference, err := url.Parse(path)
	if err != nil {
		return nil, fmt.Errorf("invalid URL path %q: %w", path, err)
	}
	fullURL, err := url.JoinPath(c.baseURL, reference.Path)
	if err != nil {
		return nil, fmt.Errorf("invalid URL path %q: %w", path, err)
	}
	if reference.RawQuery != "" {
		fullURL += "?" + reference.RawQuery
	}

	req, err := http.NewRequestWithContext(ctx, method, fullURL, body)
	if err != nil {
		return nil, err
	}

	req.Header.Set("Authorization", "Bearer "+c.apiKey)
	copyForwardHeaders(req.Header, headers)
	if req.Header.Get("Content-Type") == "" {
		req.Header.Set("Content-Type", "application/json")
	}

	return c.httpClient.Do(req)
}

func copyForwardHeaders(destination, source http.Header) {
	for key, values := range source {
		switch http.CanonicalHeaderKey(key) {
		case "Authorization", "Connection", "Content-Length", "Host", "Proxy-Authorization", "Te", "Trailer", "Transfer-Encoding", "Upgrade":
			continue
		}
		for _, value := range values {
			destination.Add(key, value)
		}
	}
}

func (c *Client) Health(ctx context.Context) error {
	resp, err := c.Do(ctx, "GET", "/api/health", nil)
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("health check failed: status %d", resp.StatusCode)
	}
	return nil
}

func (c *Client) Get(ctx context.Context, path string) ([]byte, int, error) {
	resp, err := c.Do(ctx, "GET", path, nil)
	if err != nil {
		return nil, 0, err
	}
	defer resp.Body.Close()

	data, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, resp.StatusCode, err
	}
	return data, resp.StatusCode, nil
}

func (c *Client) Post(ctx context.Context, path string, body []byte) ([]byte, int, error) {
	var reader io.Reader
	if body != nil {
		reader = bytes.NewReader(body)
	}

	resp, err := c.Do(ctx, "POST", path, reader)
	if err != nil {
		return nil, 0, err
	}
	defer resp.Body.Close()

	data, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, resp.StatusCode, err
	}
	return data, resp.StatusCode, nil
}

func (c *Client) Forward(ctx context.Context, method, path string, body io.Reader) ([]byte, int, map[string]string, error) {
	resp, err := c.Do(ctx, method, path, body)
	if err != nil {
		return nil, 0, nil, err
	}
	defer resp.Body.Close()

	data, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, resp.StatusCode, nil, err
	}

	headers := make(map[string]string)
	for key := range resp.Header {
		headers[key] = resp.Header.Get(key)
	}

	return data, resp.StatusCode, headers, nil
}
