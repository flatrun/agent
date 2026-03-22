package docker

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"testing"
)

func TestExtractBindMountPath(t *testing.T) {
	tests := []struct {
		name     string
		volume   string
		expected string
	}{
		{
			name:     "relative path with container path",
			volume:   "./app:/app",
			expected: "./app",
		},
		{
			name:     "relative path with options",
			volume:   "./data:/var/data:ro",
			expected: "./data",
		},
		{
			name:     "named volume",
			volume:   "myvolume:/var/data",
			expected: "",
		},
		{
			name:     "absolute path",
			volume:   "/host/path:/container/path",
			expected: "/host/path",
		},
		{
			name:     "parent directory path",
			volume:   "../shared:/app/shared",
			expected: "../shared",
		},
		{
			name:     "volume without colon",
			volume:   "myvolume",
			expected: "",
		},
		{
			name:     "empty string",
			volume:   "",
			expected: "",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := extractBindMountPath(tt.volume)
			if result != tt.expected {
				t.Errorf("extractBindMountPath(%q) = %q, want %q", tt.volume, result, tt.expected)
			}
		})
	}
}

func TestCreateBindMountDirs(t *testing.T) {
	// Create temp directory for test
	tmpDir, err := os.MkdirTemp("", "test-deployment-*")
	if err != nil {
		t.Fatalf("Failed to create temp dir: %v", err)
	}
	defer os.RemoveAll(tmpDir)

	d := NewDiscovery(tmpDir)

	tests := []struct {
		name           string
		composeContent string
		expectedDirs   []string
	}{
		{
			name: "creates single bind mount directory",
			composeContent: `
services:
  app:
    image: nginx
    volumes:
      - ./app:/app
`,
			expectedDirs: []string{"app"},
		},
		{
			name: "creates multiple bind mount directories",
			composeContent: `
services:
  app:
    image: nginx
    volumes:
      - ./app:/app
      - ./data:/var/data
      - ./config:/etc/config
`,
			expectedDirs: []string{"app", "data", "config"},
		},
		{
			name: "skips named volumes",
			composeContent: `
services:
  app:
    image: nginx
    volumes:
      - ./app:/app
      - myvolume:/var/data
`,
			expectedDirs: []string{"app"},
		},
		{
			name: "handles nested paths",
			composeContent: `
services:
  app:
    image: nginx
    volumes:
      - ./data/uploads:/var/uploads
`,
			expectedDirs: []string{"data/uploads"},
		},
		{
			name: "handles multiple services",
			composeContent: `
services:
  web:
    image: nginx
    volumes:
      - ./web:/app
  worker:
    image: redis
    volumes:
      - ./worker:/data
`,
			expectedDirs: []string{"web", "worker"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Create a unique deployment path for each test
			deploymentPath := filepath.Join(tmpDir, "deployment-"+tt.name)
			if err := os.MkdirAll(deploymentPath, 0755); err != nil {
				t.Fatalf("Failed to create deployment path: %v", err)
			}

			err := d.createBindMountDirs(deploymentPath, tt.composeContent)
			if err != nil {
				t.Fatalf("createBindMountDirs failed: %v", err)
			}

			// Verify directories were created
			for _, dir := range tt.expectedDirs {
				fullPath := filepath.Join(deploymentPath, dir)
				info, err := os.Stat(fullPath)
				if err != nil {
					t.Errorf("Expected directory %q to exist, but got error: %v", dir, err)
					continue
				}
				if !info.IsDir() {
					t.Errorf("Expected %q to be a directory", dir)
				}
				// Check permissions (0777)
				perm := info.Mode().Perm()
				if perm != 0777 {
					t.Errorf("Expected directory %q to have permissions 0777, got %o", dir, perm)
				}
			}
		})
	}
}

