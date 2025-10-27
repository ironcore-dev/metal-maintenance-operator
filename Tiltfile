# Tiltfile for maintenance-operator development
# A simplified, clean Tiltfile built from scratch

# Configuration
REGISTRY = "localhost:5000"
NAMESPACE = "maintenance-operator-system"
IMG = REGISTRY + "/maintenance-operator:latest"
CERT_MANAGER_VERSION = "v1.15.3"
BOOT_OPERATOR_IMAGE = "ghcr.io/ironcore-dev/boot-operator:latest"
METAL_OPERATOR_PROBE_OS_IMAGE = REGISTRY + "/metal-probe:latest"

print("🚀 Starting maintenance-operator development environment")
print("Registry: " + REGISTRY)
print("Namespace: " + NAMESPACE)
print("Image: " + IMG)
print("Cert-Manager Version: " + CERT_MANAGER_VERSION)
print("Boot-Operator Image: " + BOOT_OPERATOR_IMAGE)
print("Metal-Operator Probe OS Image: " + METAL_OPERATOR_PROBE_OS_IMAGE)

# Deploy cert-manager function
def deploy_cert_manager():
    print("🔧 Installing cert-manager " + CERT_MANAGER_VERSION)

    # Check for and clean up any leftover webhook configurations that might interfere
    print("🧹 Checking for leftover webhook configurations...")
    local("kubectl delete mutatingwebhookconfiguration secrets-injector secrets-injector-generic --ignore-not-found=true", quiet=True, echo_off=True)
    local("kubectl delete validatingwebhookconfiguration secrets-injector --ignore-not-found=true", quiet=True, echo_off=True)

    # Apply cert-manager manifests
    local("kubectl apply -f https://github.com/cert-manager/cert-manager/releases/download/" + CERT_MANAGER_VERSION + "/cert-manager.yaml", quiet=True, echo_off=True)
    print("✅ cert-manager manifests applied")

    print("⏳ Waiting for cert-manager to start (this may take a few minutes)...")

    # Wait for deployments with increased timeout and continue on failure
    print("⏳ Waiting for cert-manager deployments...")
    local("kubectl wait --for=condition=Available --timeout=300s -n cert-manager deployment/cert-manager || echo 'cert-manager timeout'", quiet=True, echo_off=True)
    local("kubectl wait --for=condition=Available --timeout=300s -n cert-manager deployment/cert-manager-cainjector || echo 'cainjector timeout'", quiet=True, echo_off=True)
    local("kubectl wait --for=condition=Available --timeout=300s -n cert-manager deployment/cert-manager-webhook || echo 'webhook timeout'", quiet=True, echo_off=True)

    # Wait for webhook to be fully ready and serving certificates
    print("⏳ Waiting for cert-manager webhook to be ready...")
    local("kubectl wait --for=condition=Ready --timeout=180s -n cert-manager pod -l app=webhook", quiet=True, echo_off=True)

    # Give additional time for certificate propagation
    print("⏳ Allowing time for webhook certificates to propagate...")
    local("sleep 30", quiet=True, echo_off=True)

    print('✅ cert-manager installation completed!')

# Helper functions for patching deployments
def patch_image(namespace, name, image):
    patch = [{
        "op": "replace",
        "path": "/spec/template/spec/containers/0/image",
        "value": image,
    }]
    local("kubectl patch deployment {} -n {} --type json -p='{}'".format(name, namespace, str(encode_json(patch)).replace("\n", "")))

def replace_args_with_new_args(namespace, name, extra_args):
    patch = [{
        "op": "replace",
        "path": "/spec/template/spec/containers/0/args",
        "value": extra_args,
    }]
    local("kubectl patch deployment {} -n {} --type json -p='{}'".format(name, namespace, str(encode_json(patch)).replace("\n", "")))

