package api

import (
	"bytes"
	"image"
	"image/color"
	"image/png"
	"io"
	"mime/multipart"
	"net/http"
	"testing"

	"github.com/flatrun/agent/internal/auth"
)

func pngBytes(t *testing.T) []byte {
	t.Helper()
	img := image.NewRGBA(image.Rect(0, 0, 4, 4))
	img.Set(1, 1, color.RGBA{R: 200, A: 255})
	var buf bytes.Buffer
	if err := png.Encode(&buf, img); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

func uploadLogo(t *testing.T, c workspaceClient, path string, content []byte) int {
	t.Helper()
	var body bytes.Buffer
	form := multipart.NewWriter(&body)
	part, _ := form.CreateFormFile("file", "logo")
	_, _ = part.Write(content)
	_ = form.Close()
	req, _ := http.NewRequest(http.MethodPut, c.url+"/api"+path, &body)
	req.Header.Set("Content-Type", form.FormDataContentType())
	req.Header.Set("Authorization", "Bearer "+c.token)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	return resp.StatusCode
}

func fetchLogo(t *testing.T, c workspaceClient, path string) (int, string, []byte) {
	t.Helper()
	req, _ := http.NewRequest(http.MethodGet, c.url+"/api"+path, nil)
	req.Header.Set("Authorization", "Bearer "+c.token)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	data, _ := io.ReadAll(resp.Body)
	return resp.StatusCode, resp.Header.Get("Content-Type"), data
}

func TestWorkspaceLogoAndName(t *testing.T) {
	s, client := setupWorkspaceTestServer(t)
	if _, err := s.authManager.CreateUser("bob", "", "bob-pass", auth.RoleOperator, nil); err != nil {
		t.Fatal(err)
	}
	admin := client.login("admin", "admin-pass")
	bob := client.login("bob", "bob-pass")
	if status, body := admin.do(http.MethodPost, "/workspaces/default/members", "", map[string]string{"username": "bob", "role": "member"}); status != http.StatusCreated {
		t.Fatalf("add bob = %d %v", status, body)
	}

	if status, body := admin.do(http.MethodPut, "/workspaces/default", "", map[string]string{"name": "Lumen Labs"}); status != http.StatusOK || body["name"] != "Lumen Labs" || body["slug"] != "default" {
		t.Fatalf("rename = %d %v", status, body)
	}

	if status, _, _ := fetchLogo(t, admin, "/workspaces/default/logo"); status != http.StatusNotFound {
		t.Fatalf("logo before upload = %d", status)
	}
	logo := pngBytes(t)
	if status := uploadLogo(t, bob, "/workspaces/default/logo", logo); status != http.StatusForbidden {
		t.Fatalf("member uploading = %d", status)
	}
	if status := uploadLogo(t, admin, "/workspaces/default/logo", []byte("not an image")); status != http.StatusUnsupportedMediaType {
		t.Fatalf("text upload = %d", status)
	}
	if status := uploadLogo(t, admin, "/workspaces/default/logo", append(logo, make([]byte, 600<<10)...)); status != http.StatusRequestEntityTooLarge {
		t.Fatalf("oversized upload = %d", status)
	}
	if status := uploadLogo(t, admin, "/workspaces/default/logo", logo); status != http.StatusOK {
		t.Fatalf("upload = %d", status)
	}

	status, contentType, data := fetchLogo(t, bob, "/workspaces/default/logo")
	if status != http.StatusOK || contentType != "image/png" || !bytes.Equal(data, logo) {
		t.Fatalf("member reading logo = %d %s %d bytes", status, contentType, len(data))
	}
	status, body := bob.do(http.MethodGet, "/workspaces", "", nil)
	items, _ := body["items"].([]any)
	if status != http.StatusOK || len(items) != 1 || items[0].(map[string]any)["logo"] == nil {
		t.Fatalf("workspace list = %d %v", status, body)
	}

	if status, _ := admin.do(http.MethodDelete, "/workspaces/default/logo", "", nil); status != http.StatusNoContent {
		t.Fatalf("delete logo = %d", status)
	}
	if status, _, _ := fetchLogo(t, admin, "/workspaces/default/logo"); status != http.StatusNotFound {
		t.Fatalf("logo after delete = %d", status)
	}
}
