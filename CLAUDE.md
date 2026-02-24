# CLAUDE.md

This file provides guidance to Claude Code (claude.ai/code) when working with code in this repository.

## Project Overview

The maintenance-operator is a Kubernetes operator built with Kubebuilder that manages server hardware through vendor management consoles (Dell OpenManage Enterprise, HPE OneView, Lenovo XClarity). It automatically imports physical servers from the metal-operator into these vendor management systems using their respective APIs.

**Domain**: `metal.ironcore.dev`
**API Group**: `maintenance.metal.ironcore.dev`
**Primary CRD**: `Console` (v1alpha1)

## Architecture

### Core Components

**API (api/v1alpha1/)**
- `Console` CRD: Defines a vendor management console and server selector
  - Spec fields: ServerSelector (label selector), ConsoleURL, Manufacturer, BMCCredentialSecretRef
  - Status fields: ManagedServers, UnmanagedServers, TotalServers
  - The operator reconciles Console resources and ensures selected servers are imported into the management console

**Controller (internal/controller/)**
- `ConsoleReconciler`: Main reconciliation logic that:
  - Lists servers matching the Console's label selector (from metal-operator Server CRD)
  - Creates a hardware manager client for the specific vendor
  - Imports servers that aren't already managed by the console
  - Updates Console status with counts of managed/unmanaged servers
  - Watches both Console and Server resources (Server changes trigger Console reconciliation)

**Hardware Manager Package (internal/hwmgr/)**
- Abstraction layer for vendor-specific management console APIs
- `ClientInterface`: Common interface with ImportServer, RemoveServer, ListServers, GetAuthToken methods
- `Client`: Factory wrapper that creates vendor-specific clients
- Vendor implementations:
  - **Dell**: Uses Dell OpenManage Enterprise (OME) REST API for server discovery/management
  - **HPE**: Uses HPE OneView golang SDK for rack server management
  - **Lenovo**: Uses Lenovo XClarity REST API for node management
- Each vendor client handles authentication differently (Dell/Lenovo use session tokens, HPE uses API keys)
- HTTP client shared via `httpclient.go` with TLS support and Bearer token auth

### Dependencies

- **metal-operator**: Provides the `Server` and `BMC` CRDs that this operator watches
- Integrates with metal-operator's BMC resource to get server IP addresses and credentials

## Common Commands

### Build and Run
```bash
make build                    # Build manager binary to bin/manager
make run                      # Run controller locally (manifests, generate, fmt, vet first)
go run ./cmd/main.go          # Direct run without make targets
```

### Code Generation
```bash
make generate                 # Generate DeepCopy, DeepCopyInto, DeepCopyObject methods
make manifests                # Generate CRDs, RBAC, and webhook configs
```

### Testing
```bash
make test                     # Run unit tests with coverage (generates cover.out)
make test-e2e                 # Run e2e tests in Kind cluster (auto-creates cluster)
make setup-test-e2e           # Create Kind cluster for e2e tests only
make cleanup-test-e2e         # Delete Kind e2e test cluster
```

### Code Quality
```bash
make fmt                      # Format code with goimports
make vet                      # Run go vet
make lint                     # Run golangci-lint
make lint-fix                 # Run golangci-lint with auto-fix
make add-license              # Add Apache 2.0 license headers to all Go files
make check-license            # Verify all Go files have license headers
make check                    # Full check: generate, manifests, add-license, fmt, lint, test
```

### Docker
```bash
make docker-build IMG=<registry>/maintenance-operator:tag
make docker-push IMG=<registry>/maintenance-operator:tag
make docker-buildx IMG=<registry>/maintenance-operator:tag  # Multi-platform build
```

### Deployment
```bash
make install                  # Install CRDs to cluster
make deploy IMG=<registry>/maintenance-operator:tag
make uninstall                # Remove CRDs from cluster
make undeploy                 # Remove controller from cluster
kubectl apply -k config/samples/   # Apply sample Console resources
```

### Documentation
```bash
make docs                     # Generate API reference docs to docs/api-reference/api.md
```

## Key Implementation Details

### Server Import Flow
1. Console resource created with ServerSelector and vendor details
2. Controller lists metal-operator Server resources matching the selector
3. For each unmanaged server:
   - Fetches BMC resource to get server IP
   - Fetches BMC secret for credentials
   - Constructs hostname as `{node}r-{rack}.cc.qa-de-1.cloud.sap` pattern
   - Calls vendor-specific ImportServer with BMC IP and credentials
4. Updates Console status with managed/unmanaged counts
5. Requeues if any servers remain unmanaged

### Authentication Token Management
- Console credentials stored in Kubernetes Secret referenced by BMCCredentialSecretRef
- Secret contains: `username`, `password`, and optionally `token`
- Controller updates the `token` field in the Secret after authentication
- Each vendor client implements GetAuthToken() which validates existing tokens or creates new ones

### Hostname Convention
The controller uses a specific hostname pattern for SAP Cloud infrastructure: `{node}r-{rack}.cc.qa-de-1.cloud.sap`, constructed by parsing the Server name (expected format: `{node}-{rack}-...`).

### Tool Installation
All tools are installed to `./bin/` directory with version tracking (e.g., `controller-gen-v0.18.0`). The Makefile uses symbolic links to reference the correct versioned binary.
