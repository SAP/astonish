package k8s

import (
	"strings"
	"testing"
)

// TestEntrypointScript_Defaults locks in the canonical script shape
// emitted when callers pass the zero-value options. This is the script
// that gets baked into astonish-sandbox-base, so regressions in the
// defaults would ship broken images cluster-wide.
//
// The default overlay mode is fuse, because it's the most portable
// option and the only one that works on nested-LXC dev clusters. See
// OverlayMode for the matrix.
func TestEntrypointScript_Defaults(t *testing.T) {
	s := EntrypointScript(EntrypointScriptOptions{})

	checks := []struct {
		name string
		want string
	}{
		{"shebang", "#!/bin/sh"},
		{"set -eu", "\nset -eu\n"},
		{"pipefail probe", "if ( set -o pipefail ) 2>/dev/null; then"},
		{"session-id guard", `: "${ASTONISH_SESSION_ID:?ASTONISH_SESSION_ID must be set}"`},
		{"layer-chain guard", `: "${ASTONISH_LAYER_CHAIN:?ASTONISH_LAYER_CHAIN must be set}"`},
		{"uppers default mount", "UPPERS_DIR='/mnt/astonish-uppers'"},
		{"layers default mount", "LAYERS_DIR='/mnt/astonish-layers'"},
		{"upperdir default", "UPPER_DIR='/var/astonish/overlay/upper'"},
		{"workdir default", "WORK_DIR='/var/astonish/overlay/work'"},
		{"mountpoint default", "MOUNT_POINT='/sandbox/rootfs'"},
		{"overlay-mode default", "OVERLAY_MODE='fuse'"},
		{"resume tarball path", `RESUME_TAR="$UPPERS_DIR/upper.tar.zst"`},
		{"legacy resume fallback", `RESUME_TAR="$UPPERS_DIR/$ASTONISH_SESSION_ID/upper.tar.zst"`},
		{"resume guard", `if [ -f "$RESUME_TAR" ]; then`},
		{"resume tar cmd", `tar --numeric-owner --xattrs --acls -I zstd -xf "$RESUME_TAR" -C "$UPPER_DIR"`},
		{"awk reversal", `awk -F, -v dir="$LAYERS_DIR"`},
		{"awk body top-first", `for (i = NF; i > 0; i--)`},
		{"pre-seed bottom layer", `BOTTOM_LAYER="${LOWER##*:}"`},
		{"pre-seed loop", `for _d in "$BOTTOM_LAYER"/*/`},
		{"pre-seed symlink guard", `[ -L "${_d%/}" ] && continue`},
		{"pre-seed mkdir", `mkdir -p "$UPPER_DIR/$_name"`},
		{"kernel helper defined", "mount_overlay_kernel() {"},
		{"fuse helper defined", "mount_overlay_fuse() {"},
		{"fuse invocation", "fuse-overlayfs \\"},
		{"fuse squash_to_root", "squash_to_root"},
		{"fuse mountpoint wait", "mountpoint -q"},
		{"fuse mknod fallback", "mknod /dev/fuse c 10 229"},
		{"fuse dispatcher", "mount_overlay_fuse || {"},
		{"handoff env override", `HANDOFF="${ASTONISH_HANDOFF:-$_DEFAULT_HANDOFF}"`},
		{"handoff default sleep", `_DEFAULT_HANDOFF='/bin/sleep'`},
		{"handoff args default infinity", `_DEFAULT_HANDOFF_ARGS='infinity'`},
		{"handoff exec", `exec chroot "$MOUNT_POINT" "$HANDOFF" $HANDOFF_ARGS`},
	}
	for _, c := range checks {
		if !strings.Contains(s, c.want) {
			t.Errorf("script missing %s: expected substring %q\n---script---\n%s", c.name, c.want, s)
		}
	}

	// Default mode is fuse; kernel dispatcher line MUST be absent,
	// otherwise we'd be emitting dead code and the pod logs would
	// read misleadingly.
	if strings.Contains(s, "mount_overlay_kernel || {") {
		t.Errorf("default mode is fuse, but kernel dispatcher present:\n%s", s)
	}
	if strings.Contains(s, "if mount_overlay_kernel 2>&1; then") {
		t.Errorf("default mode is fuse, but auto dispatcher present:\n%s", s)
	}

	// Resume-tar extraction MUST precede the overlay mount, else the
	// upper layer will be restored into an already-mounted overlay and
	// silently land on a different filesystem.
	tarIdx := strings.Index(s, "tar --numeric-owner")
	mountIdx := strings.Index(s, "mount_overlay_fuse || {")
	if tarIdx < 0 || mountIdx < 0 || tarIdx >= mountIdx {
		t.Fatalf("resume-tar extraction must precede overlay mount; tarIdx=%d mountIdx=%d", tarIdx, mountIdx)
	}

	// Handoff MUST be the last command (chroot replaces PID 1).
	trimmed := strings.TrimRight(s, "\n")
	lastLine := trimmed[strings.LastIndex(trimmed, "\n")+1:]
	if !strings.HasPrefix(lastLine, "exec chroot ") {
		t.Errorf("final line must be exec chroot, got %q", lastLine)
	}
}

