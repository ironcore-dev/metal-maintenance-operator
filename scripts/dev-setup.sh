#!/bin/bash

# SPDX-FileCopyrightText: 2025 SAP SE or an SAP affiliate company and IronCore contributors
# SPDX-License-Identifier: Apache-2.0

# Development setup script for maintenance-operator
# This script sets up a local development environment with Tilt

set -e

# Ensure we're in the project root directory
SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
PROJECT_ROOT="$(cd "${SCRIPT_DIR}/.." && pwd)"
cd "$PROJECT_ROOT"

echo "Working from project root: $(pwd)"

# Colors for output
RED='\033[0;31m'
GREEN='\033[0;32m'
YELLOW='\033[1;33m'
BLUE='\033[0;34m'
NC='\033[0m' # No Color

# Configuration
LOCAL_REGISTRY_NAME="dev-registry"
LOCAL_REGISTRY_PORT="${REGISTRY_PORT:-5000}"
NAMESPACE="${DEV_NAMESPACE:-maintenance-operator-system}"

print_step() {
    echo -e "${BLUE}==>${NC} $1"
}

print_success() {
    echo -e "${GREEN}✓${NC} $1"
}

print_warning() {
    echo -e "${YELLOW}⚠${NC} $1"
}

print_error() {
    echo -e "${RED}✗${NC} $1"
}

check_prerequisites() {
    print_step "Checking prerequisites..."

    # Check if kubectl is available
    if ! command -v kubectl &> /dev/null; then
        print_error "kubectl not found. Please install kubectl."
        exit 1
    fi

    # Check if docker is available
    if ! command -v docker &> /dev/null; then
        print_error "docker not found. Please install Docker."
        exit 1
    fi

    # Check if tilt is available
    if ! command -v tilt &> /dev/null; then
        print_error "tilt not found. Please install Tilt from https://docs.tilt.dev/install.html"
        exit 1
    fi

    # Check if we can connect to Kubernetes
    if ! kubectl cluster-info &> /dev/null; then
        print_error "Cannot connect to Kubernetes cluster. Please check your kubeconfig."
        exit 1
    fi

    print_success "All prerequisites met"
}

setup_local_registry() {
    # Check if user wants to skip registry setup
    if [ "${SKIP_REGISTRY:-false}" = "true" ]; then
        print_warning "Skipping local registry setup (SKIP_REGISTRY=true)"
        return
    fi

    print_step "Setting up local Docker registry..."

    # Check if registry is already running
    if docker ps --format "table {{.Names}}" | grep -q "^${LOCAL_REGISTRY_NAME}$"; then
        print_warning "Local registry '${LOCAL_REGISTRY_NAME}' is already running"
        return
    fi

    # Check if port is already in use by another process
    if command -v lsof >/dev/null 2>&1 && lsof -Pi :${LOCAL_REGISTRY_PORT} -sTCP:LISTEN -t >/dev/null 2>&1; then
        print_warning "Port ${LOCAL_REGISTRY_PORT} is already in use. Assuming registry is available."
        print_warning "If you want to use a different registry, set REGISTRY environment variable."
        return
    elif command -v netstat >/dev/null 2>&1 && netstat -ln | grep ":${LOCAL_REGISTRY_PORT} " >/dev/null 2>&1; then
        print_warning "Port ${LOCAL_REGISTRY_PORT} is already in use. Assuming registry is available."
        print_warning "If you want to use a different registry, set REGISTRY environment variable."
        return
    fi

    # Check if registry container exists but is stopped
    if docker ps -a --format "table {{.Names}}" | grep -q "^${LOCAL_REGISTRY_NAME}$"; then
        print_step "Starting existing registry container..."
        docker start ${LOCAL_REGISTRY_NAME}
    else
        print_step "Creating new registry container..."
        docker run -d \
            --name ${LOCAL_REGISTRY_NAME} \
            --restart=always \
            -p ${LOCAL_REGISTRY_PORT}:5000 \
            registry:2
    fi

    print_success "Local registry running on localhost:${LOCAL_REGISTRY_PORT}"
}

setup_namespace() {
    print_step "Setting up Kubernetes namespace..."

    if kubectl get namespace ${NAMESPACE} &> /dev/null; then
        print_warning "Namespace '${NAMESPACE}' already exists"
    else
        kubectl create namespace ${NAMESPACE}
        print_success "Created namespace '${NAMESPACE}'"
    fi
}

