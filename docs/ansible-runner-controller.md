# AnsibleJob Controller

This controller enables running Ansible playbooks via ansible-runner as Kubernetes Jobs for infrastructure maintenance tasks.

## Features

- **Ansible execution**: Run playbooks using ansible-runner
- **Git repository support**: Automatically clone playbooks and roles from Git repositories
- **Flexible inventory**: Support inline inventory, ConfigMaps, or Secrets
- **Resource management**: Configure CPU/memory limits and requests
- **Job monitoring**: Track execution status and results
- **Multiple Git sources**: Support separate repositories for playbooks and roles

## Usage

Create an AnsibleJob to execute Ansible playbooks using ansible-runner:
apiVersion: ansible.maintenance.metal.ironcore.dev/v1alpha1
kind: AnsibleJob
metadata:
  name: maintenance-task
  namespace: default
spec:
  playbook: "maintenance/server-update.yml"
  playbookRepo: "https://github.com/your-org/ansible-playbooks.git"
  playbookGitRef: "v1.2.0"
  rolesRepo: "https://github.com/your-org/ansible-roles.git"
  rolesGitRef: "v1.5.0"
  inventory:
    inline: |
      [servers]
      web1 ansible_host=10.0.1.10
      web2 ansible_host=10.0.1.11

      [servers:vars]
      ansible_python_interpreter=/usr/bin/python3
  extraVars:
    environment: "production"
    maintenance_window: "2024-01-15"
  limit: "servers"
  timeoutSeconds: 3600
  jobTemplate:
    image: "quay.io/ansible/ansible-runner:stable-2.12-latest"
    serviceAccountName: "ansible-runner"
    backoffLimit: 1
    resources:
      limits:
        cpu: "1000m"
        memory: "1Gi"
      requests:
        cpu: "500m"
        memory: "512Mi"
```

## Git Repository Structure

The controller clones the specified `playbookRepo` and organizes it for ansible-runner execution:

```
/runner/
├── project/          # Git repository content
│   ├── playbooks/    # Your playbooks
│   ├── roles/        # Your roles (if any)
│   └── ...          # Other repository content
├── inventory/        # Generated from spec.inventory
└── artifacts/        # Execution artifacts and logs
```

**Repository Requirements:**
- Playbooks can be in the root directory or in a `playbooks/` subdirectory
- Roles can be in a `roles/` directory
- Standard Ansible directory structure is recommended

## Working Examples

See the `examples/` directory for tested, working examples:
- `examples/hello-world.yaml` - Basic ansible-runner example
- `examples/simple-ansible-job.yaml` - Simple playbook execution
- `examples/using-job-templates.yaml` - Using template library features

## Status Tracking

Monitor job execution:

```bash
# List all AnsibleJobs
kubectl get ansiblejobs

# Get detailed information
kubectl describe ansiblejob maintenance-task

# Watch job status in real-time
kubectl get ansiblejob maintenance-task -w

# View job logs
kubectl logs job/maintenance-task-ansible-job
```

## Prerequisites

1. **Container Image**: Use `quay.io/ansible/ansible-runner:stable-2.12-latest` or build custom image with required collections
2. **RBAC**: Service account with permissions to create Jobs and ConfigMaps
3. **Git Access**: For private repositories, configure SSH keys or tokens
4. **Network Access**: Ensure pods can reach target hosts defined in inventory

### Development Setup:
```bash
# Start development environment
./hack/dev-setup.sh start

# Apply sample configurations
kubectl apply -k config/samples/
```

## Security Considerations

- Use dedicated service accounts with minimal required permissions
- Store credentials in Kubernetes Secrets, never in plain text
- Consider network policies to restrict access to target hosts
- Regularly rotate SSH keys and Git access tokens
- Use read-only Git access when possible
- Validate and sanitize extra variables and inventory content
