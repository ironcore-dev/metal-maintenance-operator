# AnsibleJob Examples

This directory contains example AnsibleJob manifests for testing and demonstration purposes using ansible-runner execution.

## Files

### hello-world.yaml
A basic example using the ansible-tower-samples repository for playbook execution.

## Using Examples

1. **Apply the example:**
   ```bash
   kubectl apply -f examples/hello-world.yaml
   ```

2. **Monitor execution:**
   ```bash
   # Check AnsibleJob status
   kubectl get ansiblejob

   # Check underlying Kubernetes Job
   kubectl get jobs

   # View logs
   kubectl logs -f <pod-name>
   ```

3. **Clean up:**
   ```bash
   kubectl delete -f examples/hello-world.yaml
   # or
   kubectl delete ansiblejob hello-world
   ```

## Creating Your Own

Use the example as a template for creating your own AnsibleJobs. Key components:

- **playbook**: Name of the playbook file to execute
- **playbookRepo**: Git repository URL containing playbooks
- **playbookGitRef**: Git branch/tag/commit to use (optional, defaults to main/master)
- **rolesRepo**: Git repository URL for additional roles (optional)
- **rolesGitRef**: Git branch/tag/commit for roles (optional)
- **inventory**: Target hosts (can be inline YAML or reference to secret/configmap)
- **extraVars**: Variables to pass to the playbook
- **limit**: Restrict execution to specific hosts (optional)
- **timeoutSeconds**: Maximum execution time (optional)
- **jobTemplate**: Configuration for the Kubernetes Job (image, resources, etc.)

For more examples, see the `config/samples/` directory.
