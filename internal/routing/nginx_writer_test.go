package routing

import (
	"context"
	"testing"
)

type recordingNginx struct {
	written  string
	deleted  string
	tested   int
	reloaded int
}

func (m *recordingNginx) WriteVirtualHost(id, content string) error {
	m.written = id + ":" + content
	return nil
}

func (m *recordingNginx) DeleteVirtualHost(id string) error {
	m.deleted = id
	return nil
}

func (m *recordingNginx) TestConfig() error {
	m.tested++
	return nil
}

func (m *recordingNginx) Reload() error {
	m.reloaded++
	return nil
}

func TestNginxWriterAppliesAndReloadsRoute(t *testing.T) {
	manager := &recordingNginx{}
	writer := NewNginxWriter(manager)
	if err := writer.Apply(context.Background(), "shop", "nginx", []byte("upstream shop {}")); err != nil {
		t.Fatal(err)
	}
	if manager.written != "shop:upstream shop {}" || manager.tested != 1 || manager.reloaded != 1 {
		t.Fatalf("unexpected manager calls: %+v", manager)
	}
}

func TestNginxWriterRejectsUnsafeRouteID(t *testing.T) {
	manager := &recordingNginx{}
	writer := NewNginxWriter(manager)
	if err := writer.Apply(context.Background(), "../shop", "nginx", []byte("route")); err == nil {
		t.Fatal("expected invalid route error")
	}
	if manager.written != "" {
		t.Fatal("unsafe route was written")
	}
}
