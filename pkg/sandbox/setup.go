package sandbox

// SandboxStatus holds information about the sandbox runtime environment.
// Local sessions use Docker OverlayFS; Kubernetes/OpenShell report via Backend.Health.
type SandboxStatus struct {
	Platform      string
	DockerReady   bool
	OverlayReady  bool
	TemplateCount int
	SessionCount  int
}