// TestEntrypointScript_OptionOverrides exercises every EntrypointScriptOptions
// field independently so breakage in the defaults-vs-overrides plumbing is
// localised by the failing subtest name.
func TestEntrypointScript_OptionOverrides(t *testing.T) {
	cases := []struct {
		name  string
		opts  EntrypointScriptOptions
		wants []string
		avoid []string
	}{
		{
			name:  "uppers mount override",
			opts:  EntrypointScriptOptions{UppersMount: "/custom/uppers"},
			wants: []string{"UPPERS_DIR='/custom/uppers'"},
			avoid: []string{"UPPERS_DIR='/mnt/astonish-uppers'"},
		},
		{
			name:  "layers mount override",
			opts:  EntrypointScriptOptions{LayersMount: "/custom/layers"},
			wants: []string{"LAYERS_DIR='/custom/layers'"},
			avoid: []string{"LAYERS_DIR='/mnt/astonish-layers'"},
		},
		{
			name:  "upperdir override",
			opts:  EntrypointScriptOptions{UpperDir: "/tmp/up"},
			wants: []string{"UPPER_DIR='/tmp/up'"},
			avoid: []string{"UPPER_DIR='/var/astonish/upper'"},
		},
		{
			name:  "workdir override",
			opts:  EntrypointScriptOptions{WorkDir: "/tmp/wk"},
			wants: []string{"WORK_DIR='/tmp/wk'"},
			avoid: []string{"WORK_DIR='/var/astonish/work'"},
		},
		{
			name:  "mountpoint override",
			opts:  EntrypointScriptOptions{MountPoint: "/mnt/root"},
			wants: []string{"MOUNT_POINT='/mnt/root'"},
			avoid: []string{"MOUNT_POINT='/sandbox/rootfs'"},
		},
		{
			name:  "handoff binary override",
			opts:  EntrypointScriptOptions{Handoff: "/bin/busybox"},
			wants: []string{`_DEFAULT_HANDOFF='/bin/busybox'`},
			avoid: []string{`_DEFAULT_HANDOFF='/bin/sleep'`},
		},
		{
			name: "handoff args override — multi-arg",
			opts: EntrypointScriptOptions{HandoffArgs: []string{"node", "--flag", "value"}},
			wants: []string{
				`_DEFAULT_HANDOFF_ARGS='node' '--flag' 'value'`,
			},
		},
		{
			name: "handoff args override — args requiring quoting",
			opts: EntrypointScriptOptions{HandoffArgs: []string{"it's fine"}},
			wants: []string{
				`'it'"'"'s fine'`,
			},
		},
		{
			name: "all overrides combined",
			opts: EntrypointScriptOptions{
				UppersMount: "/u",
				LayersMount: "/l",
				UpperDir:    "/ud",
				WorkDir:     "/wd",
				MountPoint:  "/mp",
				Handoff:     "/h",
				HandoffArgs: []string{"x", "y"},
			},
			wants: []string{
				"UPPERS_DIR='/u'",
				"LAYERS_DIR='/l'",
				"UPPER_DIR='/ud'",
				"WORK_DIR='/wd'",
				"MOUNT_POINT='/mp'",
				`_DEFAULT_HANDOFF='/h'`,
				`_DEFAULT_HANDOFF_ARGS='x' 'y'`,
			},
		},
	}
	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			s := EntrypointScript(tc.opts)
			for _, w := range tc.wants {
				if !strings.Contains(s, w) {
					t.Errorf("expected substring %q in script\n---script---\n%s", w, s)
				}
			}
			for _, a := range tc.avoid {
				if strings.Contains(s, a) {
					t.Errorf("unexpected substring %q in script (default not overridden)", a)
				}
			}
		})
	}
}

