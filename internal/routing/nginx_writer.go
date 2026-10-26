package routing

import (
	"context"
	"fmt"
)

type NginxManager interface {
	ApplyVirtualHost(string, string) error
	RemoveVirtualHost(string) error
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
	if err := w.manager.ApplyVirtualHost(id, string(content)); err != nil {
		return fmt.Errorf("apply Nginx route: %w", err)
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
	if err := w.manager.RemoveVirtualHost(id); err != nil {
		return fmt.Errorf("remove Nginx route: %w", err)
	}
	return nil
}