# Deploy boot-operator function
def deploy_boot_operator():
    print("🥾 Deploying boot-operator")

    # Wait for cert-manager webhook to be fully functional before proceeding
    print("🔍 Verifying cert-manager webhook is ready...")
    local("""
# Wait for webhook to be responding correctly
for i in {1..30}; do
    if kubectl get validatingwebhookconfiguration cert-manager-webhook >/dev/null 2>&1; then
        echo '✅ cert-manager webhook is ready'
        break
    fi
    echo "⏳ Waiting for cert-manager webhook... ($i/30)"
    sleep 10
done
    """, quiet=True, echo_off=True)

    boot_uri = "https://github.com/ironcore-dev/boot-operator//config/dev"

    # Apply boot-operator manifests with retry logic
    cmd = "kubectl apply -k {}".format(boot_uri)
    print("📦 Applying boot-operator manifests...")

    # Try applying with retries in case of webhook issues
    local("""
for i in {1..3}; do
    if kubectl apply -k """ + boot_uri + """; then
        echo '✅ boot-operator manifests applied successfully'
        break
    else
        echo "⚠️ Retry $i/3: boot-operator deployment failed, retrying in 30 seconds..."
        sleep 30
    fi
done
    """, quiet=False)

    # Boot-operator arguments
    boot_args = [
        "--health-probe-bind-address=:8081",
        "--metrics-bind-address=127.0.0.1:8085",
        "--leader-elect",
        "--ipxe-service-url=http://boot-service:30007",
        "--ipxe-service-port=30007",
        "--controllers=ipxebootconfig,serverbootconfigpxe,serverbootconfighttp,httpbootconfig",
    ]

    # Patch boot-operator deployment with new arguments
    replace_args_with_new_args("boot-operator-system", "boot-operator-controller-manager", boot_args)

    # Patch boot-operator image
    patch_image("boot-operator-system", "boot-operator-controller-manager", BOOT_OPERATOR_IMAGE)

    print('✅ boot-operator deployment completed!')

# Deploy metal-operator function
def deploy_metal_operator():
    print("🔩 Deploying metal-operator")

    # Use kubectl kustomize to build metal-operator manifests from local directory
    yaml = local("cd ../metal-operator && kubectl kustomize ./config/default", quiet=True)

    # Convert to string and make necessary modifications
    yaml_str = str(yaml)
    yaml_str = yaml_str.replace("image: controller:latest", "image: localhost:5000/controller:latest")

    # Replace the existing args with our complete args list
    old_args = """      - args:
        - --metrics-bind-address=:8443
        - --leader-elect
        - --metrics-cert-path=/tmp/k8s-metrics-server/metrics-certs
        - --webhook-cert-path=/tmp/k8s-webhook-server/serving-certs"""

    new_args = """      - args:
        - --health-probe-bind-address=:8081
        - --metrics-bind-address=127.0.0.1:8080
        - --leader-elect
        - --probe-os-image=""" + METAL_OPERATOR_PROBE_OS_IMAGE + """
        - --registry-url=""" + REGISTRY + """
        - --metrics-cert-path=/tmp/k8s-metrics-server/metrics-certs
        - --webhook-cert-path=/tmp/k8s-webhook-server/serving-certs"""

    yaml_str = yaml_str.replace(old_args, new_args)

    # Convert back to blob and apply
    k8s_yaml(blob(yaml_str))

    print('✅ metal-operator deployment completed with probe OS image configured!')

# Deploy cert-manager
if config.tilt_subcommand != "down":
    deploy_cert_manager()

# Deploy metal-operator
if config.tilt_subcommand != "down":
    deploy_metal_operator()

# Deploy boot-operator
if config.tilt_subcommand != "down":
    deploy_boot_operator()

# Create local resources to track deployments
local_resource(
    'cert-manager-deploy',
    cmd='echo "cert-manager already deployed"',
    labels=['cert-manager'],
)

local_resource(
    'boot-operator-deploy',
    cmd='echo "boot-operator already deployed"',
    labels=['boot-operator'],
)

# Create a k8s_resource for the metal-operator deployment
k8s_resource(
    'metal-operator-controller-manager',
    new_name='metal-operator',
    labels=['metal-operator'],
)

# Build the maintenance-operator Docker image
docker_build(
    IMG,
    context='.',
    dockerfile='./Dockerfile',
    # Only rebuild when these files change
    only=[
        './cmd/',
        './internal/',
        './api/',
        './go.mod',
        './go.sum',
        './Dockerfile',
    ],
    # Live update for faster development
    live_update=[
        sync('./cmd', '/workspace/cmd'),
        sync('./internal', '/workspace/internal'),
        sync('./api', '/workspace/api'),
        run('cd /workspace && go build -o manager cmd/main.go', trigger=['./cmd/', './internal/', './api/']),
    ],
)

