# Development Guide

This guide covers setting up a development environment for the Maintenance Operator project.

## Prerequisites

- go version v1.24.0+
- docker version 17.03+
- kubectl version v1.11.3+
- Access to a Kubernetes v1.11.3+ cluster
- **Install Tilt**: Follow the [Tilt installation guide](https://docs.tilt.dev/install.html)

## Quick Development Setup

For fast local development with hot reloading, we recommend using [Tilt](https://tilt.dev/):

### Option 1: Helper Script (Recommended)
```bash
./hack/dev-setup.sh start
```

### Option 2: Manual Tilt Setup
```bash
tilt up
```

### Option 3: Using an Existing Registry
```bash
# With environment variables
REGISTRY=localhost:5001 SKIP_REGISTRY=true ./hack/dev-setup.sh start

# Or with Tilt directly
tilt up -- --registry=localhost:5001 --skip-registry-setup=true
```

### What This Does:
- Set up a local Docker registry (unless you skip it)
- Build and deploy the operator with live reload
- Provide a web UI at http://localhost:10350
- Forward metrics ports (8080) and health endpoints (8081)

## Manual Installation (Alternative to Tilt)

If you prefer not to use Tilt, you can set up the development environment manually:

1. **Install the CRDs**:
```bash
make install
```

2. **Deploy the controller**:
```bash
make deploy IMG=ghcr.io/ironcore-dev/maintenance-operator:latest
```

## Detailed Development with Tilt

This project includes a [Tiltfile](https://tilt.dev/) for fast local development cycles. Tilt provides automatic rebuilds, hot reloading, and easy log streaming for Kubernetes development.

### Additional Prerequisites for Tilt

1. **Kubernetes cluster**: Ensure you have a local Kubernetes cluster running (kind, minikube, Docker Desktop, etc.)
2. **Docker registry**: For pushing images (can use a local registry)
3. **kubectl**: Configured to access your cluster

### Manual Registry Setup (Optional)

If you want to start a local Docker registry manually:

```bash
docker run -d -p 5000:5000 --name registry registry:2
```

### Open the Tilt UI

Navigate to http://localhost:10350 to see the development dashboard.

## Configuration

You can customize the deployment with command-line arguments or environment variables:

### Using Tilt directly:
```bash
# Use a custom registry
tilt up -- --registry=your-registry.com/username

# Use a custom namespace
tilt up -- --namespace=my-dev-namespace

# Skip local registry setup (if you have an existing one)
tilt up -- --skip-registry-setup=true

# Combine multiple options
tilt up -- --registry=my-registry.com/user --namespace=my-dev --skip-registry-setup=true
```

### Using the helper script:
```bash
# Use existing registry
REGISTRY=my-registry.com/username SKIP_REGISTRY=true ./hack/dev-setup.sh start

# Use custom namespace
DEV_NAMESPACE=my-dev-namespace ./hack/dev-setup.sh start

# Use existing registry on different port
REGISTRY=localhost:5001 SKIP_REGISTRY=true ./hack/dev-setup.sh start
```

## Development Workflow

### Hot Reloading

The Tiltfile is configured for fast development cycles:

- **Code changes** in `./cmd/`, `./internal/`, `./api/`, or `./main.go` trigger automatic rebuilds
- **Live updates** sync changed files directly into the running container
- **Binary recompilation** happens inside the container for faster feedback

### Available Resources

In the Tilt UI, you'll see these resources:

**Core Components:**
- **maintenance-operator**: Main controller deployment
- **cert-manager**: TLS certificate management (auto-deployed)
- **boot-operator**: Server boot configuration management (auto-deployed)
- **metal-operator**: Metal server management (auto-deployed)
- **logs**: Live stream of controller logs

**Sample Resources:**
- **apply-samples**: Deploy sample AnsibleJob resources (manual trigger)
- **cleanup-samples**: Remove sample resources (manual trigger)
- **cleanup-all**: Complete cleanup including cert-manager (manual trigger)

### Port Forwards

The Tilt setup automatically forwards these ports:

- `localhost:8080` → maintenance-operator metrics endpoint
- `localhost:8081` → maintenance-operator health/readiness endpoint

### Viewing Logs

You can view logs in multiple ways:

1. **Tilt UI**: Click on the "logs" resource
2. **Terminal**: Use the logs resource that streams `kubectl logs`
3. **Direct kubectl**: `kubectl logs -f deployment/maintenance-operator-controller-manager -n maintenance-operator-system`

## Testing with Sample Resources

1. **Deploy samples**:
   ```bash
   # In Tilt UI: click the "apply-samples" resource trigger button
   # Or manually:
   kubectl apply -k config/samples/
   ```

2. **Check AnsibleJob status**:
   ```bash
   kubectl get ansiblejob -o wide
   kubectl describe ansiblejob sample-ansible-job
   ```

3. **Clean up samples**:
   ```bash
   # In Tilt UI: click the "cleanup" resource trigger button
   # Or manually:
   kubectl delete -k config/samples/
   ```

## Advanced Usage

### Custom Kubernetes Manifests

To test with custom manifests:

1. Create your AnsibleJob YAML files
2. Apply them directly: `kubectl apply -f your-ansiblejob.yaml`
3. Watch them in the Tilt UI or through kubectl

### Debugging

1. **Container debugging**: Tilt supports connecting debuggers to running containers
2. **Resource inspection**: Use the Tilt UI to inspect Kubernetes resources
3. **Log analysis**: Filter and search logs in the Tilt UI

### Building Only

To build the image without deploying:

```bash
tilt build maintenance-operator
```

## Stopping Development

To stop the development environment:

```bash
tilt down
```

This will:
- Stop all running resources
- Clean up port forwards
- Keep your built images (unless you specify cleanup flags)

## Tips

1. **First run**: The first `tilt up` may take longer as it builds the initial image
2. **Incremental builds**: Subsequent runs are much faster due to Docker layer caching
3. **Resource dependencies**: Tilt automatically handles resource dependencies (CRDs → deployment)
4. **File watching**: Only relevant file changes trigger rebuilds, making development efficient

## Troubleshooting

### Common Issues

1. **Permission errors**: Ensure your kubectl context has sufficient permissions
2. **Registry access**: Make sure your Kubernetes cluster can pull from your specified registry
3. **Port conflicts**: If ports 8080/8081 are in use, they'll be automatically reassigned

### Useful Commands

```bash
# Check Tilt status
tilt status

# View all resources
tilt get all

# Restart a specific resource
tilt trigger maintenance-operator

# View resource logs
tilt logs maintenance-operator
```

For more information, see the [Tilt documentation](https://docs.tilt.dev/).
