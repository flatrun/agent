package e2e

import (
	"bytes"
	"crypto/tls"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"time"
)

var (
	baseURL   = getEnv("FLATRUN_API_URL", "http://localhost:8090/api")
	authToken = getEnv("FLATRUN_AUTH_TOKEN", "")
)

func getEnv(key, fallback string) string {
	if value := os.Getenv(key); value != "" {
		return value
	}
	return fallback
}

type APIClient struct {
	BaseURL    string
	AuthToken  string
	HTTPClient *http.Client
}

func NewAPIClient() *APIClient {
	return &APIClient{
		BaseURL:   baseURL,
		AuthToken: authToken,
		HTTPClient: &http.Client{
			Timeout: 30 * time.Second,
		},
	}
}

func (c *APIClient) doRequest(method, path string, body interface{}) (*http.Response, error) {
	var reqBody io.Reader
	if body != nil {
		jsonData, err := json.Marshal(body)
		if err != nil {
			return nil, err
		}
		reqBody = bytes.NewBuffer(jsonData)
	}

	req, err := http.NewRequest(method, c.BaseURL+path, reqBody)
	if err != nil {
		return nil, err
	}

	req.Header.Set("Content-Type", "application/json")
	if c.AuthToken != "" {
		req.Header.Set("Authorization", "Bearer "+c.AuthToken)
	}

	return c.HTTPClient.Do(req)
}

func (c *APIClient) Get(path string) (*http.Response, error) {
	return c.doRequest("GET", path, nil)
}

func (c *APIClient) Post(path string, body interface{}) (*http.Response, error) {
	return c.doRequest("POST", path, body)
}

func (c *APIClient) Delete(path string) (*http.Response, error) {
	return c.doRequest("DELETE", path, nil)
}

type CreateDeploymentRequest struct {
	Name              string           `json:"name"`
	ComposeContent    string           `json:"compose_content"`
	TemplateID        string           `json:"template_id,omitempty"`
	Metadata          *ServiceMetadata `json:"metadata,omitempty"`
	EnvVars           []EnvVar         `json:"env_vars,omitempty"`
	AutoStart         bool             `json:"auto_start"`
	UseSharedDatabase bool             `json:"use_shared_database"`
}

type ServiceMetadata struct {
	Name        string            `json:"name"`
	Type        string            `json:"type"`
	Networking  NetworkingConfig  `json:"networking"`
	SSL         SSLConfig         `json:"ssl"`
	HealthCheck HealthCheckConfig `json:"healthcheck,omitempty"`
}

type NetworkingConfig struct {
	Expose        bool   `json:"expose"`
	Domain        string `json:"domain"`
	ContainerPort int    `json:"container_port"`
	Protocol      string `json:"protocol"`
	ProxyType     string `json:"proxy_type,omitempty"`
}

type SSLConfig struct {
	Enabled  bool `json:"enabled"`
	AutoCert bool `json:"auto_cert"`
}

type HealthCheckConfig struct {
	Path     string `json:"path,omitempty"`
	Interval string `json:"interval,omitempty"`
	Timeout  string `json:"timeout,omitempty"`
}

type EnvVar struct {
	Key   string `json:"key"`
	Value string `json:"value"`
}

type Template struct {
	ID          string `json:"id"`
	Name        string `json:"name"`
	Description string `json:"description"`
	Priority    int    `json:"priority"`
	Content     string `json:"content"`
}

type TemplatesResponse struct {
	Templates []Template `json:"templates"`
}

type DeploymentResponse struct {
	Message     string       `json:"message"`
	Name        string       `json:"name"`
	ProxyResult *ProxyResult `json:"proxy_result,omitempty"`
	AutoStarted bool         `json:"auto_started"`
	StartOutput string       `json:"start_output,omitempty"`
	StartError  string       `json:"start_error,omitempty"`
}

type ProxyResult struct {
	DeploymentName       string `json:"deployment_name"`
	Domain               string `json:"domain,omitempty"`
	Success              bool   `json:"success"`
	Skipped              bool   `json:"skipped"`
	Message              string `json:"message,omitempty"`
	VirtualHostCreated   bool   `json:"virtual_host_created"`
	NginxReloaded        bool   `json:"nginx_reloaded"`
	CertificateRequested bool   `json:"certificate_requested"`
	CertificateExists    bool   `json:"certificate_exists"`
	SSLMessage           string `json:"ssl_message,omitempty"`
	SSLError             string `json:"ssl_error,omitempty"`
}