# Generate Kubernetes manifests using the Makefile
k8s_yaml(local('make tilt-yaml IMG=' + IMG, quiet=True))

# Configure the main resource
k8s_resource(
    'maintenance-operator-controller-manager',
    new_name='maintenance-operator',
    port_forwards=[
        '8080:8080',  # Metrics endpoint
        '8081:8081',  # Health endpoint
    ],
    labels=['operator'],
    resource_deps=['cert-manager-deploy', 'boot-operator-deploy', 'metal-operator'],
)

# Development helpers
local_resource(
    'logs',
    serve_cmd='kubectl logs -f deployment/maintenance-operator-controller-manager -n ' + NAMESPACE,
    labels=['debug'],
    resource_deps=['maintenance-operator'],
)

# Apply sample configurations if they exist
if os.path.exists('./config/samples'):
    local_resource(
        'apply-samples',
        cmd='kubectl apply -k config/samples/',
        deps=['./config/samples/'],
        labels=['samples'],
        resource_deps=['maintenance-operator'],
        auto_init=False,
    )

    local_resource(
        'cleanup-samples',
        cmd='kubectl delete -k config/samples/ --ignore-not-found=true',
        labels=['cleanup'],
        auto_init=False,
    )

# Complete cleanup
local_resource(
    'cleanup-all',
    cmd='''
echo "🧹 Cleaning up all deployed resources..."

echo "🗑️ Deleting operator namespaces..."
kubectl delete namespace boot-operator-system --ignore-not-found=true --timeout=120s
kubectl delete namespace metal-operator-system --ignore-not-found=true --timeout=120s
kubectl delete namespace maintenance-operator-system --ignore-not-found=true --timeout=60s

echo "🗑️ Deleting cert-manager resources..."
# Delete cert-manager namespace first to trigger cleanup
kubectl delete namespace cert-manager --ignore-not-found=true --timeout=120s

# Wait a moment for namespace deletion to start
sleep 5

# Clean up cluster-scoped cert-manager resources
echo "🗑️ Cleaning up cert-manager cluster resources..."
kubectl delete mutatingwebhookconfiguration cert-manager-webhook --ignore-not-found=true
kubectl delete validatingwebhookconfiguration cert-manager-webhook --ignore-not-found=true

# Delete all cert-manager cluster roles
kubectl delete clusterrole cert-manager-cainjector cert-manager-controller-issuers cert-manager-controller-clusterissuers cert-manager-controller-certificates cert-manager-controller-orders cert-manager-controller-challenges cert-manager-controller-ingress-shim cert-manager-view cert-manager-edit cert-manager-controller-approve:cert-manager-io cert-manager-controller-certificatesigningrequests cert-manager-webhook:subjectaccessreviews --ignore-not-found=true

# Delete all cert-manager cluster role bindings
kubectl delete clusterrolebinding cert-manager-cainjector cert-manager-controller-issuers cert-manager-controller-clusterissuers cert-manager-controller-certificates cert-manager-controller-orders cert-manager-controller-challenges cert-manager-controller-ingress-shim cert-manager-controller-approve:cert-manager-io cert-manager-controller-certificatesigningrequests cert-manager-webhook:subjectaccessreviews --ignore-not-found=true

# Delete cert-manager CRDs
kubectl delete crd certificates.cert-manager.io certificaterequests.cert-manager.io issuers.cert-manager.io clusterissuers.cert-manager.io orders.acme.cert-manager.io challenges.acme.cert-manager.io --ignore-not-found=true

# Clean up any additional cert-manager resources that might exist
echo "🔍 Scanning for remaining cert-manager resources..."
for role in $(kubectl get clusterrole | grep cert-manager | awk '{print $1}'); do
  echo "Deleting clusterrole: $role"
  kubectl delete clusterrole "$role" --ignore-not-found=true
done

for binding in $(kubectl get clusterrolebinding | grep cert-manager | awk '{print $1}'); do
  echo "Deleting clusterrolebinding: $binding"
  kubectl delete clusterrolebinding "$binding" --ignore-not-found=true
done

for crd in $(kubectl get crd | grep cert-manager | awk '{print $1}'); do
  echo "Deleting crd: $crd"
  kubectl delete crd "$crd" --ignore-not-found=true
done

echo "🗑️ Cleaning up metal-operator resources..."
# Clean up metal-operator specific resources
kubectl delete validatingwebhookconfiguration metal-operator-validating-webhook-configuration --ignore-not-found=true
for role in $(kubectl get clusterrole | grep metal-operator | awk '{print $1}'); do
  kubectl delete clusterrole "$role" --ignore-not-found=true
done
for binding in $(kubectl get clusterrolebinding | grep metal-operator | awk '{print $1}'); do
  kubectl delete clusterrolebinding "$binding" --ignore-not-found=true
done
for crd in $(kubectl get crd | grep metal.ironcore.dev | awk '{print $1}'); do
  kubectl delete crd "$crd" --ignore-not-found=true
done

echo "🗑️ Cleaning up boot-operator resources..."
# Clean up boot-operator specific resources
for role in $(kubectl get clusterrole | grep boot-operator | awk '{print $1}'); do
  kubectl delete clusterrole "$role" --ignore-not-found=true
done
for binding in $(kubectl get clusterrolebinding | grep boot-operator | awk '{print $1}'); do
  kubectl delete clusterrolebinding "$binding" --ignore-not-found=true
done

echo "🗑️ Cleaning up maintenance-operator cluster-scoped resources..."
kubectl delete clusterrole maintenance-operator-manager-role maintenance-operator-metrics-auth-role maintenance-operator-metrics-reader --ignore-not-found=true
kubectl delete clusterrolebinding maintenance-operator-manager-rolebinding maintenance-operator-metrics-auth-rolebinding --ignore-not-found=true
kubectl delete crd ansiblejobs.ansible.maintenance.metal.ironcore.dev --ignore-not-found=true

echo '✅ Cleanup completed! All resources have been removed.'
echo "💡 You may need to wait a few moments for all resources to fully terminate."
    ''',
    labels=['cleanup'],
    auto_init=False,
)