// TestEntrypointScript_ApplyDefaults verifies the defaulting layer in
// isolation so tests of EntrypointScript don't have to re-check every
// sentinel for every subtest.
func TestEntrypointScript_ApplyDefaults(t *testing.T) {
	var o EntrypointScriptOptions
	o.applyDefaults()
	if o.UppersMount != mountUppers {
		t.Errorf("UppersMount default = %q, want %q", o.UppersMount, mountUppers)
	}
	if o.LayersMount != mountLayers {
		t.Errorf("LayersMount default = %q, want %q", o.LayersMount, mountLayers)
	}
	if o.UpperDir != mountUpper {
		t.Errorf("UpperDir default = %q, want %q", o.UpperDir, mountUpper)
	}
	if o.WorkDir != mountWork {
		t.Errorf("WorkDir default = %q, want %q", o.WorkDir, mountWork)
	}
	if o.MountPoint != "/sandbox/rootfs" {
		t.Errorf("MountPoint default = %q, want /sandbox/rootfs", o.MountPoint)
	}
	if o.Handoff != "/bin/sleep" {
		t.Errorf("Handoff default = %q, want /bin/sleep", o.Handoff)
	}
	if len(o.HandoffArgs) != 1 || o.HandoffArgs[0] != "infinity" {
		t.Errorf("HandoffArgs default = %v, want [infinity]", o.HandoffArgs)
	}
	if o.HostBinaryPath != "/usr/local/bin/astonish-host" {
		t.Errorf("HostBinaryPath default = %q, want /usr/local/bin/astonish-host", o.HostBinaryPath)
	}
	if o.HostTreeSitterLibPath != "/usr/lib/astonish/libastonish-treesitter.so" {
		t.Errorf("HostTreeSitterLibPath default = %q, want /usr/lib/astonish/libastonish-treesitter.so", o.HostTreeSitterLibPath)
	}
	if o.Mode != OverlayModeFuse {
		t.Errorf("Mode default = %q, want %q", o.Mode, OverlayModeFuse)
	}
	if o.EnsureFuseDevice == nil || !*o.EnsureFuseDevice {
		t.Errorf("EnsureFuseDevice default = %v, want &true", o.EnsureFuseDevice)
	}
	if o.BindKernelFS == nil || !*o.BindKernelFS {
		t.Errorf("BindKernelFS default = %v, want &true", o.BindKernelFS)
	}

	// Idempotency: re-applying defaults must not duplicate args or
	// mutate already-set values.
	falseVal := false
	custom := EntrypointScriptOptions{
		UppersMount:      "/x",
		HandoffArgs:      []string{"a"},
		Mode:             OverlayModeKernel,
		EnsureFuseDevice: &falseVal,
		BindKernelFS:     &falseVal,
	}
	custom.applyDefaults()
	custom.applyDefaults()
	if custom.UppersMount != "/x" {
		t.Errorf("applyDefaults mutated explicit UppersMount: %q", custom.UppersMount)
	}
	if len(custom.HandoffArgs) != 1 || custom.HandoffArgs[0] != "a" {
		t.Errorf("applyDefaults mutated explicit HandoffArgs: %v", custom.HandoffArgs)
	}
	if custom.Mode != OverlayModeKernel {
		t.Errorf("applyDefaults mutated explicit Mode: %q", custom.Mode)
	}
	if custom.EnsureFuseDevice == nil || *custom.EnsureFuseDevice {
		t.Errorf("applyDefaults mutated explicit EnsureFuseDevice: %v", custom.EnsureFuseDevice)
	}
	if custom.BindKernelFS == nil || *custom.BindKernelFS {
		t.Errorf("applyDefaults mutated explicit BindKernelFS: %v", custom.BindKernelFS)
	}
}

