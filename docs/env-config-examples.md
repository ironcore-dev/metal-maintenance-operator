# Development Environment Configuration Examples
# Use these environment variables to customize the development setup

# Docker Registry Configuration
# Use an existing registry instead of setting up a local one
# REGISTRY=your-registry.com/username
# SKIP_REGISTRY=true

# Use localhost registry on different port
# REGISTRY=localhost:5001
# REGISTRY_PORT=5001

# Kubernetes Configuration
# DEV_NAMESPACE=my-dev-namespace

# Tilt Configuration Examples:
# tilt up -- --registry=your-registry.com/username --skip-registry-setup=true
# tilt up -- --namespace=my-dev-namespace
# tilt up -- --registry=localhost:5001 --skip-registry-setup=true --namespace=my-dev
