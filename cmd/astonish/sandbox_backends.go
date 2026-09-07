// Sandbox backend link-time registrations.
//
// Each backend in pkg/sandbox/* self-registers with sandbox.NewBackend via
// an init() function. For that init to run, the package must be linked
// into the binary, which requires an import somewhere in the import graph.
//
// Docker OverlayFS is the local default (blank-imported below). Kubernetes,
// OpenShell, and mock are optional, used only when the deployment selects
// them via BackendFactoryConfig.Kind. Blank-importing them here makes the
// astonish CLI a "batteries-included" distribution: all built-in backends
// are available without changes to config or build tags.
//
// Binaries that want to exclude a backend (e.g., to avoid linking
// k8s.io/client-go) can fork this list. Out-of-tree backends follow the
// same pattern: import your package to trigger its init().
package astonish

import (
	// Register the Docker backend (local OverlayFS sessions).
	_ "github.com/SAP/astonish/pkg/sandbox/docker"

	// Register the k8s backend with sandbox.NewBackend so that
	// configurations setting backend=k8s succeed.
	_ "github.com/SAP/astonish/pkg/sandbox/k8s"

	// Register the mock backend so tests and demos that request
	// backend=mock succeed without a separate build tag.
	_ "github.com/SAP/astonish/pkg/sandbox/mock"

	// Register the openshell backend so configurations setting
	// backend=openshell succeed. Links the NVIDIA OpenShell gRPC
	// client, gateway connection, and init-container support.
	_ "github.com/SAP/astonish/pkg/sandbox/openshell"
)
