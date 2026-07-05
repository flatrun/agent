package api

import "testing"

func TestPluginToolNameRoundTrip(t *testing.T) {
	name := pluginToolName("observability", "get_deployment_health")
	if name != "plugin__observability__get_deployment_health" {
		t.Fatalf("unexpected namespaced name %q", name)
	}
	plugin, tool, ok := parsePluginToolName(name)
	if !ok || plugin != "observability" || tool != "get_deployment_health" {
		t.Errorf("parse = (%q, %q, %v), want observability/get_deployment_health/true", plugin, tool, ok)
	}
}

func TestParsePluginToolNameRejectsBuiltins(t *testing.T) {
	if _, _, ok := parsePluginToolName("get_instance_info"); ok {
		t.Error("a built-in tool name must not parse as a plugin tool")
	}
}