// TestEntrypointScript_QuotingInjection guards against shell injection
// via option values: every path field flows through shellQuote, which
// uses the POSIX single-quote dance ('…'"'"'…') to escape embedded
// single quotes. A regression here would enable a malicious caller to
// break out of the single-quoted assignment and execute arbitrary
// commands at PID 1 inside the sandbox pod.
func TestEntrypointScript_QuotingInjection(t *testing.T) {
	s := EntrypointScript(EntrypointScriptOptions{
		UppersMount: "/tricky'path",
		Handoff:     "/bin/it's",
	})
	if !strings.Contains(s, `UPPERS_DIR='/tricky'"'"'path'`) {
		t.Errorf("UppersMount single-quote not escaped; got:\n%s", s)
	}
	if !strings.Contains(s, `'/bin/it'"'"'s'`) {
		t.Errorf("Handoff single-quote not escaped; got:\n%s", s)
	}
	// An injection attempt must not materialise as bare shell.
	if strings.Contains(s, "UPPERS_DIR=/tricky'path") {
		t.Errorf("single-quote escaped incorrectly, producing injectable content:\n%s", s)
	}
}

// TestEntrypointScript_HostBinaryBindMount exercises the bind-mount
// of a base-image astonish binary over the overlay's
// /usr/local/bin/astonish. Three behaviours are pinned:
//
//  1. Default path: HostBinaryPath defaults to /usr/local/bin/astonish-host
//     and the bind-mount block appears between overlay mount and handoff.
//  2. Explicit override: a non-standard HostBinaryPath flows through
//     shellQuote and overrides the default.
//  3. Sentinel "-": bind-mount block is OMITTED entirely, matching
//     the pre-bind-mount shape for backward-compat tests.
func TestEntrypointScript_HostBinaryBindMount(t *testing.T) {
	t.Run("default enables bind-mount", func(t *testing.T) {
		s := EntrypointScript(EntrypointScriptOptions{})
		if !strings.Contains(s, "HOST_BIN='/usr/local/bin/astonish-host'") {
			t.Errorf("default HOST_BIN assignment missing; got:\n%s", s)
		}
		if !strings.Contains(s, `mount --bind "$HOST_BIN" "$OVERLAY_BIN"`) {
			t.Errorf("bind-mount command missing; got:\n%s", s)
		}

		// Ordering: bind-mount must come AFTER overlay dispatch and
		// BEFORE handoff. Otherwise the bind destination wouldn't
		// exist yet (no overlay), or PID 1 would handoff without
		// the trusted binary in place. The default mode is fuse, so
		// the post-helpers dispatcher is `mount_overlay_fuse || {`.
		mountIdx := strings.Index(s, "mount_overlay_fuse || {")
		bindIdx := strings.Index(s, `mount --bind "$HOST_BIN"`)
		handoffIdx := strings.Index(s, "exec chroot ")
		if mountIdx < 0 || bindIdx < 0 || handoffIdx < 0 {
			t.Fatalf("missing markers: overlay=%d bind=%d handoff=%d", mountIdx, bindIdx, handoffIdx)
		}
		if !(mountIdx < bindIdx && bindIdx < handoffIdx) {
			t.Errorf("ordering wrong: overlay=%d bind=%d handoff=%d; want overlay < bind < handoff",
				mountIdx, bindIdx, handoffIdx)
		}
	})

	t.Run("custom host binary path", func(t *testing.T) {
		s := EntrypointScript(EntrypointScriptOptions{
			HostBinaryPath: "/opt/astonish/bin/astonish",
		})
		if !strings.Contains(s, "HOST_BIN='/opt/astonish/bin/astonish'") {
			t.Errorf("custom HOST_BIN missing; got:\n%s", s)
		}
		// Default path must NOT leak when overridden.
		if strings.Contains(s, "HOST_BIN='/usr/local/bin/astonish-host'") {
			t.Errorf("default HOST_BIN leaked despite override; got:\n%s", s)
		}
	})

	t.Run("sentinel suppresses bind-mount", func(t *testing.T) {
		s := EntrypointScript(EntrypointScriptOptions{
			HostBinaryPath: "-",
		})
		if strings.Contains(s, "HOST_BIN=") {
			t.Errorf("HOST_BIN assignment should be absent with sentinel; got:\n%s", s)
		}
		if strings.Contains(s, `mount --bind "$HOST_BIN"`) {
			t.Errorf("host-binary bind-mount should be absent with sentinel; got:\n%s", s)
		}
		// Sanity: the rest of the script still emits correctly.
		if !strings.Contains(s, "mount_overlay_fuse") {
			t.Errorf("overlay mount helper missing with sentinel; got:\n%s", s)
		}
		if !strings.Contains(s, `exec chroot "$MOUNT_POINT"`) {
			t.Errorf("handoff missing with sentinel; got:\n%s", s)
		}
	})

	t.Run("fallback when host binary missing", func(t *testing.T) {
		// The generated script must tolerate the base-image binary
		// being absent (e.g. a custom sandbox image that doesn't bake
		// astonish in). The else branch logs to stderr and leaves
		// the overlay's binary intact so the handoff still works if
		// @base shipped its own.
		s := EntrypointScript(EntrypointScriptOptions{})
		if !strings.Contains(s, `if [ -x "$HOST_BIN" ]; then`) {
			t.Errorf("missing executability guard; got:\n%s", s)
		}
		if !strings.Contains(s, `"skipping bind-mount`) {
			t.Errorf("missing fallback warning; got:\n%s", s)
		}
	})
}

