# Development with Tilt

This project includes a [Tiltfile](https://tilt.dev/) for fast local development cycles. Tilt provides automatic rebuilds, hot reloading, and easy log streaming for Kubernetes development.

## Prerequisites

1. **Install Tilt**: Follow the [Tilt installation guide](https://docs.tilt.dev/install.html)
2. **Kubernetes cluster**: Ensure you have a local Kubernetes cluster running (kind, minikube, Docker Desktop, etc.)
3. **Docker registry**: For pushing images (can use a local registry)
4. **kubectl**: Configured to access your cluster

## Quick Start

1. **Start a local Docker registry** (optional, if not using an external registry):
   ```bash
   docker run -d -p 5000:5000 --name registry registry:2
   ```

2. **Start Tilt**:
   ```bash
   tilt up
   ```

3. **Open the Tilt UI**: Navigate to http://localhost:10350 to see the development dashboard

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
REGISTRY=my-registry.com/username SKIP_REGISTRY=true ./scripts/dev-setup.sh start

# Use custom namespace
DEV_NAMESPACE=my-dev-namespace ./scripts/dev-setup.sh start

# Use existing registry on different port
REGISTRY=localhost:5001 SKIP_REGISTRY=true ./scripts/dev-setup.sh start
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
