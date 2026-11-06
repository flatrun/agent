package webhook

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
	"strings"
	"time"

	"github.com/golang-jwt/jwt/v5"
	"github.com/google/uuid"
)

type GitHubApp struct {
	AppID          int64
	PrivateKeyFile string
	BaseURL        string
	Client         *http.Client
}

func (a GitHubApp) JIT(ctx context.Context, e *Envelope, group int64) (string, error) {
	if e.InstallationID <= 0 || len(strings.Split(e.Subject, "/")) != 2 {
		return "", errors.New("missing installation or repository")
	}
	raw, err := os.ReadFile(a.PrivateKeyFile)
	if err != nil {
		return "", errors.New("cannot read GitHub App key")
	}
	key, err := jwt.ParseRSAPrivateKeyFromPEM(raw)
	if err != nil {
		return "", errors.New("invalid GitHub App key")
	}
	now := time.Now()
	token, err := jwt.NewWithClaims(jwt.SigningMethodRS256, jwt.MapClaims{"iat": now.Add(-time.Minute).Unix(), "exp": now.Add(9 * time.Minute).Unix(), "iss": fmt.Sprint(a.AppID)}).SignedString(key)
	if err != nil {
		return "", errors.New("cannot sign GitHub App JWT")
	}
	var installation struct {
		Token string `json:"token"`
	}
	if err := a.post(ctx, fmt.Sprintf("/app/installations/%d/access_tokens", e.InstallationID), token, map[string]any{"repositories": []string{strings.Split(e.Subject, "/")[1]}}, &installation); err != nil {
		return "", err
	}
	if installation.Token == "" {
		return "", errors.New("missing installation token")
	}
	var config struct {
		Encoded string `json:"encoded_jit_config"`
	}
	parts := strings.Split(e.Subject, "/")
	err = a.post(ctx, "/repos/"+url.PathEscape(parts[0])+"/"+url.PathEscape(parts[1])+"/actions/runners/generate-jitconfig", installation.Token, map[string]any{"name": "flatrun-" + uuid.NewString(), "runner_group_id": group, "labels": e.Labels, "work_folder": "_work"}, &config)
	if err != nil {
		return "", err
	}
	if config.Encoded == "" {
		return "", errors.New("missing JIT configuration")
	}
	return config.Encoded, nil
}

func (a GitHubApp) post(ctx context.Context, path, token string, body, output any) error {
	raw, err := json.Marshal(body)
	if err != nil {
		return errors.New("invalid GitHub request")
	}
	base := a.BaseURL
	if base == "" {
		base = "https://api.github.com"
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, base+path, bytes.NewReader(raw))
	if err != nil {
		return errors.New("invalid GitHub API URL")
	}
	request.Header.Set("Authorization", "Bearer "+token)
	request.Header.Set("Accept", "application/vnd.github+json")
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("X-GitHub-Api-Version", "2022-11-28")
	client := a.Client
	if client == nil {
		client = &http.Client{Timeout: 20 * time.Second, CheckRedirect: func(_ *http.Request, _ []*http.Request) error { return http.ErrUseLastResponse }}
	}
	response, err := client.Do(request)
	if err != nil {
		return errors.New("GitHub API request failed")
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusCreated {
		return fmt.Errorf("GitHub API returned %d", response.StatusCode)
	}
	if json.NewDecoder(io.LimitReader(response.Body, 2<<20)).Decode(output) != nil {
		return errors.New("invalid GitHub API response")
	}
	return nil
}