func (c *APIClient) CreateDeployment(req CreateDeploymentRequest) (*DeploymentResponse, error) {
	resp, err := c.Post("/deployments", req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusCreated {
		body, _ := io.ReadAll(resp.Body)
		return nil, fmt.Errorf("failed to create deployment: %s - %s", resp.Status, string(body))
	}

	var result DeploymentResponse
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return nil, err
	}

	return &result, nil
}

func (c *APIClient) DeleteDeployment(name string) error {
	resp, err := c.Delete("/deployments/" + name)
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK && resp.StatusCode != http.StatusNoContent {
		body, _ := io.ReadAll(resp.Body)
		return fmt.Errorf("failed to delete deployment: %s - %s", resp.Status, string(body))
	}

	return nil
}

func (c *APIClient) GetTemplates() (*TemplatesResponse, error) {
	resp, err := c.Get("/templates")
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		return nil, fmt.Errorf("failed to get templates: %s - %s", resp.Status, string(body))
	}

	var result TemplatesResponse
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return nil, err
	}

	return &result, nil
}

func (c *APIClient) RefreshTemplates() error {
	resp, err := c.Post("/templates/refresh", nil)
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		return fmt.Errorf("failed to refresh templates: %s - %s", resp.Status, string(body))
	}

	return nil
}

func (c *APIClient) StartDeployment(name string) error {
	resp, err := c.Post("/deployments/"+name+"/start", nil)
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		return fmt.Errorf("failed to start deployment: %s - %s", resp.Status, string(body))
	}

	return nil
}

func (c *APIClient) StopDeployment(name string) error {
	resp, err := c.Post("/deployments/"+name+"/stop", nil)
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		return fmt.Errorf("failed to stop deployment: %s - %s", resp.Status, string(body))
	}

	return nil
}

func WaitForHTTP(url string, timeout time.Duration) error {
	client := &http.Client{Timeout: 5 * time.Second}
	deadline := time.Now().Add(timeout)

	for time.Now().Before(deadline) {
		resp, err := client.Get(url)
		if err == nil {
			resp.Body.Close()
			if resp.StatusCode >= 200 && resp.StatusCode < 500 {
				return nil
			}
		}
		time.Sleep(1 * time.Second)
	}

	return fmt.Errorf("timeout waiting for %s", url)
}

func HTTPGet(url string) (int, string, error) {
	client := &http.Client{Timeout: 10 * time.Second}
	resp, err := client.Get(url)
	if err != nil {
		return 0, "", err
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return resp.StatusCode, "", err
	}

	return resp.StatusCode, string(body), nil
}

func GenerateTestName(prefix string) string {
	return fmt.Sprintf("%s-%d", prefix, time.Now().UnixNano()%100000)
}

func GetNginxHTTPURL(domain string) string {
	return "http://localhost:18080"
}

func GetNginxHTTPSURL(domain string) string {
	return "https://localhost:18443"
}

func HTTPGetWithHost(url, host string) (int, string, error) {
	client := &http.Client{
		Timeout: 10 * time.Second,
		Transport: &http.Transport{
			TLSClientConfig: &tls.Config{InsecureSkipVerify: true},
		},
	}

	req, err := http.NewRequest("GET", url, nil)
	if err != nil {
		return 0, "", err
	}
	req.Host = host

	resp, err := client.Do(req)
	if err != nil {
		return 0, "", err
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return resp.StatusCode, "", err
	}

	return resp.StatusCode, string(body), nil
}

func WaitForHTTPWithHost(url, host string, timeout time.Duration) error {
	client := &http.Client{
		Timeout: 5 * time.Second,
		Transport: &http.Transport{
			TLSClientConfig: &tls.Config{InsecureSkipVerify: true},
		},
	}
	deadline := time.Now().Add(timeout)

	for time.Now().Before(deadline) {
		req, err := http.NewRequest("GET", url, nil)
		if err != nil {
			time.Sleep(1 * time.Second)
			continue
		}
		req.Host = host

		resp, err := client.Do(req)
		if err == nil {
			resp.Body.Close()
			if resp.StatusCode >= 200 && resp.StatusCode < 500 {
				return nil
			}
		}
		time.Sleep(1 * time.Second)
	}

	return fmt.Errorf("timeout waiting for %s (host: %s)", url, host)
}

