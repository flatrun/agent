package secrets

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"sort"
	"strings"
	"time"
)

// Field describes one setting a provider needs, either on its connection
// (credentials) or on a binding (where to read from).
type Field struct {
	Key      string `json:"key"`
	Label    string `json:"label"`
	Secret   bool   `json:"secret,omitempty"`
	Required bool   `json:"required,omitempty"`
	Default  string `json:"default,omitempty"`
}

// Provider reads secrets from an external manager. Credentials hold the
// connection's settings; options say which secrets the binding reads.
type Provider interface {
	Type() string
	Label() string
	ConnectionFields() []Field
	BindingFields() []Field
	Fetch(ctx context.Context, credentials, options map[string]string) (map[string]string, error)
}

type Registry struct {
	providers map[string]Provider
}

func NewRegistry(providers ...Provider) *Registry {
	r := &Registry{providers: map[string]Provider{}}
	for _, p := range providers {
		r.providers[p.Type()] = p
	}
	return r
}

// DefaultRegistry holds every built-in provider.
func DefaultRegistry(client *http.Client) *Registry {
	if client == nil {
		client = &http.Client{Timeout: 20 * time.Second}
	}
	return NewRegistry(
		Infisical{Client: client},
		Vault{Client: client},
		Doppler{Client: client},
		OnePassword{Client: client},
	)
}

func (r *Registry) Get(typ string) (Provider, bool) {
	p, ok := r.providers[typ]
	return p, ok
}

func (r *Registry) All() []Provider {
	out := make([]Provider, 0, len(r.providers))
	for _, p := range r.providers {
		out = append(out, p)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Type() < out[j].Type() })
	return out
}

// CheckFields reports the first required field missing from values.
func CheckFields(fields []Field, values map[string]string) error {
	for _, f := range fields {
		if f.Required && strings.TrimSpace(values[f.Key]) == "" && f.Default == "" {
			return fmt.Errorf("%s is required", f.Label)
		}
	}
	return nil
}

func valueOr(values map[string]string, key, fallback string) string {
	if v := strings.TrimSpace(values[key]); v != "" {
		return v
	}
	return fallback
}

func doJSON(client *http.Client, req *http.Request, out any) error {
	resp, err := client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(io.LimitReader(resp.Body, 8<<20))
	if err != nil {
		return err
	}
	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		return fmt.Errorf("%s %s returned %d: %s", req.Method, req.URL.Path, resp.StatusCode, strings.TrimSpace(string(body[:min(len(body), 200)])))
	}
	if err := json.Unmarshal(body, out); err != nil {
		return fmt.Errorf("unexpected response from %s: %w", req.URL.Path, err)
	}
	return nil
}

// Infisical reads one folder of a project environment with a machine
// identity's universal auth credentials.
type Infisical struct{ Client *http.Client }

func (Infisical) Type() string  { return "infisical" }
func (Infisical) Label() string { return "Infisical" }

func (Infisical) ConnectionFields() []Field {
	return []Field{
		{Key: "site_url", Label: "Site URL", Default: "https://app.infisical.com"},
		{Key: "client_id", Label: "Client ID", Required: true},
		{Key: "client_secret", Label: "Client secret", Secret: true, Required: true},
	}
}

func (Infisical) BindingFields() []Field {
	return []Field{
		{Key: "project_id", Label: "Project ID", Required: true},
		{Key: "environment", Label: "Environment slug", Required: true},
		{Key: "secret_path", Label: "Secret path", Default: "/"},
	}
}

func (p Infisical) Fetch(ctx context.Context, creds, opts map[string]string) (map[string]string, error) {
	base := strings.TrimRight(valueOr(creds, "site_url", "https://app.infisical.com"), "/")
	login, _ := json.Marshal(map[string]string{"clientId": creds["client_id"], "clientSecret": creds["client_secret"]})
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, base+"/api/v1/auth/universal-auth/login", bytes.NewReader(login))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	var auth struct {
		AccessToken string `json:"accessToken"`
	}
	if err := doJSON(p.Client, req, &auth); err != nil {
		return nil, fmt.Errorf("infisical login: %w", err)
	}

	q := url.Values{}
	q.Set("projectId", opts["project_id"])
	q.Set("environment", opts["environment"])
	q.Set("secretPath", valueOr(opts, "secret_path", "/"))
	q.Set("viewSecretValue", "true")
	req, err = http.NewRequestWithContext(ctx, http.MethodGet, base+"/api/v4/secrets?"+q.Encode(), nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", "Bearer "+auth.AccessToken)
	var list struct {
		Secrets []struct {
			SecretKey   string `json:"secretKey"`
			SecretValue string `json:"secretValue"`
		} `json:"secrets"`
	}
	if err := doJSON(p.Client, req, &list); err != nil {
		return nil, fmt.Errorf("infisical secrets: %w", err)
	}
	out := make(map[string]string, len(list.Secrets))
	for _, s := range list.Secrets {
		out[s.SecretKey] = s.SecretValue
	}
	return out, nil
}