// TestEntrypointScript_HostTreeSitterLibBindMount exercises the
// bind-mount of the base-image tree-sitter library into the overlay
// so codeintel tools can dlopen it after chroot.
func TestEntrypointScript_HostTreeSitterLibBindMount(t *testing.T) {
	t.Run("default enables bind-mount", func(t *testing.T) {
		s := EntrypointScript(EntrypointScriptOptions{})
		if !strings.Contains(s, "HOST_TS_LIB='/usr/lib/astonish/libastonish-treesitter.so'") {
			t.Errorf("default HOST_TS_LIB assignment missing; got:\n%s", s)
		}
		if !strings.Contains(s, `mount --bind "$HOST_TS_LIB" "$OVERLAY_TS_LIB"`) {
			t.Errorf("tree-sitter bind-mount command missing; got:\n%s", s)
		}

		binBindIdx := strings.Index(s, `mount --bind "$HOST_BIN"`)
		tsBindIdx := strings.Index(s, `mount --bind "$HOST_TS_LIB"`)
		handoffIdx := strings.Index(s, "exec chroot ")
		if binBindIdx < 0 || tsBindIdx < 0 || handoffIdx < 0 {
			t.Fatalf("missing markers: binBind=%d tsBind=%d handoff=%d", binBindIdx, tsBindIdx, handoffIdx)
		}
		if !(binBindIdx < tsBindIdx && tsBindIdx < handoffIdx) {
			t.Errorf("ordering wrong: binBind=%d tsBind=%d handoff=%d; want binBind < tsBind < handoff",
				binBindIdx, tsBindIdx, handoffIdx)
		}
	})

	t.Run("custom library path", func(t *testing.T) {
		s := EntrypointScript(EntrypointScriptOptions{
			HostTreeSitterLibPath: "/opt/lib/libastonish-treesitter.so",
		})
		if !strings.Contains(s, "HOST_TS_LIB='/opt/lib/libastonish-treesitter.so'") {
			t.Errorf("custom HOST_TS_LIB missing; got:\n%s", s)
		}
		if strings.Contains(s, "HOST_TS_LIB='/usr/lib/astonish/libastonish-treesitter.so'") {
			t.Errorf("default HOST_TS_LIB leaked despite override; got:\n%s", s)
		}
	})

	t.Run("sentinel suppresses bind-mount", func(t *testing.T) {
		s := EntrypointScript(EntrypointScriptOptions{
			HostTreeSitterLibPath: "-",
		})
		if strings.Contains(s, "HOST_TS_LIB=") {
			t.Errorf("HOST_TS_LIB assignment should be absent with sentinel; got:\n%s", s)
		}
		if strings.Contains(s, `mount --bind "$HOST_TS_LIB"`) {
			t.Errorf("tree-sitter bind-mount should be absent with sentinel; got:\n%s", s)
		}
		// Binary bind-mount should still be present by default.
		if !strings.Contains(s, `mount --bind "$HOST_BIN"`) {
			t.Errorf("host-binary bind-mount unexpectedly absent; got:\n%s", s)
		}
	})

	t.Run("fallback when host library missing", func(t *testing.T) {
		s := EntrypointScript(EntrypointScriptOptions{})
		if !strings.Contains(s, `if [ -f "$HOST_TS_LIB" ]; then`) {
			t.Errorf("missing file-existence guard; got:\n%s", s)
		}
		if !strings.Contains(s, `"skipping tree-sitter library bind-mount`) {
			t.Errorf("missing fallback warning; got:\n%s", s)
		}
	})
}