func TestCreateDeploymentWithBindMounts(t *testing.T) {
	tmpDir, err := os.MkdirTemp("", "test-deployment-*")
	if err != nil {
		t.Fatalf("Failed to create temp dir: %v", err)
	}
	defer os.RemoveAll(tmpDir)

	d := NewDiscovery(tmpDir)

	composeContent := `name: test-app
services:
  app:
    image: bitnami/laravel:latest
    volumes:
      - ./app:/app
    expose:
      - "8000"
`

	err = d.CreateDeployment("test-app", composeContent)
	if err != nil {
		t.Fatalf("CreateDeployment failed: %v", err)
	}

	// Verify deployment directory exists
	deploymentPath := filepath.Join(tmpDir, "test-app")
	if _, err := os.Stat(deploymentPath); err != nil {
		t.Errorf("Deployment directory should exist: %v", err)
	}

	// Verify compose file exists
	composePath := filepath.Join(deploymentPath, "docker-compose.yml")
	if _, err := os.Stat(composePath); err != nil {
		t.Errorf("docker-compose.yml should exist: %v", err)
	}

	// Verify bind mount directory was created with correct permissions
	appDir := filepath.Join(deploymentPath, "app")
	info, err := os.Stat(appDir)
	if err != nil {
		t.Errorf("app directory should exist: %v", err)
	} else {
		if !info.IsDir() {
			t.Error("app should be a directory")
		}
		if info.Mode().Perm() != 0777 {
			t.Errorf("app directory should have 0777 permissions, got %o", info.Mode().Perm())
		}
	}
}

func TestApplyMountOwnership(t *testing.T) {
	tmpDir, err := os.MkdirTemp("", "test-ownership-*")
	if err != nil {
		t.Fatalf("Failed to create temp dir: %v", err)
	}
	defer os.RemoveAll(tmpDir)

	d := NewDiscovery(tmpDir)
	deploymentPath := filepath.Join(tmpDir, "test-deploy")
	if err := os.MkdirAll(deploymentPath, 0755); err != nil {
		t.Fatalf("Failed to create deployment dir: %v", err)
	}

	t.Run("creates subdirectories with user ownership", func(t *testing.T) {
		currentUID := os.Getuid()
		currentGID := os.Getgid()
		user := fmt.Sprintf("%d:%d", currentUID, currentGID)

		mounts := []MountOwnership{
			{
				HostPath:       "./storage",
				User:           user,
				Subdirectories: []string{"framework/cache", "framework/sessions", "logs"},
			},
		}

		err := d.ApplyMountOwnership(deploymentPath, mounts)
		if err != nil {
			t.Fatalf("ApplyMountOwnership failed: %v", err)
		}

		basePath := filepath.Join(deploymentPath, "storage")
		if _, err := os.Stat(basePath); err != nil {
			t.Errorf("Expected storage dir to exist: %v", err)
		}

		for _, sub := range []string{"framework/cache", "framework/sessions", "logs"} {
			subPath := filepath.Join(basePath, sub)
			if _, err := os.Stat(subPath); err != nil {
				t.Errorf("Expected subdirectory %q to exist: %v", sub, err)
			}
		}
	})

	t.Run("falls back to 0777 when no user specified", func(t *testing.T) {
		mounts := []MountOwnership{
			{
				HostPath: "./nouser",
			},
		}

		err := d.ApplyMountOwnership(deploymentPath, mounts)
		if err != nil {
			t.Fatalf("ApplyMountOwnership failed: %v", err)
		}

		info, err := os.Stat(filepath.Join(deploymentPath, "nouser"))
		if err != nil {
			t.Fatalf("Expected nouser dir to exist: %v", err)
		}
		if info.Mode().Perm() != 0777 {
			t.Errorf("Expected 0777 permissions, got %o", info.Mode().Perm())
		}
	})

	t.Run("rejects invalid user format", func(t *testing.T) {
		mounts := []MountOwnership{
			{
				HostPath: "./baduser",
				User:     "notvalid",
			},
		}

		err := d.ApplyMountOwnership(deploymentPath, mounts)
		if err == nil {
			t.Fatal("Expected error for invalid user format")
		}
	})
}

