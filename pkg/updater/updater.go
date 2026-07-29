package updater

import (
	"archive/tar"
	"compress/gzip"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"time"

	"github.com/Masterminds/semver/v3"

	"github.com/flatrun/agent/pkg/version"
)

const (
	GitHubOwner = "flatrun"
	GitHubRepo  = "agent"
	BinaryName  = "flatrun-agent"
)

// Channel selects which releases an update considers. Stable ignores
// prereleases; Prerelease is the opt-in channel that also sees betas.
type Channel string

const (
	ChannelStable     Channel = "stable"
	ChannelPrerelease Channel = "prerelease"
)

type Release struct {
	TagName     string  `json:"tag_name"`
	Name        string  `json:"name"`
	PublishedAt string  `json:"published_at"`
	Prerelease  bool    `json:"prerelease"`
	Draft       bool    `json:"draft"`
	Assets      []Asset `json:"assets"`
}

type Asset struct {
	Name               string `json:"name"`
	BrowserDownloadURL string `json:"browser_download_url"`
	Size               int64  `json:"size"`
}

type UpdateResult struct {
	CurrentVersion  string
	LatestVersion   string
	UpdateAvailable bool
	Downloaded      bool
	Installed       bool
	Message         string
}

func CheckForUpdate(channel Channel) (*UpdateResult, error) {
	current := version.Get()
	currentVer := strings.TrimSuffix(current.Version, "-dev")
	currentVer = strings.TrimPrefix(currentVer, "v")

	release, err := getTargetRelease(channel)
	if err != nil {
		return nil, fmt.Errorf("failed to check for updates: %w", err)
	}

	latestVer := strings.TrimPrefix(release.TagName, "v")

	result := &UpdateResult{
		CurrentVersion:  currentVer,
		LatestVersion:   latestVer,
		UpdateAvailable: isNewer(latestVer, currentVer),
	}

	if result.UpdateAvailable {
		result.Message = fmt.Sprintf("Update available: %s -> %s", currentVer, latestVer)
	} else {
		result.Message = fmt.Sprintf("Already up to date (v%s)", currentVer)
	}

	return result, nil
}

func Update(force bool, channel Channel) (*UpdateResult, error) {
	result, err := CheckForUpdate(channel)
	if err != nil {
		return nil, err
	}

	if !result.UpdateAvailable && !force {
		return result, nil
	}

	release, err := getTargetRelease(channel)
	if err != nil {
		return nil, fmt.Errorf("failed to get release info: %w", err)
	}

	asset := findAssetForPlatform(release.Assets)
	if asset == nil {
		return nil, fmt.Errorf("no release found for %s/%s", runtime.GOOS, runtime.GOARCH)
	}

	execPath, err := os.Executable()
	if err != nil {
		return nil, fmt.Errorf("failed to get executable path: %w", err)
	}
	execPath, err = filepath.EvalSymlinks(execPath)
	if err != nil {
		return nil, fmt.Errorf("failed to resolve executable path: %w", err)
	}

	tempDir, err := os.MkdirTemp("", "flatrun-update-*")
	if err != nil {
		return nil, fmt.Errorf("failed to create temp directory: %w", err)
	}
	defer os.RemoveAll(tempDir)

	fmt.Printf("Downloading %s...\n", asset.Name)
	archivePath := filepath.Join(tempDir, asset.Name)
	if err := downloadFile(asset.BrowserDownloadURL, archivePath); err != nil {
		return nil, fmt.Errorf("failed to download update: %w", err)
	}
	result.Downloaded = true

	fmt.Println("Extracting...")
	binaryPath, err := extractBinary(archivePath, tempDir)
	if err != nil {
		return nil, fmt.Errorf("failed to extract binary: %w", err)
	}

	fmt.Println("Installing...")
	if err := installBinary(binaryPath, execPath); err != nil {
		return nil, fmt.Errorf("failed to install binary: %w", err)
	}
	result.Installed = true
	result.Message = fmt.Sprintf("Successfully updated to v%s", result.LatestVersion)

	return result, nil
}

func RestartService() error {
	cmd := exec.Command("systemctl", "restart", "flatrun-agent")
	if err := cmd.Run(); err != nil {
		cmd = exec.Command("sudo", "systemctl", "restart", "flatrun-agent")
		return cmd.Run()
	}
	return nil
}

// getTargetRelease returns the highest-versioned release visible to the channel.
// The stable channel skips prereleases; the prerelease channel considers both,
// so a newer stable is still offered to a beta user. Ordering is by semver, not
// publish date or string comparison, so a prerelease is ranked below its final
// and an older tag can never masquerade as the latest.
func getTargetRelease(channel Channel) (*Release, error) {
	releases, err := getReleases()
	if err != nil {
		return nil, err
	}

	target := selectRelease(releases, channel)
	if target == nil {
		return nil, fmt.Errorf("no %s release found", channel)
	}
	return target, nil
}

