package cluster

import (
	"errors"
	"testing"
)

func TestFleetHealthEventOpensAndResolvesOneNodeIncident(t *testing.T) {
	failed := fleetHealthEvent("prod2", false, false, errors.New("connection refused"))
	if failed == nil || failed.Type != "node.unavailable" || failed.CorrelationKey != "node:prod2" || failed.Resolved {
		t.Fatalf("failed event = %#v", failed)
	}
	if repeated := fleetHealthEvent("prod2", true, false, errors.New("connection refused")); repeated != nil {
		t.Fatalf("repeated event = %#v", repeated)
	}
	recovered := fleetHealthEvent("prod2", true, false, nil)
	if recovered == nil || recovered.Type != "node.recovered" || recovered.CorrelationKey != failed.CorrelationKey || !recovered.Resolved {
		t.Fatalf("recovered event = %#v", recovered)
	}
}

func TestFleetHealthEventDoesNotAnnounceInitialSuccess(t *testing.T) {
	if event := fleetHealthEvent("prod2", false, false, nil); event != nil {
		t.Fatalf("event = %#v", event)
	}
}
