package config

import (
	"os"
	"path/filepath"
	"testing"
)

func TestFirecrackerConfigLoadAndDefaults(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.yml")
	if err := os.WriteFile(path, []byte("runs:\n  firecracker:\n    binary: /opt/firecracker\n    jailer: /opt/jailer\n    kernel: /opt/vmlinux\n    state_dir: /var/lib/custom-fc\n    chroot_base_dir: /srv/custom-jailer\n    guest_init: /opt/flatrun-guest-init\n    default_memory: 536870912\n"), 0600); err != nil {
		t.Fatal(err)
	}
	cfg, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	fc := cfg.Runs.Firecracker
	if fc.ChrootBaseDir != "/srv/custom-jailer" || fc.Binary != "/opt/firecracker" || fc.Jailer != "/opt/jailer" || fc.Kernel != "/opt/vmlinux" || fc.StateDir != "/var/lib/custom-fc" || fc.GuestInit != "/opt/flatrun-guest-init" || fc.DefaultMemory != 512<<20 {
		t.Fatalf("config %+v", fc)
	}
	defaults := &Config{}
	setDefaults(defaults)
	fc = defaults.Runs.Firecracker
	if fc.ChrootBaseDir != "/srv/jailer" || fc.Kernel != "" || fc.Binary != "firecracker" || fc.Jailer != "jailer" || fc.StateDir != "/var/lib/flatrun/firecracker" || fc.DefaultMemory != 256<<20 {
		t.Fatalf("defaults %+v", fc)
	}
}