func selectRelease(releases []Release, channel Channel) *Release {
	var best *Release
	var bestVer *semver.Version

	for i := range releases {
		r := releases[i]
		if r.Draft {
			continue
		}
		if r.Prerelease && channel != ChannelPrerelease {
			continue
		}

		v, err := semver.NewVersion(strings.TrimPrefix(r.TagName, "v"))
		if err != nil {
			continue
		}
		if bestVer == nil || v.Compare(bestVer) > 0 {
			best = &releases[i]
			bestVer = v
		}
	}

	return best
}

// isNewer reports whether latest is a strictly higher semver than current.
// A current version that does not parse (e.g. a dev build) is treated as
// updatable so the CLI is never stuck when it cannot read its own version.
func isNewer(latest, current string) bool {
	lv, err := semver.NewVersion(strings.TrimPrefix(latest, "v"))
	if err != nil {
		return false
	}
	cv, err := semver.NewVersion(strings.TrimPrefix(current, "v"))
	if err != nil {
		return true
	}
	return lv.Compare(cv) > 0
}

func getReleases() ([]Release, error) {
	url := fmt.Sprintf("https://api.github.com/repos/%s/%s/releases?per_page=100", GitHubOwner, GitHubRepo)

	client := &http.Client{Timeout: 30 * time.Second}
	req, err := http.NewRequest("GET", url, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Accept", "application/vnd.github.v3+json")
	req.Header.Set("User-Agent", "flatrun-agent-updater")

	resp, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("GitHub API returned status %d", resp.StatusCode)
	}

	var releases []Release
	if err := json.NewDecoder(resp.Body).Decode(&releases); err != nil {
		return nil, err
	}

	return releases, nil
}

func findAssetForPlatform(assets []Asset) *Asset {
	osName := runtime.GOOS
	archName := runtime.GOARCH

	expectedName := fmt.Sprintf("%s-%s-%s.tar.gz", BinaryName, osName, archName)

	for _, asset := range assets {
		if strings.Contains(asset.Name, osName) && strings.Contains(asset.Name, archName) {
			return &asset
		}
	}

	for _, asset := range assets {
		if asset.Name == expectedName || strings.HasSuffix(asset.Name, expectedName) {
			return &asset
		}
	}

	return nil
}

func downloadFile(url, destPath string) error {
	client := &http.Client{Timeout: 5 * time.Minute}
	resp, err := client.Get(url)
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("download failed with status %d", resp.StatusCode)
	}

	out, err := os.Create(destPath)
	if err != nil {
		return err
	}
	defer out.Close()

	_, err = io.Copy(out, resp.Body)
	return err
}

func extractBinary(archivePath, destDir string) (string, error) {
	f, err := os.Open(archivePath)
	if err != nil {
		return "", err
	}
	defer f.Close()

	gzr, err := gzip.NewReader(f)
	if err != nil {
		return "", err
	}
	defer gzr.Close()

	tr := tar.NewReader(gzr)
	var binaryPath string

	for {
		header, err := tr.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			return "", err
		}

		if header.Typeflag != tar.TypeReg {
			continue
		}

		name := filepath.Base(header.Name)
		if name == BinaryName || strings.HasPrefix(name, BinaryName) {
			binaryPath = filepath.Join(destDir, name)
			outFile, err := os.OpenFile(binaryPath, os.O_CREATE|os.O_WRONLY, 0755)
			if err != nil {
				return "", err
			}
			if _, err := io.Copy(outFile, tr); err != nil {
				outFile.Close()
				return "", err
			}
			outFile.Close()
			break
		}
	}

	if binaryPath == "" {
		return "", fmt.Errorf("binary not found in archive")
	}

	return binaryPath, nil
}

func installBinary(newBinary, targetPath string) error {
	backupPath := targetPath + ".backup"
	if err := os.Rename(targetPath, backupPath); err != nil {
		return fmt.Errorf("failed to backup current binary: %w", err)
	}

	newData, err := os.ReadFile(newBinary)
	if err != nil {
		_ = os.Rename(backupPath, targetPath)
		return fmt.Errorf("failed to read new binary: %w", err)
	}

	if err := os.WriteFile(targetPath, newData, 0755); err != nil {
		_ = os.Rename(backupPath, targetPath)
		return fmt.Errorf("failed to write new binary: %w", err)
	}

	os.Remove(backupPath)
	return nil
}

func Rollback() error {
	execPath, err := os.Executable()
	if err != nil {
		return fmt.Errorf("failed to get executable path: %w", err)
	}
	execPath, err = filepath.EvalSymlinks(execPath)
	if err != nil {
		return fmt.Errorf("failed to resolve executable path: %w", err)
	}

	backupPath := execPath + ".backup"
	if _, err := os.Stat(backupPath); os.IsNotExist(err) {
		return fmt.Errorf("no backup found to rollback to")
	}

	if err := os.Rename(backupPath, execPath); err != nil {
		return fmt.Errorf("failed to restore backup: %w", err)
	}

	return nil
}