func GenerateCertForDomain(domain string) error {
	certsPath := getEnv("FLATRUN_CERTS_PATH", "/tmp/flatrun-e2e-deployments/nginx/certs")
	certDir := filepath.Join(certsPath, "live", domain)

	if err := os.MkdirAll(certDir, 0755); err != nil {
		return err
	}

	cmd := exec.Command("openssl", "req", "-x509", "-nodes", "-days", "365",
		"-newkey", "rsa:2048",
		"-keyout", filepath.Join(certDir, "privkey.pem"),
		"-out", filepath.Join(certDir, "fullchain.pem"),
		"-subj", fmt.Sprintf("/CN=%s", domain),
		"-addext", fmt.Sprintf("subjectAltName=DNS:%s,DNS:*.%s", domain, domain))

	return cmd.Run()
}

func ReloadNginx() error {
	cmd := exec.Command("docker", "exec", "flatrun-e2e-nginx", "nginx", "-s", "reload")
	return cmd.Run()
}

func WaitForContainerHTTP(containerName string, port int, timeout time.Duration) error {
	deadline := time.Now().Add(timeout)
	url := fmt.Sprintf("http://%s:%d/", containerName, port)

	for time.Now().Before(deadline) {
		cmd := exec.Command("docker", "exec", "flatrun-e2e-nginx",
			"wget", "-q", "-O", "/dev/null", "--timeout=5", url)
		if err := cmd.Run(); err == nil {
			return nil
		}
		time.Sleep(2 * time.Second)
	}

	return fmt.Errorf("timeout waiting for container %s:%d", containerName, port)
}

func ContainerHTTPGet(containerName string, port int) (int, string, error) {
	url := fmt.Sprintf("http://%s:%d/", containerName, port)
	cmd := exec.Command("docker", "exec", "flatrun-e2e-nginx",
		"wget", "-q", "-O", "-", "--timeout=10", url)

	output, err := cmd.Output()
	if err != nil {
		if exitErr, ok := err.(*exec.ExitError); ok {
			return exitErr.ExitCode(), string(output), err
		}
		return 0, "", err
	}

	return 0, string(output), nil
}

func WaitForNginxProxy(containerName string, domain string, timeout time.Duration) error {
	deadline := time.Now().Add(timeout)

	for time.Now().Before(deadline) {
		cmd := exec.Command("docker", "exec", "flatrun-e2e-nginx",
			"sh", "-c",
			fmt.Sprintf("wget -q -O /dev/null --timeout=5 --header 'Host: %s' http://127.0.0.1/", domain))
		if err := cmd.Run(); err == nil {
			return nil
		}
		time.Sleep(2 * time.Second)
	}

	return fmt.Errorf("timeout waiting for nginx proxy to %s (domain: %s)", containerName, domain)
}

func NginxConfigExists(deploymentName string) bool {
	deploymentsPath := getEnv("FLATRUN_DEPLOYMENTS_PATH", "/tmp/flatrun-e2e-deployments")
	configPath := filepath.Join(deploymentsPath, "nginx", "conf.d", deploymentName+".conf")
	_, err := os.Stat(configPath)
	return err == nil
}

func ReadNginxConfig(deploymentName string) (string, error) {
	deploymentsPath := getEnv("FLATRUN_DEPLOYMENTS_PATH", "/tmp/flatrun-e2e-deployments")
	configPath := filepath.Join(deploymentsPath, "nginx", "conf.d", deploymentName+".conf")
	data, err := os.ReadFile(configPath)
	if err != nil {
		return "", err
	}
	return string(data), nil
}

func CertificateExists(domain string) bool {
	certsPath := getEnv("FLATRUN_CERTS_PATH", "/tmp/flatrun-e2e-deployments/nginx/certs")
	certPath := filepath.Join(certsPath, "live", domain, "fullchain.pem")
	_, err := os.Stat(certPath)
	return err == nil
}
