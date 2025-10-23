# AnsibleJob Documentation

The Maintenance Operator provides a Kubernetes-native way to execute Ansible playbooks for infrastructure maintenance tasks using ansible-runner.

## Features

- 🚀 **Ansible Execution**: Execute playbooks using ansible-runner
- 🔧 **Git Repository Integration**: Automatic cloning of playbook and role repositories
- 📊 **Job Status Tracking**: Comprehensive status reporting and logging
- 🛡️ **RBAC Ready**: Complete role-based access control configuration
- 🔐 **Secure Inventory Management**: Support for inline, ConfigMap, and Secret-based inventories

## Usage

Create an AnsibleJob that executes playbooks using ansible-runner:

```yaml
apiVersion: ansible.maintenance.metal.ironcore.dev/v1alpha1
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

## Inventory Options

You can specify inventory in multiple ways:

### Inline Inventory
```yaml
spec:
  inventory:
    inline: |
      [servers]
      server1.example.com
      server2.example.com
```

### ConfigMap Reference
```yaml
spec:
  inventory:
    configMapRef:
      name: my-inventory
      key: inventory.ini
```

### Secret Reference
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

## Related Documentation

- [Development Guide](development.md) - Setting up a development environment
- [Development Examples](development-example.md) - Example development workflows
- [Environment Configuration](env-config-examples.md) - Configuration examples
- [Ansible Runner Controller](ansible-runner-controller.md) - Controller architecture