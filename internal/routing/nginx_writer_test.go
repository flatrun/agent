package routing

import (
	"context"
	"testing"
)

type recordingNginx struct {
	written string
	deleted string
	applied int
}

func (m *recordingNginx) ApplyVirtualHost(id, content string) error {
	m.written = id + ":" + content
	m.applied++
	return nil
}

func (m *recordingNginx) RemoveVirtualHost(id string) error {
	m.deleted = id
	return nil
}

func TestNginxWriterAppliesAndReloadsRoute(t *testing.T) {
	manager := &recordingNginx{}
	writer := NewNginxWriter(manager)
	if err := writer.Apply(context.Background(), "shop", "nginx", []byte("upstream shop {}")); err != nil {
		t.Fatal(err)
	}
	if manager.written != "shop:upstream shop {}" || manager.applied != 1 {
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
