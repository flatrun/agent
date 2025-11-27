package system

import (
	"bytes"
	"fmt"
	"os/exec"
	"strings"
)

type Service struct {
	Name        string `json:"name"`
	Type        string `json:"type"`
	Description string `json:"description"`
	Load        string `json:"load"`
	Active      string `json:"active"`
	Sub         string `json:"sub"`
}

type ServicesManager struct{}

func NewServicesManager() *ServicesManager {
	return &ServicesManager{}
}

func (m *ServicesManager) ListServices() ([]Service, error) {
	cmd := exec.Command("systemctl", "list-units", "--type=service", "--all", "--no-pager", "--plain", "--no-legend")
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr

	if err := cmd.Run(); err != nil {
		return nil, fmt.Errorf("failed to list services: %s", stderr.String())
	}

	var services []Service
	lines := strings.Split(stdout.String(), "\n")

	for _, line := range lines {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}

		fields := strings.Fields(line)
		if len(fields) < 4 {
			continue
		}

		name := strings.TrimSuffix(fields[0], ".service")

		description := ""
		if len(fields) > 4 {
			description = strings.Join(fields[4:], " ")
		}

		services = append(services, Service{
			Name:        name,
			Type:        "service",
			Load:        fields[1],
			Active:      fields[2],
			Sub:         fields[3],
			Description: description,
		})
	}

	return services, nil
}

func (m *ServicesManager) GetService(name string) (*Service, error) {
	services, err := m.ListServices()
	if err != nil {
		return nil, err
	}

	for _, svc := range services {
		if svc.Name == name {
			return &svc, nil
		}
	}

	return nil, fmt.Errorf("service not found: %s", name)
}

func (m *ServicesManager) StartService(name string) error {
	cmd := exec.Command("systemctl", "start", name)
	var stderr bytes.Buffer
	cmd.Stderr = &stderr

	if err := cmd.Run(); err != nil {
		return fmt.Errorf("failed to start service: %s", stderr.String())
	}

	return nil
}

func (m *ServicesManager) StopService(name string) error {
	cmd := exec.Command("systemctl", "stop", name)
	var stderr bytes.Buffer
	cmd.Stderr = &stderr

	if err := cmd.Run(); err != nil {
		return fmt.Errorf("failed to stop service: %s", stderr.String())
	}

	return nil
}

func (m *ServicesManager) RestartService(name string) error {
	cmd := exec.Command("systemctl", "restart", name)
	var stderr bytes.Buffer
	cmd.Stderr = &stderr

	if err := cmd.Run(); err != nil {
		return fmt.Errorf("failed to restart service: %s", stderr.String())
	}

	return nil
}

func (m *ServicesManager) EnableService(name string) error {
	cmd := exec.Command("systemctl", "enable", name)
	var stderr bytes.Buffer
	cmd.Stderr = &stderr

	if err := cmd.Run(); err != nil {
		return fmt.Errorf("failed to enable service: %s", stderr.String())
	}

	return nil
}

func (m *ServicesManager) DisableService(name string) error {
	cmd := exec.Command("systemctl", "disable", name)
	var stderr bytes.Buffer
	cmd.Stderr = &stderr

	if err := cmd.Run(); err != nil {
		return fmt.Errorf("failed to disable service: %s", stderr.String())
	}

	return nil
}
