package tool

import "strings"

// SetContainerContext tracks compose/workdir metadata without forcing container execution.
func (r *Registry) SetContainerContext(composePath, workDir string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.setContainerContextLocked(composePath, workDir)
}

func (r *Registry) setContainerContextLocked(composePath, workDir string) {
	r.containerCompose = composePath
	r.containerWorkDir = workDir
}

// ContainerInfo exposes whether container execution is enabled and the compose details.
func (r *Registry) ContainerInfo() (enabled bool, composePath string, workDir string) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	return strings.TrimSpace(r.containerCompose) != "", r.containerCompose, r.containerWorkDir
}