func TestParseUIDGID(t *testing.T) {
	tests := []struct {
		name      string
		input     string
		wantUID   int
		wantGID   int
		wantError bool
	}{
		{name: "valid pair", input: "33:33", wantUID: 33, wantGID: 33},
		{name: "different values", input: "1000:1001", wantUID: 1000, wantGID: 1001},
		{name: "root", input: "0:0", wantUID: 0, wantGID: 0},
		{name: "missing colon", input: "1000", wantError: true},
		{name: "non-numeric uid", input: "abc:100", wantError: true},
		{name: "non-numeric gid", input: "100:abc", wantError: true},
		{name: "empty string", input: "", wantError: true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			uid, gid, err := parseUIDGID(tt.input)
			if tt.wantError {
				if err == nil {
					t.Errorf("parseUIDGID(%q) expected error, got nil", tt.input)
				}
				return
			}
			if err != nil {
				t.Fatalf("parseUIDGID(%q) unexpected error: %v", tt.input, err)
			}
			if uid != tt.wantUID {
				t.Errorf("parseUIDGID(%q) uid = %d, want %d", tt.input, uid, tt.wantUID)
			}
			if gid != tt.wantGID {
				t.Errorf("parseUIDGID(%q) gid = %d, want %d", tt.input, gid, tt.wantGID)
			}
		})
	}
}

func TestGenerateMetadataFromCompose_ServiceName(t *testing.T) {
	tests := []struct {
		name        string
		compose     string
		wantService string
	}{
		{
			name: "single service stores service name",
			compose: `services:
  backend:
    image: myapp:latest
    ports:
      - "3000:3000"
`,
			wantService: "backend",
		},
		{
			name: "single service without ports stores name",
			compose: `services:
  worker:
    image: myapp:latest
`,
			wantService: "worker",
		},
		{
			name: "multiple services picks first with ports",
			compose: `services:
  web:
    image: nginx:latest
    ports:
      - "80:80"
  db:
    image: postgres:15
`,
			wantService: "web",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			tmpDir, err := os.MkdirTemp("", "meta-test-*")
			if err != nil {
				t.Fatalf("Failed to create temp dir: %v", err)
			}
			defer os.RemoveAll(tmpDir)

			composePath := filepath.Join(tmpDir, "docker-compose.yml")
			if err := os.WriteFile(composePath, []byte(tt.compose), 0644); err != nil {
				t.Fatalf("Failed to write compose file: %v", err)
			}

			d := NewDiscovery(tmpDir)
			metadata := d.generateMetadataFromCompose(composePath, "test")
			if metadata == nil {
				t.Fatal("generateMetadataFromCompose returned nil")
			}

			if metadata.Networking.Service != tt.wantService {
				t.Errorf("Networking.Service = %q, want %q", metadata.Networking.Service, tt.wantService)
			}
		})
	}
}

func TestExtractBindMounts(t *testing.T) {
	tests := []struct {
		name     string
		compose  string
		expected []string
	}{
		{
			name: "single bind mount",
			compose: `services:
  app:
    image: nginx
    volumes:
      - ./data:/var/data
`,
			expected: []string{"./data"},
		},
		{
			name: "multiple bind mounts",
			compose: `services:
  app:
    image: nginx
    volumes:
      - ./app:/app
      - ./config:/etc/config
`,
			expected: []string{"./app", "./config"},
		},
		{
			name: "skips named volumes",
			compose: `services:
  app:
    image: nginx
    volumes:
      - ./data:/data
      - myvolume:/var/lib
`,
			expected: []string{"./data"},
		},
		{
			name: "multiple services",
			compose: `services:
  web:
    volumes:
      - ./web:/app
  worker:
    volumes:
      - ./worker:/app
`,
			expected: []string{"./web", "./worker"},
		},
		{
			name: "deduplicates shared mounts",
			compose: `services:
  web:
    volumes:
      - ./shared:/data
  worker:
    volumes:
      - ./shared:/data
`,
			expected: []string{"./shared"},
		},
		{
			name: "no volumes",
			compose: `services:
  app:
    image: nginx
`,
			expected: nil,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := ExtractBindMounts(tt.compose)
			if len(result) != len(tt.expected) {
				t.Fatalf("ExtractBindMounts returned %d paths, want %d: got %v", len(result), len(tt.expected), result)
			}
			sort.Strings(result)
			sorted := make([]string, len(tt.expected))
			copy(sorted, tt.expected)
			sort.Strings(sorted)
			for i, path := range sorted {
				if result[i] != path {
					t.Errorf("ExtractBindMounts[%d] = %q, want %q", i, result[i], path)
				}
			}
		})
	}
}
