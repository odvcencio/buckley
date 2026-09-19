package provision

import (
	"context"
	"errors"
	"strings"
)

var ErrCleanupRequired = errors.New("provisioning cleanup required")

// CleanupRequiredError carries only the bounded owned identity needed to retry
// removal after an acknowledgement or daemon failure.
type CleanupRequiredError struct {
	Resource string
	Identity string
}

func (e *CleanupRequiredError) Error() string {
	if e == nil || e.Resource == "" || e.Identity == "" {
		return ErrCleanupRequired.Error()
	}
	return ErrCleanupRequired.Error() + ": " + e.Resource + " " + e.Identity
}

func (*CleanupRequiredError) Unwrap() error { return ErrCleanupRequired }

func joinCleanupRequired(primary error, resource, identity string) error {
	if len(resource) == 0 || len(resource) > 64 || strings.ContainsAny(resource, "\r\n\x00") || len(identity) == 0 || len(identity) > 512 || strings.ContainsAny(identity, "\r\n\x00") {
		return errors.Join(primary, &CleanupRequiredError{})
	}
	return errors.Join(primary, &CleanupRequiredError{Resource: resource, Identity: identity})
}

func cleanupContainer(ctx context.Context, runner Runner, id string) error {
	if runner == nil || !containerIDPattern.MatchString(id) {
		return errors.New("container cleanup identity is invalid")
	}
	if _, err := runner.Run(ctx, "rm", "-f", "--", id); err == nil {
		return nil
	}
	remaining, listErr := runner.Run(ctx, "container", "ls", "-a", "--no-trunc", "--filter", "id="+id, "--format", "{{.ID}}")
	if listErr == nil && strings.TrimSpace(remaining) == "" {
		return nil
	}
	return errors.New("container cleanup requires operator action")
}

func cleanupNetwork(ctx context.Context, runner Runner, id string) error {
	if runner == nil || !containerIDPattern.MatchString(id) {
		return errors.New("network cleanup identity is invalid")
	}
	if _, err := runner.Run(ctx, "network", "rm", "--", id); err == nil {
		return nil
	}
	remaining, listErr := runner.Run(ctx, "network", "ls", "--no-trunc", "--filter", "id="+id, "--format", "{{.ID}}")
	if listErr == nil && strings.TrimSpace(remaining) == "" {
		return nil
	}
	return errors.New("network cleanup requires operator action")
}

func cleanupImageTag(ctx context.Context, runner Runner, tag string) error {
	if !localTagPattern.MatchString(tag) {
		return errors.New("image cleanup identity is invalid")
	}
	return cleanupImageReference(ctx, runner, tag)
}

func cleanupImageReference(ctx context.Context, runner Runner, reference string) error {
	if runner == nil || safeReference(reference) == "" || strings.Contains(reference, "@") {
		return errors.New("image cleanup identity is invalid")
	}
	if _, err := runner.Run(ctx, "image", "rm", "--", reference); err == nil {
		return nil
	}
	remaining, listErr := runner.Run(ctx, "image", "ls", "--no-trunc", "--quiet", reference)
	if listErr == nil && strings.TrimSpace(remaining) == "" {
		return nil
	}
	return errors.New("image cleanup requires operator action")
}

func resolveOwnedContainer(ctx context.Context, runner Runner, name, owner, imageRef, imageID string) (string, error) {
	if runner == nil || name == "" || !ownerPattern.MatchString(owner) || safeReference(imageRef) == "" || !imageIDPattern.MatchString(imageID) {
		return "", errors.New("container ownership query is invalid")
	}
	template := `{{.Id}}|{{.Image}}|{{.Config.Image}}|{{index .Config.Labels "` + registryOwnerLabel + `"}}`
	output, err := runner.Run(ctx, "inspect", "-f", template, "--", name)
	if err != nil {
		return "", errors.New("container ownership is unavailable")
	}
	parts := strings.Split(strings.TrimSpace(output), "|")
	if len(parts) != 4 || !containerIDPattern.MatchString(parts[0]) || parts[1] != imageID || parts[2] != imageRef || parts[3] != owner {
		return "", errors.New("container name is not owned by this ceremony")
	}
	return parts[0], nil
}

func resolveOwnedNetwork(ctx context.Context, runner Runner, name, owner string) (string, error) {
	if runner == nil || name == "" || !ownerPattern.MatchString(owner) {
		return "", errors.New("network ownership query is invalid")
	}
	template := `{{.Id}}|{{index .Labels "` + registryOwnerLabel + `"}}`
	output, err := runner.Run(ctx, "network", "inspect", "-f", template, "--", name)
	if err != nil {
		return "", errors.New("network ownership is unavailable")
	}
	parts := strings.Split(strings.TrimSpace(output), "|")
	if len(parts) != 2 || !containerIDPattern.MatchString(parts[0]) || parts[1] != owner {
		return "", errors.New("network name is not owned by this ceremony")
	}
	return parts[0], nil
}
