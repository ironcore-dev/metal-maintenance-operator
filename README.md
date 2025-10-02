# maintenance-operator

[![REUSE status](https://api.reuse.software/badge/github.com/ironcore-dev/maintenance-operator)](https://api.reuse.software/info/github.com/ironcore-dev/maintenance-operator)
[![Go Report Card](https://goreportcard.com/badge/github.com/ironcore-dev/maintenance-operator)](https://goreportcard.com/report/github.com/ironcore-dev/maintenance-operator)
[![GitHub License](https://img.shields.io/static/v1?label=License&message=Apache-2.0&color=blue)](LICENSE)
[![PRs Welcome](https://img.shields.io/badge/PRs-welcome-brightgreen.svg)](https://makeapullrequest.com)

A Kubernetes operator for running Ansible jobs using ansible-runner for infrastructure maintenance tasks.

## Description

The Maintenance Operator provides a Kubernetes-native way to execute Ansible playbooks for infrastructure maintenance tasks using ansible-runner.

## Features

- 🚀 **Ansible Execution**: Execute playbooks using ansible-runner
- 🔧 **Git Repository Integration**: Automatic cloning of playbook and role repositories
- 📊 **Job Status Tracking**: Comprehensive status reporting and logging
- 🛡️ **RBAC Ready**: Complete role-based access control configuration
- 🔐 **Secure Inventory Management**: Support for inline, ConfigMap, and Secret-based inventories

## Getting Started

### Prerequisites
- go version v1.24.0+
- docker version 17.03+.
- kubectl version v1.11.3+.
- Access to a Kubernetes v1.11.3+ cluster.

### Installation

1. Install the CRDs:
```bash
make install
```

2. Deploy the controller:
```bash
make deploy IMG=ghcr.io/ironcore-dev/maintenance-operator:latest
```
§
### Development Setup

For fast local development with hot reloading, we recommend using [Tilt](https://tilt.dev/):

1. **Install Tilt**: Follow the [installation guide](https://docs.tilt.dev/install.html)

2. **Quick start with our helper script**:
```bash
./scripts/dev-setup.sh start
```

3. **Or manually with Tilt**:
```bash
tilt up
```

4. **Using an existing registry** (if you already have one running):
```bash
# With environment variables
REGISTRY=localhost:5001 SKIP_REGISTRY=true ./scripts/dev-setup.sh start

# Or with Tilt directly
tilt up -- --registry=localhost:5001 --skip-registry-setup=true
```

This will:
- Set up a local Docker registry (unless you skip it)
- Build and deploy the operator with live reload
- Provide a web UI at http://localhost:10350
- Forward metrics ports (8080) and health endpoints (8081)

For detailed development instructions, see [docs/development.md](docs/development.md).

## Usage

Create an AnsibleJob that executes playbooks using ansible-runner:

```yaml
apiVersion: maintenance.metal.ironcore.dev/v1alpha1
kind: AnsibleJob
metadata:
  name: maintenance-job
  namespace: default
spec:
  playbook: site.yml
  playbookRepo: "https://github.com/example/ansible-playbooks.git"
  playbookGitRef: "main"
  rolesRepo: "https://github.com/example/ansible-roles.git"
  rolesGitRef: "v1.0.0"
  inventory:
    inline: |
      [webservers]
      web1.example.com
      web2.example.com

      [databases]
      db1.example.com

      [all:vars]
      ansible_user=admin
  extraVars:
    environment: production
    backup_enabled: "true"
    maintenance_window: "02:00-04:00"
  limit: "webservers"
  timeoutSeconds: 3600
  jobTemplate:
    image: "quay.io/ansible/ansible-runner:latest"
    serviceAccountName: "ansible-runner"
    backoffLimit: 3
    resources:
      limits:
        cpu: "1"
        memory: "2Gi"
      requests:
        cpu: "500m"
        memory: "1Gi"
```

### Inventory Options

You can specify inventory in multiple ways:

#### Inline Inventory
```yaml
spec:
  inventory:
    inline: |
      [servers]
      server1.example.com
      server2.example.com
```

#### ConfigMap Reference
```yaml
spec:
  inventory:
    configMapRef:
      name: my-inventory
      key: inventory.ini
```

#### Secret Reference
```yaml
spec:
  inventory:
    secretRef:
      name: my-inventory-secret
      key: inventory.yml
```

## Monitoring Jobs

Check the status of your AnsibleJob:

```bash
kubectl get ansiblejob -o wide
kubectl describe ansiblejob <job-name>
```

View job logs:

```bash
kubectl logs -l ansiblejob=<job-name>
```

### To Deploy on the cluster
**Build and push your image to the location specified by `IMG`:**

```sh
make docker-build docker-push IMG=<some-registry>/maintenance-operator:tag
```

**NOTE:** This image ought to be published in the personal registry you specified.
And it is required to have access to pull the image from the working environment.
Make sure you have the proper permission to the registry if the above commands don’t work.

**Install the CRDs into the cluster:**

```sh
make install
```

**Deploy the Manager to the cluster with the image specified by `IMG`:**

```sh
make deploy IMG=<some-registry>/maintenance-operator:tag
```

> **NOTE**: If you encounter RBAC errors, you may need to grant yourself cluster-admin
privileges or be logged in as admin.

**Create instances of your solution**
You can apply the samples (examples) from the config/sample:

```sh
kubectl apply -k config/samples/
```

>**NOTE**: Ensure that the samples has default values to test it out.

### To Uninstall
**Delete the instances (CRs) from the cluster:**

```sh
kubectl delete -k config/samples/
```

**Delete the APIs(CRDs) from the cluster:**

```sh
make uninstall
```

**UnDeploy the controller from the cluster:**

```sh
make undeploy
```

## Project Distribution

Following the options to release and provide this solution to the users.

### By providing a bundle with all YAML files

1. Build the installer for the image built and published in the registry:

```sh
make build-installer IMG=<some-registry>/maintenance-operator:tag
```

**NOTE:** The makefile target mentioned above generates an 'install.yaml'
file in the dist directory. This file contains all the resources built
with Kustomize, which are necessary to install this project without its
dependencies.

2. Using the installer

Users can just run 'kubectl apply -f <URL for YAML BUNDLE>' to install
the project, i.e.:

```sh
kubectl apply -f https://raw.githubusercontent.com/<org>/maintenance-operator/<tag or branch>/dist/install.yaml
```

### By providing a Helm Chart

1. Build the chart using the optional helm plugin

```sh
kubebuilder edit --plugins=helm/v1-alpha
```

2. See that a chart was generated under 'dist/chart', and users
can obtain this solution from there.

**NOTE:** If you change the project, you need to update the Helm Chart
using the same command above to sync the latest changes. Furthermore,
if you create webhooks, you need to use the above command with
the '--force' flag and manually ensure that any custom configuration
previously added to 'dist/chart/values.yaml' or 'dist/chart/manager/manager.yaml'
is manually re-applied afterwards.

## Contributing
// TODO(user): Add detailed information on how you would like others to contribute to this project

**NOTE:** Run `make help` for more information on all potential `make` targets

More information can be found via the [Kubebuilder Documentation](https://book.kubebuilder.io/introduction.html)

## Licensing

Copyright 2025 SAP SE or an SAP affiliate company and IronCore contributors. Please see our [LICENSE](LICENSE) for
copyright and license information. Detailed information including third-party components and their licensing/copyright
information is available [via the REUSE tool](https://api.reuse.software/info/github.com/ironcore-dev/maintenance-operator).
