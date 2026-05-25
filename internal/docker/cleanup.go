package docker

import (
	"context"
	"strings"
	"time"

	"github.com/docker/docker/api/types/container"
	"github.com/docker/docker/api/types/filters"
	"github.com/docker/docker/api/types/image"
)

type RemovedImage struct {
	ID    string `json:"id"`
	Tag   string `json:"tag,omitempty"`
	Bytes int64  `json:"bytes"`
}

type CleanupResult struct {
	Removed    []RemovedImage `json:"removed"`
	FreedBytes int64          `json:"freed_bytes"`
	ImagesKept int            `json:"images_kept"`
	DryRun     bool           `json:"dry_run,omitempty"`
}

type imageRecord struct {
	id       string
	repos    []string
	fullRefs []string
	bytes    int64
}

func (m *Manager) CleanupDeploymentImages(name string, dryRun bool) (CleanupResult, error) {
	if m.apiClient == nil {
		return CleanupResult{}, nil
	}

	deployment, err := m.GetDeployment(name)
	if err != nil {
		return CleanupResult{}, err
	}

	currentRefs, currentRepos, err := composeImageRefs(m.executor, deployment.Path)
	if err != nil {
		return CleanupResult{}, err
	}
	if len(currentRefs) == 0 {
		return CleanupResult{}, nil
	}

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	hostImages, err := m.apiClient.listImageRecords(ctx)
	if err != nil {
		return CleanupResult{}, err
	}

	inUseByContainers, err := m.apiClient.containerImageRefs(ctx)
	if err != nil {
		return CleanupResult{}, err
	}

	otherDeploymentRefs, _ := m.imageRefsAcrossDeployments(deployment.Name)

	stale, kept := selectStaleImages(hostImages, currentRefs, currentRepos, inUseByContainers, otherDeploymentRefs)

	result := CleanupResult{DryRun: dryRun, ImagesKept: kept}
	for _, img := range stale {
		tagLabel := ""
		if len(img.fullRefs) > 0 {
			tagLabel = img.fullRefs[0]
		}
		if !dryRun {
			if _, err := m.apiClient.cli.ImageRemove(ctx, img.id, image.RemoveOptions{}); err != nil {
				continue
			}
		}
		result.Removed = append(result.Removed, RemovedImage{
			ID:    img.id,
			Tag:   tagLabel,
			Bytes: img.bytes,
		})
		result.FreedBytes += img.bytes
	}

	return result, nil
}

func (m *Manager) PruneDanglingImages(dryRun bool) (CleanupResult, error) {
	if m.apiClient == nil {
		return CleanupResult{}, nil
	}

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	if dryRun {
		f := filters.NewArgs(filters.Arg("dangling", "true"))
		images, err := m.apiClient.cli.ImageList(ctx, image.ListOptions{Filters: f, All: true})
		if err != nil {
			return CleanupResult{}, err
		}
		result := CleanupResult{DryRun: true}
		for _, img := range images {
			result.Removed = append(result.Removed, RemovedImage{ID: img.ID, Bytes: img.Size})
			result.FreedBytes += img.Size
		}
		return result, nil
	}

	report, err := m.apiClient.cli.ImagesPrune(ctx, filters.NewArgs(filters.Arg("dangling", "true")))
	if err != nil {
		return CleanupResult{}, err
	}
	result := CleanupResult{FreedBytes: int64(report.SpaceReclaimed)}
	for _, d := range report.ImagesDeleted {
		if d.Deleted != "" {
			result.Removed = append(result.Removed, RemovedImage{ID: d.Deleted})
		}
	}
	return result, nil
}

func composeImageRefs(executor *ComposeExecutor, deploymentPath string) (map[string]bool, map[string]bool, error) {
	images, err := executor.GetImageInfo(deploymentPath)
	if err != nil {
		return nil, nil, err
	}
	refs := make(map[string]bool)
	repos := make(map[string]bool)
	for _, img := range images {
		if img.Image == "" || img.IsBuild {
			continue
		}
		refs[img.Image] = true
		repos[splitRepo(img.Image)] = true
	}
	return refs, repos, nil
}

func (m *Manager) imageRefsAcrossDeployments(excludeName string) (map[string]bool, error) {
	out := make(map[string]bool)

	deployments, err := m.discovery.FindDeployments()
	if err != nil {
		return out, nil
	}
	if infra, err := m.discovery.FindInfrastructure(); err == nil {
		deployments = append(deployments, infra...)
	}

	for _, dep := range deployments {
		if dep.Name == excludeName {
			continue
		}
		images, err := m.executor.GetImageInfo(dep.Path)
		if err != nil {
			continue
		}
		for _, img := range images {
			if img.Image != "" && !img.IsBuild {
				out[img.Image] = true
			}
		}
	}
	return out, nil
}

func (a *APIClient) listImageRecords(ctx context.Context) ([]imageRecord, error) {
	images, err := a.cli.ImageList(ctx, image.ListOptions{All: false})
	if err != nil {
		return nil, err
	}
	out := make([]imageRecord, 0, len(images))
	for _, img := range images {
		rec := imageRecord{id: img.ID, bytes: img.Size}
		for _, ref := range img.RepoTags {
			if ref == "" || ref == "<none>:<none>" {
				continue
			}
			rec.fullRefs = append(rec.fullRefs, ref)
			rec.repos = append(rec.repos, splitRepo(ref))
		}
		out = append(out, rec)
	}
	return out, nil
}

func (a *APIClient) containerImageRefs(ctx context.Context) (map[string]bool, error) {
	containers, err := a.cli.ContainerList(ctx, container.ListOptions{All: true})
	if err != nil {
		return nil, err
	}
	out := make(map[string]bool)
	for _, c := range containers {
		if c.ImageID != "" {
			out[c.ImageID] = true
		}
		if c.Image != "" {
			out[c.Image] = true
		}
	}
	return out, nil
}

func selectStaleImages(host []imageRecord, currentRefs, currentRepos, inUse, otherDeps map[string]bool) ([]imageRecord, int) {
	var stale []imageRecord
	kept := 0
	for _, img := range host {
		if !anyMatch(img.repos, currentRepos) {
			kept++
			continue
		}
		if anyMatch(img.fullRefs, currentRefs) {
			kept++
			continue
		}
		if inUse[img.id] {
			kept++
			continue
		}
		if anyMatch(img.fullRefs, inUse) {
			kept++
			continue
		}
		if anyMatch(img.fullRefs, otherDeps) {
			kept++
			continue
		}
		stale = append(stale, img)
	}
	return stale, kept
}

func anyMatch(values []string, set map[string]bool) bool {
	for _, v := range values {
		if set[v] {
			return true
		}
	}
	return false
}

func splitRepo(ref string) string {
	if i := strings.LastIndex(ref, "@"); i > 0 {
		return ref[:i]
	}
	if i := strings.LastIndex(ref, ":"); i > 0 {
		after := ref[i+1:]
		if !strings.Contains(after, "/") {
			return ref[:i]
		}
	}
	return ref
}
