package sandbox

// RetainedBotHomeCommand applies cwd after entering the composed overlay.
// Callers must establish a retained binding and readiness first. Unlike the
// legacy image wrapper this command fails closed if the home is not mounted.
// Positional arguments preserve command/cwd bytes without shell interpolation.
func RetainedBotHomeCommand(command []string, workDir string) []string {
	if workDir == "" {
		workDir = "/root"
	}
	if len(command) > 0 && command[0] == "/usr/local/bin/astonish-shell" {
		command = command[1:]
	}
	if len(command) == 0 {
		command = []string{"/bin/bash", "-l"}
	}
	const script = `set -eu
mountpoint -q /sandbox/rootfs
mountpoint -q /sandbox/rootfs/root
exec chroot /sandbox/rootfs /bin/sh -c 'cd "$1" && shift && exec "$@"' home-exec "$@"`
	return append([]string{"/bin/sh", "-c", script, "home-exec", workDir}, command...)
}
