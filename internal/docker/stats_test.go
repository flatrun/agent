package docker

import "testing"

func TestParseContainerDeploymentLabelsPrefersManagedWorkload(t *testing.T) {
	labels := parseContainerDeploymentLabels("abc|shop.1.task|legacy|shop\ndef|db|database|\n")
	if labels["abc"] != "shop" || labels["shop.1.task"] != "shop" {
		t.Fatalf("managed labels = %#v", labels)
	}
	if labels["def"] != "database" || labels["db"] != "database" {
		t.Fatalf("Compose labels = %#v", labels)
	}
}
