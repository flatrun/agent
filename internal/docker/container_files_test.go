package docker

import (
	"testing"
)

// The two outputs below were captured from real containers: nginx:alpine for
// busybox and nginx:latest for GNU coreutils. They differ only in padding, so
// one parser has to read both.
const busyboxLS = `total 32
drwxr-xr-x    2 root     root          4096 Jun 22 19:46 conf.d
-rw-r--r--    1 root     root          1077 Jun 17 15:58 fastcgi.conf
lrwxrwxrwx    1 root     root            22 Jun 17 15:58 modules -> /usr/lib/nginx/modules
`

const gnuLS = `total 28
drwxr-xr-x 2 root root 4096 Jul 14 01:22 conf.d
-rw-r--r-- 1 root root 1007 Jun 17 14:40 fastcgi_params
-rw-r--r-- 1 root root 5349 Jun 17 14:40 mime.types
`

func TestParseLSOutputBusybox(t *testing.T) {
	files := parseLSOutput(busyboxLS, "/etc/nginx")

	if len(files) != 3 {
		t.Fatalf("got %d entries, want 3: %+v", len(files), files)
	}

	if !files[0].IsDir || files[0].Name != "conf.d" {
		t.Errorf("entry 0 = %+v, want the conf.d directory", files[0])
	}
	if files[0].Path != "/etc/nginx/conf.d" {
		t.Errorf("path = %q, want /etc/nginx/conf.d", files[0].Path)
	}

	if files[1].IsDir || files[1].Size != 1077 {
		t.Errorf("entry 1 = %+v, want a 1077 byte file", files[1])
	}

	// A symlink's target is listed after the name and must not become part of it.
	if !files[2].IsSymlink || files[2].Name != "modules" {
		t.Errorf("entry 2 = %+v, want the modules symlink", files[2])
	}
	if files[2].LinkTarget != "/usr/lib/nginx/modules" {
		t.Errorf("link target = %q", files[2].LinkTarget)
	}
}

func TestParseLSOutputGNU(t *testing.T) {
	files := parseLSOutput(gnuLS, "/etc/nginx")

	if len(files) != 3 {
		t.Fatalf("got %d entries, want 3: %+v", len(files), files)
	}
	if !files[0].IsDir || files[0].Name != "conf.d" {
		t.Errorf("entry 0 = %+v, want the conf.d directory", files[0])
	}
	if files[2].Name != "mime.types" || files[2].Size != 5349 {
		t.Errorf("entry 2 = %+v, want mime.types at 5349 bytes", files[2])
	}
}

func TestParseLSOutputKeepsNamesWithSpaces(t *testing.T) {
	files := parseLSOutput("-rw-r--r-- 1 root root 12 Jun 17 14:40 my config.conf\n", "/etc")

	if len(files) != 1 {
		t.Fatalf("got %d entries, want 1", len(files))
	}
	if files[0].Name != "my config.conf" {
		t.Errorf("name = %q, want it to keep its space", files[0].Name)
	}
}

func TestShellQuote(t *testing.T) {
	tests := []struct {
		in   string
		want string
	}{
		{"/etc/nginx", `'/etc/nginx'`},
		// The path reaches sh -c, so a shell metacharacter has to stay data.
		{"/etc; rm -rf /", `'/etc; rm -rf /'`},
		{"/etc/$(whoami)", `'/etc/$(whoami)'`},
		{"/it's", `'/it'\''s'`},
	}

	for _, tt := range tests {
		if got := shellQuote(tt.in); got != tt.want {
			t.Errorf("shellQuote(%q) = %s, want %s", tt.in, got, tt.want)
		}
	}
}
