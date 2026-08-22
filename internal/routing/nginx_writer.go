package routing

import (
	"context"
	"fmt"
)

type NginxManager interface {
	WriteVirtualHost(string, string) error
	DeleteVirtualHost(string) error
	TestConfig() error
	Reload() error
}

type NginxWriter struct {
	manager NginxManager
}

func NewNginxWriter(manager NginxManager) *NginxWriter {
	return &NginxWriter{manager: manager}
}

func (w *NginxWriter) Apply(ctx context.Context, id, provider string, content []byte) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if provider != string(ProviderNginx) {
		return fmt.Errorf("Nginx writer cannot apply %q configuration", provider)
	}
	if !safeRouteID.MatchString(id) {
		return fmt.Errorf("Route ID is invalid")
	}
	if err := w.manager.WriteVirtualHost(id, string(content)); err != nil {
		return fmt.Errorf("write Nginx route: %w", err)
	}
	if err := w.manager.TestConfig(); err != nil {
		return fmt.Errorf("test Nginx configuration: %w", err)
	}
	if err := w.manager.Reload(); err != nil {
		return fmt.Errorf("reload Nginx: %w", err)
	}
	return nil
}

func (w *NginxWriter) Remove(ctx context.Context, id string) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if !safeRouteID.MatchString(id) {
		return fmt.Errorf("Route ID is invalid")
	}
	if err := w.manager.DeleteVirtualHost(id); err != nil {
		return fmt.Errorf("delete Nginx route: %w", err)
	}
	if err := w.manager.TestConfig(); err != nil {
		return fmt.Errorf("test Nginx configuration: %w", err)
	}
	if err := w.manager.Reload(); err != nil {
		return fmt.Errorf("reload Nginx: %w", err)
	}
	return nil
}