// TestEntrypointScript_OverlayModes pins the dispatcher emitted for
// each OverlayMode, plus the EnsureFuseDevice knob.
//
// Rationale: the dispatcher shape is the load-bearing behavioural
// contract between this generator and the cluster. A silent regression
// (e.g. emitting the fuse dispatcher for a kernel-mode deployment)
// would result in pods that only work on one kind of cluster.
func TestEntrypointScript_OverlayModes(t *testing.T) {
	t.Run("fuse mode (explicit)", func(t *testing.T) {
		s := EntrypointScript(EntrypointScriptOptions{Mode: OverlayModeFuse})
		if !strings.Contains(s, "OVERLAY_MODE='fuse'") {
			t.Errorf("OVERLAY_MODE marker wrong; got:\n%s", s)
		}
		if !strings.Contains(s, "mount_overlay_fuse || {") {
			t.Errorf("fuse dispatcher missing; got:\n%s", s)
		}
		if strings.Contains(s, "mount_overlay_kernel || {") {
			t.Errorf("kernel-only dispatcher leaked; got:\n%s", s)
		}
		if strings.Contains(s, "if mount_overlay_kernel 2>&1; then") {
			t.Errorf("auto dispatcher leaked; got:\n%s", s)
		}
	})

	t.Run("kernel mode", func(t *testing.T) {
		s := EntrypointScript(EntrypointScriptOptions{Mode: OverlayModeKernel})
		if !strings.Contains(s, "OVERLAY_MODE='kernel'") {
			t.Errorf("OVERLAY_MODE marker wrong; got:\n%s", s)
		}
		if !strings.Contains(s, "mount_overlay_kernel || {") {
			t.Errorf("kernel dispatcher missing; got:\n%s", s)
		}
		// userxattr option is what makes kernel overlayfs work in a
		// user namespace (K8s hostUsers: false). It's a no-op in
		// privileged mode, so emitting it unconditionally is safe.
		if !strings.Contains(s, "userxattr,lowerdir=$LOWER") {
			t.Errorf("userxattr option missing; got:\n%s", s)
		}
		if strings.Contains(s, "mount_overlay_fuse || {") {
			t.Errorf("fuse dispatcher leaked into kernel mode; got:\n%s", s)
		}
	})

	t.Run("auto mode", func(t *testing.T) {
		s := EntrypointScript(EntrypointScriptOptions{Mode: OverlayModeAuto})
		if !strings.Contains(s, "OVERLAY_MODE='auto'") {
			t.Errorf("OVERLAY_MODE marker wrong; got:\n%s", s)
		}
		if !strings.Contains(s, "if mount_overlay_kernel 2>&1; then") {
			t.Errorf("auto dispatcher (kernel-first) missing; got:\n%s", s)
		}
		if !strings.Contains(s, "elif mount_overlay_fuse; then") {
			t.Errorf("auto fallback missing; got:\n%s", s)
		}
		// Operator-observable log lines so the chosen path is
		// visible in pod stdout.
		if !strings.Contains(s, "composed via kernel overlayfs") {
			t.Errorf("success-via-kernel log missing; got:\n%s", s)
		}
		if !strings.Contains(s, "composed via fuse-overlayfs (kernel path failed)") {
			t.Errorf("fallback log missing; got:\n%s", s)
		}
	})

	t.Run("EnsureFuseDevice enabled emits mknod", func(t *testing.T) {
		tr := true
		s := EntrypointScript(EntrypointScriptOptions{
			Mode:             OverlayModeFuse,
			EnsureFuseDevice: &tr,
		})
		if !strings.Contains(s, "mknod /dev/fuse c 10 229") {
			t.Errorf("mknod fallback missing; got:\n%s", s)
		}
		if !strings.Contains(s, "chmod 0666 /dev/fuse") {
			t.Errorf("chmod 0666 /dev/fuse missing; got:\n%s", s)
		}
	})

	t.Run("EnsureFuseDevice disabled omits mknod", func(t *testing.T) {
		fv := false
		s := EntrypointScript(EntrypointScriptOptions{
			Mode:             OverlayModeFuse,
			EnsureFuseDevice: &fv,
		})
		if strings.Contains(s, "mknod /dev/fuse") {
			t.Errorf("mknod must be absent when EnsureFuseDevice=false (device plugin path); got:\n%s", s)
		}
		if strings.Contains(s, "chmod 0666 /dev/fuse") {
			t.Errorf("chmod must be absent when EnsureFuseDevice=false; got:\n%s", s)
		}
		// The fuse helper itself MUST still be emitted — only the
		// mknod inside it is skipped.
		if !strings.Contains(s, "fuse-overlayfs \\") {
			t.Errorf("fuse-overlayfs invocation missing; got:\n%s", s)
		}
	})
}

