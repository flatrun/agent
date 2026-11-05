package runtimes

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
)

func assertIngressBeforeReturns(t *testing.T, rules []string, taps []string) {
	t.Helper()
	firstReturn := len(rules)
	for n, rule := range rules {
		if (strings.Contains(rule, "ESTABLISHED,RELATED") || strings.Contains(rule, "RELATED,ESTABLISHED")) && n < firstReturn {
			firstReturn = n
		}
	}
	if firstReturn == len(rules) {
		t.Fatal("missing return accept")
	}
	for _, tap := range taps {
		found := false
		for n, rule := range rules {
			if strings.Contains(rule, "-i "+tap+" -j FR") {
				found = true
				if n >= firstReturn {
					t.Fatalf("ingress follows return accept: %v", rules)
				}
			}
		}
		if !found {
			t.Fatalf("missing ingress for %s: %v", tap, rules)
		}
	}
}

func TestNetworkRuleOrdering(t *testing.T) {
	runtime := NewFirecracker(FirecrackerConfig{}, nil, "ordering")
	instances := []*firecrackerInstance{{runtime: runtime, id: uuid.NewString()}, {runtime: runtime, id: uuid.NewString()}}
	for _, order := range [][]int{{0, 1}, {1, 0}} {
		var forward []string
		for _, index := range order {
			instance := instances[index]
			for _, rule := range instance.networkRules("-I") {
				if rule[1] != "FORWARD" {
					continue
				}
				value := strings.Join(rule[1:], " ")
				if rule[0] == "-I" {
					forward = append([]string{value}, forward...)
				} else {
					forward = append(forward, value)
				}
			}
			for _, rule := range instance.networkRules("-D") {
				if slices.Contains(rule, "-I") || slices.Contains(rule, "-A") {
					t.Fatalf("invalid deletion: %v", rule)
				}
			}
		}
		assertIngressBeforeReturns(t, forward, []string{instances[0].tap(), instances[1].tap()})
	}
}

func TestRootFirecrackerNetworkOrdering(t *testing.T) {
	kernel, binary := os.Getenv("FLATRUN_FC_KERNEL"), os.Getenv("FLATRUN_FC_BIN")
	if kernel == "" || binary == "" {
		t.Skip("requires FLATRUN_FC_KERNEL and FLATRUN_FC_BIN")
	}
	if _, err := os.Stat("/dev/kvm"); err != nil {
		t.Skip("requires /dev/kvm")
	}
	if os.Geteuid() != 0 {
		t.Skip("requires root")
	}
	init := filepath.Join(t.TempDir(), "flatrun-guest-init")
	build := exec.Command("go", "build", "-buildvcs=false", "-o", init, "../../cmd/guest-init")
	build.Env = append(os.Environ(), "CGO_ENABLED=0")
	if output, err := build.CombinedOutput(); err != nil {
		t.Fatalf("build init: %s %v", output, err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	docker, err := NewDocker()
	if err != nil {
		t.Fatal(err)
	}
	runtime := NewFirecracker(FirecrackerConfig{Binary: binary, Jailer: filepath.Join(filepath.Dir(binary), "jailer"), Kernel: kernel, GuestInit: init, StateDir: t.TempDir()}, docker, uuid.NewString())
	if err := runtime.Available(ctx); err != nil {
		t.Fatal(err)
	}
	var taps []string
	for range 2 {
		instance, err := runtime.Create(ctx, Spec{Image: "busybox:latest", Command: []string{"sleep", "120"}, Network: Network{Egress: true}})
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() {
			if err := instance.Destroy(context.Background()); err != nil {
				t.Error(err)
			}
		})
		if err := instance.Start(ctx); err != nil {
			t.Fatal(err)
		}
		taps = append(taps, instance.(*firecrackerInstance).tap())
	}
	output, err := exec.CommandContext(ctx, "iptables", "-S", "FORWARD").CombinedOutput()
	if err != nil {
		t.Fatalf("iptables: %v: %s", err, output)
	}
	assertIngressBeforeReturns(t, strings.Split(string(output), "\n"), taps)
	for _, tap := range taps {
		chain := "FR" + strings.TrimPrefix(tap, "fr")
		output, err := exec.CommandContext(ctx, "iptables", "-S", chain).CombinedOutput()
		if err != nil {
			t.Fatalf("iptables chain: %v: %s", err, output)
		}
		rules := strings.Split(strings.TrimSpace(string(output)), "\n")
		if len(rules) < 3 || !strings.Contains(rules[1], "! -s ") || !strings.HasSuffix(rules[1], "-j DROP") {
			t.Fatalf("missing first source check: %s", output)
		}
		if strings.Contains(string(output), "ESTABLISHED,RELATED") {
			t.Fatalf("return accept in ingress chain: %s", output)
		}
	}
}
