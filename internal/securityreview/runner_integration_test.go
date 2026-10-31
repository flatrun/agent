package securityreview

import (
	"context"
	"encoding/json"
	"os"
	"testing"

	"github.com/flatrun/agent/pkg/securityprofile"
)

func TestCollectorImageIsolation(t *testing.T) {
	image := os.Getenv("FLATRUN_SECURITY_TEST_IMAGE")
	if image == "" {
		t.Skip("set FLATRUN_SECURITY_TEST_IMAGE to a locally available immutable Python image")
	}
	command := `import json,os,socket,pathlib
checks={"non_root":os.getuid()==65532,"trusted_working_directory":os.getcwd()=="/work","no_socket":not pathlib.Path("/var/run/docker.sock").exists(),"isolated_imports":__import__("sys").flags.isolated==1}
checks["capabilities_dropped"]="CapEff:\t0000000000000000" in pathlib.Path("/proc/self/status").read_text()
checks["no_new_privileges"]="NoNewPrivs:\t1" in pathlib.Path("/proc/self/status").read_text()
checks["seccomp_enforced"]="Seccomp:\t2" in pathlib.Path("/proc/self/status").read_text()
checks["memory_limit"]=pathlib.Path("/sys/fs/cgroup/memory.max").read_text().strip()=="268435456"
checks["pid_limit"]=pathlib.Path("/sys/fs/cgroup/pids.max").read_text().strip()=="128"
try:
 pathlib.Path("/root-write-probe").write_text("probe")
 checks["root_read_only"]=False
except OSError:
 checks["root_read_only"]=True
try:
 socket.create_connection(("1.1.1.1",443),timeout=0.2)
 checks["network_denied"]=False
except OSError:
 checks["network_denied"]=True
print(json.dumps({"api_version":"flatrun.collector-result/v1","process_status":"completed","items":[checks],"omissions":[],"boundaries":[],"limitations":[],"coverage":{"declared_observation":"complete","manifest_processing":"complete","window_coverage":"unknown"}}))`
	runner := Runner{Images: []string{image}, ScratchRoot: t.TempDir()}
	collector := securityprofile.Collector{ID: "isolation", Image: image, Command: []string{"python3", "-c", command}}
	result, err := runner.Run(context.Background(), collector, securityprofile.Draft("python").Budget, Input{})
	if err != nil {
		t.Fatal(err)
	}
	if result.ProcessStatus != "completed" || len(result.Items) != 1 {
		t.Fatalf("collector failed: %+v", result)
	}
	var checks map[string]bool
	if err = json.Unmarshal(result.Items[0], &checks); err != nil {
		t.Fatal(err)
	}
	for name, passed := range checks {
		if !passed {
			t.Errorf("sandbox control failed: %s", name)
		}
	}
}