// TestEntrypointScript_BindKernelFS exercises the Section 2b kernel-
// filesystem bind-mount block which makes /dev (including /dev/ptmx),
// /proc, and /sys visible inside the chroot.
func TestEntrypointScript_BindKernelFS(t *testing.T) {
	t.Run("default emits bind-mount block", func(t *testing.T) {
		s := EntrypointScript(EntrypointScriptOptions{})
		if !strings.Contains(s, "for _src in /dev /proc /sys") {
			t.Errorf("bind-mount for-loop missing in default script; got:\n%s", s)
		}
		if !strings.Contains(s, "mount --rbind") {
			t.Errorf("mount --rbind missing in default script; got:\n%s", s)
		}
		if !strings.Contains(s, "mount --make-rslave") {
			t.Errorf("mount --make-rslave missing in default script; got:\n%s", s)
		}
		if !strings.Contains(s, `"$MOUNT_POINT$_src"`) {
			t.Errorf("target path construction missing in default script; got:\n%s", s)
		}
		// The section header must be present.
		if !strings.Contains(s, "--- 2b.") {
			t.Errorf("section 2b header missing; got:\n%s", s)
		}
	})

	t.Run("pre-mount mkdir in upper dir", func(t *testing.T) {
		s := EntrypointScript(EntrypointScriptOptions{})
		// The pre-mount mkdir must create dirs in UPPER_DIR before the
		// overlay is composed (fuse-overlayfs snapshots at mount time).
		if !strings.Contains(s, `mkdir -p "$UPPER_DIR/dev" "$UPPER_DIR/proc" "$UPPER_DIR/sys"`) {
			t.Errorf("pre-mount upper-dir mkdir missing; got:\n%s", s)
		}
		// Must appear BEFORE the overlay mount helpers.
		mkdirIdx := strings.Index(s, `"$UPPER_DIR/dev"`)
		overlayIdx := strings.Index(s, "mount_overlay_fuse() {")
		if mkdirIdx >= overlayIdx {
			t.Errorf("pre-mount mkdir (idx %d) must appear before overlay mount (idx %d)", mkdirIdx, overlayIdx)
		}
	})

	t.Run("BindKernelFS=false suppresses block", func(t *testing.T) {
		falseVal := false
		s := EntrypointScript(EntrypointScriptOptions{
			BindKernelFS: &falseVal,
		})
		if strings.Contains(s, "for _src in /dev /proc /sys") {
			t.Errorf("bind-mount block present despite BindKernelFS=false; got:\n%s", s)
		}
		if strings.Contains(s, "--- 2b.") {
			t.Errorf("section 2b header present despite BindKernelFS=false; got:\n%s", s)
		}
		if strings.Contains(s, `"$UPPER_DIR/dev"`) {
			t.Errorf("pre-mount upper-dir mkdir present despite BindKernelFS=false; got:\n%s", s)
		}
	})

	t.Run("idempotent guard uses mountinfo", func(t *testing.T) {
		s := EntrypointScript(EntrypointScriptOptions{})
		if !strings.Contains(s, "/proc/self/mountinfo") {
			t.Errorf("idempotent mountinfo guard missing; got:\n%s", s)
		}
	})

	t.Run("resolv.conf bind-mount for DNS", func(t *testing.T) {
		s := EntrypointScript(EntrypointScriptOptions{})
		if !strings.Contains(s, `/etc/resolv.conf`) {
			t.Errorf("resolv.conf bind-mount missing in default script; got:\n%s", s)
		}
		if !strings.Contains(s, `mount --bind "$_RESOLV_SRC" "$_RESOLV_DST"`) {
			t.Errorf("resolv.conf mount --bind command missing; got:\n%s", s)
		}
	})

	t.Run("BindKernelFS=false suppresses resolv.conf", func(t *testing.T) {
		falseVal := false
		s := EntrypointScript(EntrypointScriptOptions{
			BindKernelFS: &falseVal,
		})
		if strings.Contains(s, `_RESOLV_SRC`) {
			t.Errorf("resolv.conf block present despite BindKernelFS=false; got:\n%s", s)
		}
	})
}