// Vault reads one secret from a KV version 2 engine. OpenBao serves the same
// API.
type Vault struct{ Client *http.Client }

func (Vault) Type() string  { return "vault" }
func (Vault) Label() string { return "HashiCorp Vault / OpenBao" }

func (Vault) ConnectionFields() []Field {
	return []Field{
		{Key: "address", Label: "Address", Required: true},
		{Key: "token", Label: "Token", Secret: true, Required: true},
		{Key: "namespace", Label: "Namespace"},
	}
}

func (Vault) BindingFields() []Field {
	return []Field{
		{Key: "mount", Label: "KV mount", Default: "secret"},
		{Key: "path", Label: "Secret path", Required: true},
	}
}

func (p Vault) Fetch(ctx context.Context, creds, opts map[string]string) (map[string]string, error) {
	base := strings.TrimRight(creds["address"], "/")
	mount := strings.Trim(valueOr(opts, "mount", "secret"), "/")
	path := strings.Trim(opts["path"], "/")
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, base+"/v1/"+mount+"/data/"+path, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("X-Vault-Token", creds["token"])
	if ns := creds["namespace"]; ns != "" {
		req.Header.Set("X-Vault-Namespace", ns)
	}
	var body struct {
		Data struct {
			Data map[string]any `json:"data"`
		} `json:"data"`
	}
	if err := doJSON(p.Client, req, &body); err != nil {
		return nil, fmt.Errorf("vault: %w", err)
	}
	out := make(map[string]string, len(body.Data.Data))
	for k, v := range body.Data.Data {
		if s, ok := v.(string); ok {
			out[k] = s
			continue
		}
		raw, _ := json.Marshal(v)
		out[k] = string(raw)
	}
	return out, nil
}

// Doppler downloads a config's secrets. A service token already names its
// project and config, so those are only needed with other token types.
type Doppler struct{ Client *http.Client }

func (Doppler) Type() string  { return "doppler" }
func (Doppler) Label() string { return "Doppler" }

func (Doppler) ConnectionFields() []Field {
	return []Field{
		{Key: "token", Label: "Token", Secret: true, Required: true},
		{Key: "api_url", Label: "API URL", Default: "https://api.doppler.com"},
	}
}

func (Doppler) BindingFields() []Field {
	return []Field{
		{Key: "project", Label: "Project"},
		{Key: "config", Label: "Config"},
	}
}

func (p Doppler) Fetch(ctx context.Context, creds, opts map[string]string) (map[string]string, error) {
	base := strings.TrimRight(valueOr(creds, "api_url", "https://api.doppler.com"), "/")
	q := url.Values{}
	q.Set("format", "json")
	if v := opts["project"]; v != "" {
		q.Set("project", v)
	}
	if v := opts["config"]; v != "" {
		q.Set("config", v)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, base+"/v3/configs/config/secrets/download?"+q.Encode(), nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", "Bearer "+creds["token"])
	out := map[string]string{}
	if err := doJSON(p.Client, req, &out); err != nil {
		return nil, fmt.Errorf("doppler: %w", err)
	}
	return out, nil
}

// OnePassword reads one item's fields through a 1Password Connect server.
// Each field's label becomes a key.
type OnePassword struct{ Client *http.Client }

func (OnePassword) Type() string  { return "onepassword" }
func (OnePassword) Label() string { return "1Password Connect" }

func (OnePassword) ConnectionFields() []Field {
	return []Field{
		{Key: "connect_url", Label: "Connect server URL", Required: true},
		{Key: "token", Label: "Access token", Secret: true, Required: true},
	}
}

func (OnePassword) BindingFields() []Field {
	return []Field{
		{Key: "vault_id", Label: "Vault ID", Required: true},
		{Key: "item_id", Label: "Item ID", Required: true},
	}
}

func (p OnePassword) Fetch(ctx context.Context, creds, opts map[string]string) (map[string]string, error) {
	base := strings.TrimRight(creds["connect_url"], "/")
	path := "/v1/vaults/" + url.PathEscape(opts["vault_id"]) + "/items/" + url.PathEscape(opts["item_id"])
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, base+path, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", "Bearer "+creds["token"])
	var item struct {
		Fields []struct {
			Label string `json:"label"`
			Value string `json:"value"`
		} `json:"fields"`
	}
	if err := doJSON(p.Client, req, &item); err != nil {
		return nil, fmt.Errorf("1password: %w", err)
	}
	out := map[string]string{}
	for _, f := range item.Fields {
		if namePattern.MatchString(f.Label) && f.Value != "" {
			out[f.Label] = f.Value
		}
	}
	return out, nil
}
