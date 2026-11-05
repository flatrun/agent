package runtimes

import (
	"archive/tar"
	"context"
	"errors"
	"io"
	"os"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
)

const testImage = "busybox:latest"

func dockerRuntime(t *testing.T) *Docker {
	t.Helper()
	if testing.Short() {
		t.Skip("needs a Docker daemon")
	}
	d, err := NewDocker()
	if err != nil {
		t.Skipf("docker client: %v", err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := d.Available(ctx); err != nil {
		t.Skip(err)
	}
	d.Scope = uuid.NewString()
	d.EgressSubnet = os.Getenv("FLATRUN_TEST_EGRESS_SUBNET")
	return d
}

func create(t *testing.T, d *Docker, spec Spec) Instance {
	t.Helper()
	inst, err := d.Create(context.Background(), spec)
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	t.Cleanup(func() { _ = inst.Destroy(context.Background()) })
	return inst
}

func assertGone(t *testing.T, d *Docker, id string) {
	t.Helper()
	left, err := d.Leftovers(context.Background())
	if err != nil {
		t.Fatalf("leftovers: %v", err)
	}
	if slices.Contains(left, id) {
		t.Fatalf("instance %s left a container behind", id)
	}
}

func TestDockerRunToCompletion(t *testing.T) {
	d := dockerRuntime(t)
	ctx := context.Background()

	inst := create(t, d, Spec{
		Image:   testImage,
		Command: []string{"sh", "-c", `mkdir -p /out && echo "$GREETING" > /out/report.txt && echo started && echo oops >&2`},
		Env:     map[string]string{"GREETING": "hello from the run"},
	})
	if err := inst.Start(ctx); err != nil {
		t.Fatalf("start: %v", err)
	}
	exit, err := inst.Wait(ctx)
	if err != nil || exit.Code != 0 {
		t.Fatalf("wait: exit %d, err %v", exit.Code, err)
	}

	logs, err := inst.Logs(ctx, false)
	if err != nil {
		t.Fatalf("logs: %v", err)
	}
	out, _ := io.ReadAll(logs)
	logs.Close()
	if !strings.Contains(string(out), "started") || !strings.Contains(string(out), "oops") {
		t.Fatalf("logs missing stdout or stderr: %q", out)
	}

	stream, err := inst.CopyOut(ctx, "/out/report.txt")
	if err != nil {
		t.Fatalf("copy out: %v", err)
	}
	defer stream.Close()
	tr := tar.NewReader(stream)
	hdr, err := tr.Next()
	if err != nil {
		t.Fatalf("read tar: %v", err)
	}
	body, _ := io.ReadAll(tr)
	if hdr.Name != "report.txt" || strings.TrimSpace(string(body)) != "hello from the run" {
		t.Fatalf("artifact %q = %q", hdr.Name, body)
	}

	if err := inst.Destroy(ctx); err != nil {
		t.Fatalf("destroy: %v", err)
	}
	if err := inst.Destroy(ctx); err != nil {
		t.Fatalf("second destroy: %v", err)
	}
	assertGone(t, d, inst.ID())
}

func TestDockerNonZeroExit(t *testing.T) {
	d := dockerRuntime(t)
	ctx := context.Background()

	inst := create(t, d, Spec{Image: testImage, Command: []string{"sh", "-c", "exit 3"}})
	if err := inst.Start(ctx); err != nil {
		t.Fatalf("start: %v", err)
	}
	exit, err := inst.Wait(ctx)
	if err != nil || exit.Code != 3 {
		t.Fatalf("wait: exit %d, err %v", exit.Code, err)
	}
}

func TestDockerStopEndsLongRunningInstance(t *testing.T) {
	d := dockerRuntime(t)
	ctx := context.Background()

	inst := create(t, d, Spec{Image: testImage, Command: []string{"sleep", "300"}})
	if err := inst.Start(ctx); err != nil {
		t.Fatalf("start: %v", err)
	}
	done := make(chan Exit, 1)
	go func() {
		exit, _ := inst.Wait(ctx)
		done <- exit
	}()

	if err := inst.Stop(ctx, time.Second); err != nil {
		t.Fatalf("stop: %v", err)
	}
	select {
	case exit := <-done:
		if exit.Code == 0 {
			t.Fatalf("stopped instance reported success")
		}
	case <-time.After(15 * time.Second):
		t.Fatal("instance still running after stop")
	}
}

func TestDockerNetworkDeniedByDefault(t *testing.T) {
	d := dockerRuntime(t)
	ctx := context.Background()

	inst := create(t, d, Spec{Image: testImage, Command: []string{"sh", "-c", "ls /sys/class/net"}})
	if err := inst.Start(ctx); err != nil {
		t.Fatalf("start: %v", err)
	}
	if _, err := inst.Wait(ctx); err != nil {
		t.Fatalf("wait: %v", err)
	}
	logs, err := inst.Logs(ctx, false)
	if err != nil {
		t.Fatalf("logs: %v", err)
	}
	out, _ := io.ReadAll(logs)
	logs.Close()
	if ifaces := strings.Fields(string(out)); !slices.Equal(ifaces, []string{"lo"}) {
		t.Fatalf("interfaces = %v, want only lo", ifaces)
	}
}

// egress creates an instance on the shared egress network. A daemon whose
// address pools are all taken cannot create that network unless
// FLATRUN_TEST_EGRESS_SUBNET names a free CIDR, so the test is skipped.
func egress(t *testing.T, d *Docker, command ...string) Instance {
	t.Helper()
	inst, err := d.Create(context.Background(), Spec{Image: testImage, Command: command, Network: Network{Egress: true}})
	if err != nil && strings.Contains(err.Error(), "fully subnetted") {
		t.Skipf("no free address pool on this daemon: %v", err)
	}
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	t.Cleanup(func() { _ = inst.Destroy(context.Background()) })
	return inst
}

func TestDockerEgressInstancesCannotReachEachOther(t *testing.T) {
	d := dockerRuntime(t)
	ctx := context.Background()

	target := egress(t, d, "sleep", "300")
	if err := target.Start(ctx); err != nil {
		t.Fatalf("start target: %v", err)
	}
	info, err := d.cli.ContainerInspect(ctx, target.(*dockerInstance).container)
	if err != nil {
		t.Fatalf("inspect target: %v", err)
	}
	addr := info.NetworkSettings.Networks[EgressNetwork].IPAddress
	if addr == "" {
		t.Fatal("target has no address on the egress network")
	}

	probe := egress(t, d, "ping", "-c", "1", "-W", "2", addr)
	if err := probe.Start(ctx); err != nil {
		t.Fatalf("start probe: %v", err)
	}
	exit, err := probe.Wait(ctx)
	if err != nil {
		t.Fatalf("wait probe: %v", err)
	}
	if exit.Code == 0 {
		t.Fatalf("probe reached %s on the shared egress network", addr)
	}
}

func TestDockerDestroyByIDRemovesOrphans(t *testing.T) {
	d := dockerRuntime(t)
	ctx := context.Background()

	inst := create(t, d, Spec{Image: testImage, Command: []string{"sleep", "300"}})
	if err := inst.Start(ctx); err != nil {
		t.Fatalf("start: %v", err)
	}
	left, err := d.Leftovers(ctx)
	if err != nil || !slices.Contains(left, inst.ID()) {
		t.Fatalf("leftovers %v (err %v) missing live instance", left, err)
	}
	if err := d.DestroyByID(ctx, inst.ID()); err != nil {
		t.Fatalf("destroy by id: %v", err)
	}
	assertGone(t, d, inst.ID())
}

func TestDockerCreateRejectsMissingImage(t *testing.T) {
	d := dockerRuntime(t)
	if _, err := d.Create(context.Background(), Spec{}); err == nil {
		t.Fatal("create without image succeeded")
	} else if errors.Is(err, ErrUnavailable) {
		t.Fatalf("unexpected unavailable error: %v", err)
	}
}

func TestDockerCleanupIsScoped(t *testing.T) {
	first := dockerRuntime(t)
	second := dockerRuntime(t)
	ctx := context.Background()
	firstInstance := create(t, first, Spec{Image: testImage, Command: []string{"sleep", "30"}})
	secondInstance := create(t, second, Spec{
		Image:   testImage,
		Command: []string{"sleep", "30"},
		Labels:  map[string]string{ScopeLabel: first.Scope},
	})
	for _, instance := range []Instance{firstInstance, secondInstance} {
		if err := instance.Start(ctx); err != nil {
			t.Fatal(err)
		}
	}
	leftovers, err := first.Leftovers(ctx)
	if err != nil || !slices.Equal(leftovers, []string{firstInstance.ID()}) {
		t.Fatalf("first scope leftovers: %v %v", leftovers, err)
	}
	if err := first.DestroyByID(ctx, secondInstance.ID()); err != nil {
		t.Fatal(err)
	}
	for _, id := range leftovers {
		if err := first.DestroyByID(ctx, id); err != nil {
			t.Fatal(err)
		}
	}
	assertGone(t, first, firstInstance.ID())
	info, err := second.cli.ContainerInspect(ctx, secondInstance.(*dockerInstance).container)
	if err != nil || !info.State.Running {
		t.Fatalf("cleanup stopped the other scope's instance: %v %v", info.State, err)
	}
	leftovers, err = second.Leftovers(ctx)
	if err != nil || !slices.Equal(leftovers, []string{secondInstance.ID()}) {
		t.Fatalf("second scope leftovers: %v %v", leftovers, err)
	}
}

func TestDecodeImagePullError(t *testing.T) {
	stream := strings.NewReader("{\"status\":\"Pulling from library/busybox\"}\n{\"errorDetail\":{\"message\":\"manifest unknown\"},\"error\":\"manifest unknown\"}\n")
	if err := decodeImagePull(stream); err == nil || err.Error() != "manifest unknown" {
		t.Fatalf("pull error = %v", err)
	}
}