// TestEntrypointScript_PreSeedUpperDirs verifies the EXDEV-mitigation
// pre-seed block that creates first-level directories from the lowest
// layer in the upper before composing the overlay. Key invariants:
//   - BOTTOM_LAYER is extracted from the reversed $LOWER variable
//   - Symlinks are skipped (must not turn /bin→usr/bin into a directory)
//   - Existing upper entries are not clobbered (resume path)
//   - The block appears AFTER awk reversal and BEFORE mount helpers
func TestEntrypointScript_PreSeedUpperDirs(t *testing.T) {
	s := EntrypointScript(EntrypointScriptOptions{})

	t.Run("bottom layer extraction", func(t *testing.T) {
		// Uses ${LOWER##*:} to get the last colon-separated entry (bottommost layer).
		if !strings.Contains(s, `BOTTOM_LAYER="${LOWER##*:}"`) {
			t.Errorf("BOTTOM_LAYER extraction missing; got:\n%s", s)
		}
	})

	t.Run("symlink guard", func(t *testing.T) {
		// Must skip symlinks to avoid turning /bin→usr/bin into a real dir.
		if !strings.Contains(s, `[ -L "${_d%/}" ] && continue`) {
			t.Errorf("symlink guard missing; got:\n%s", s)
		}
	})

	t.Run("idempotent existing check", func(t *testing.T) {
		// Must not clobber existing upper entries (from resume path).
		if !strings.Contains(s, `if [ ! -d "$UPPER_DIR/$_name" ]; then`) {
			t.Errorf("existing-check guard missing; got:\n%s", s)
		}
	})

	t.Run("precedes mount helpers", func(t *testing.T) {
		preSeedIdx := strings.Index(s, `BOTTOM_LAYER=`)
		mountFuseIdx := strings.Index(s, "mount_overlay_fuse() {")
		if preSeedIdx < 0 || mountFuseIdx < 0 || preSeedIdx >= mountFuseIdx {
			t.Errorf("pre-seed (idx=%d) must precede fuse mount helper (idx=%d)", preSeedIdx, mountFuseIdx)
		}
	})

	t.Run("follows LOWER computation", func(t *testing.T) {
		lowerIdx := strings.Index(s, `awk -F, -v dir="$LAYERS_DIR"`)
		preSeedIdx := strings.Index(s, `BOTTOM_LAYER=`)
		if lowerIdx < 0 || preSeedIdx < 0 || lowerIdx >= preSeedIdx {
			t.Errorf("pre-seed (idx=%d) must follow LOWER computation (idx=%d)", preSeedIdx, lowerIdx)
		}
	})
}

// TestEntrypointScript_OrderingInvariants pins the section order so
// refactors don't silently reshuffle steps that have causal dependencies
// (resume must precede mount; mount must precede handoff).
func TestEntrypointScript_OrderingInvariants(t *testing.T) {
	// Default (fuse) mode shape.
	s := EntrypointScript(EntrypointScriptOptions{})

	markers := []string{
		"set -eu",
		`: "${ASTONISH_SESSION_ID`,
		`: "${ASTONISH_LAYER_CHAIN`,
		"UPPERS_DIR=",
		"LAYERS_DIR=",
		"OVERLAY_MODE=",
		"RESUME_TAR=",
		"tar --numeric-owner",
		`mkdir -p "$UPPER_DIR/dev" "$UPPER_DIR/proc" "$UPPER_DIR/sys"`,
		`awk -F, -v dir="$LAYERS_DIR"`,
		`BOTTOM_LAYER="${LOWER##*:}"`,
		"mount_overlay_kernel() {",
		"mount_overlay_fuse() {",
		"mount_overlay_fuse || {",
		"HOST_BIN=",
		"for _src in /dev /proc /sys",
		"exec chroot",
	}
	prev := -1
	for _, m := range markers {
		idx := strings.Index(s, m)
		if idx < 0 {
			t.Fatalf("marker %q missing from script", m)
		}
		if idx <= prev {
			t.Errorf("marker %q appears at index %d, must be after previous marker at %d", m, idx, prev)
		}
		prev = idx
	}
}