# Set up cleanup to run on tilt down
def cleanup_on_down():
    local('''
echo "🧹 Running cleanup on tilt down..."
kubectl delete namespace boot-operator-system cert-manager metal-operator-system maintenance-operator-system --ignore-not-found=true --timeout=60s
echo "🗑️ Cleaning up cluster-scoped resources..."
kubectl delete clusterrole,clusterrolebinding -l "app.kubernetes.io/name in (cert-manager,boot-operator,metal-operator,maintenance-operator)" --ignore-not-found=true
kubectl delete validatingwebhookconfiguration,mutatingwebhookconfiguration cert-manager-webhook metal-operator-validating-webhook-configuration --ignore-not-found=true
echo '✅ Quick cleanup completed'
    ''', echo_off=True)

# Register cleanup to run on tilt down
if config.tilt_subcommand == "down":
    cleanup_on_down()

# Only show the ready message when starting up
if config.tilt_subcommand != "down":
    print("""
✅ Maintenance Operator Development Environment Ready!

Available resources:
- maintenance-operator: Main controller deployment
- cert-manager: TLS certificate management (automatically deployed)
- boot-operator: Server boot configuration management (automatically deployed)
- logs: Stream controller logs
- apply-samples: Apply sample AnsibleJob resources
- cleanup-samples: Remove sample resources
- cleanup-all: Complete cleanup (includes cert-manager + boot-operator)

Port forwards:
- localhost:8080 -> maintenance-operator metrics endpoint
- localhost:8081 -> maintenance-operator health endpoint

Quick commands:
- tilt up: Start development environment
- tilt down: Stop Tilt
- tilt trigger apply-samples: Deploy sample AnsibleJobs
- tilt trigger cleanup-all: Complete cleanup (cert-manager + boot-operator + maintenance-operator)

Configuration:
- Registry: """ + REGISTRY + """
- Namespace: """ + NAMESPACE + """
- Image: """ + IMG + """
- Cert-Manager Version: """ + CERT_MANAGER_VERSION + """
- Boot-Operator Image: """ + BOOT_OPERATOR_IMAGE + """
""")