start_tilt() {
    print_step "Starting Tilt development environment..."

    # Allow custom registry from environment
    REGISTRY=${REGISTRY:-"localhost:${LOCAL_REGISTRY_PORT}"}
    NAMESPACE=${NAMESPACE:-"${NAMESPACE}"}
    SKIP_REGISTRY=${SKIP_REGISTRY:-"false"}

    print_step "Using registry: ${REGISTRY}"
    print_step "Using namespace: ${NAMESPACE}"

    # Build Tilt configuration arguments (these go after --)
    TILT_CONFIG_ARGS=""
    if [ "${REGISTRY}" != "localhost:${LOCAL_REGISTRY_PORT}" ]; then
        TILT_CONFIG_ARGS="${TILT_CONFIG_ARGS} --registry=${REGISTRY}"
    fi
    if [ "${NAMESPACE}" != "maintenance-operator-system" ]; then
        TILT_CONFIG_ARGS="${TILT_CONFIG_ARGS} --namespace=${NAMESPACE}"
    fi
    if [ "${SKIP_REGISTRY}" = "true" ]; then
        TILT_CONFIG_ARGS="${TILT_CONFIG_ARGS} --skip-registry-setup=true"
    fi

    # Ensure we're in the project root directory (where Tiltfile is located)
    PROJECT_ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
    cd "${PROJECT_ROOT}"

    # Start Tilt with proper argument syntax
    if [ -n "${TILT_CONFIG_ARGS}" ]; then
        print_step "Starting Tilt with configuration:${TILT_CONFIG_ARGS}"
        print_step "Working directory: $(pwd)"
        # Pass config args after -- separator
        # shellcheck disable=SC2086
        tilt up -- ${TILT_CONFIG_ARGS}
    else
        print_step "Starting Tilt with default configuration"
        print_step "Working directory: $(pwd)"
        tilt up
    fi
}

cleanup() {
    print_step "Cleaning up development environment..."

    # Stop Tilt if running
    if pgrep -f "tilt" > /dev/null; then
        print_step "Stopping Tilt..."
        tilt down
    fi

    # Stop local registry
    if docker ps --format "table {{.Names}}" | grep -q "^${LOCAL_REGISTRY_NAME}$"; then
        print_step "Stopping local registry..."
        docker stop ${LOCAL_REGISTRY_NAME}
        docker rm ${LOCAL_REGISTRY_NAME}
    fi

    print_success "Cleanup completed"
}

show_help() {
    echo "Development setup script for maintenance-operator"
    echo ""
    echo "Usage: $0 [COMMAND]"
    echo ""
    echo "Commands:"
    echo "  start     Set up and start development environment (default)"
    echo "  stop      Stop and cleanup development environment"
    echo "  status    Show status of development components"
    echo "  help      Show this help message"
    echo ""
    echo "Environment variables:"
    echo "  REGISTRY         Docker registry to use (default: localhost:5000)"
    echo "  REGISTRY_PORT    Port for local registry (default: 5000)"
    echo "  DEV_NAMESPACE    Kubernetes namespace (default: maintenance-operator-system)"
    echo "  SKIP_REGISTRY    Skip local registry setup (default: false)"
    echo ""
    echo "Examples:"
    echo "  # Use existing registry"
    echo "  REGISTRY=my-registry.com/username SKIP_REGISTRY=true $0 start"
    echo ""
    echo "  # Use custom namespace"
    echo "  DEV_NAMESPACE=my-dev-namespace $0 start"
    echo ""
    echo "  # Use existing registry on different port"
    echo "  REGISTRY=localhost:5001 SKIP_REGISTRY=true $0 start"
}

show_status() {
    print_step "Development environment status:"

    # Check registry
    if docker ps --format "table {{.Names}}" | grep -q "^${LOCAL_REGISTRY_NAME}$"; then
        print_success "Local registry is running"
    else
        print_warning "Local registry is not running"
    fi

    # Check namespace
    if kubectl get namespace ${NAMESPACE} &> /dev/null; then
        print_success "Namespace '${NAMESPACE}' exists"
    else
        print_warning "Namespace '${NAMESPACE}' does not exist"
    fi

    # Check if Tilt is running
    if pgrep -f "tilt" > /dev/null; then
        print_success "Tilt is running"
    else
        print_warning "Tilt is not running"
    fi
}

main() {
    case "${1:-start}" in
        start)
            check_prerequisites
            setup_local_registry
            setup_namespace
            start_tilt
            ;;
        stop)
            cleanup
            ;;
        status)
            show_status
            ;;
        help|--help|-h)
            show_help
            ;;
        *)
            print_error "Unknown command: $1"
            show_help
            exit 1
            ;;
    esac
}

main "$@"
