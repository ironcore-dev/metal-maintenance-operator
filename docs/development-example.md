# Example: Development Workflow

This example demonstrates the complete development workflow using Tilt.

## Step 1: Start Development Environment

```bash
# Option 1: Use the convenience script
./scripts/dev-setup.sh start

# Option 2: Use make target
make dev

# Option 3: Use Tilt directly
tilt up
```

## Step 2: Access the Tilt UI

Open your browser to http://localhost:10350 to see:

- Real-time build status
- Log streams from all components
- Resource health status
- Port forwarding status

## Step 3: Make a Code Change

Try making a small change to see hot reloading in action:

1. Open `internal/controller/ansiblejob_controller.go`
2. Find the `Reconcile` function and add a log statement:
   ```go
   logger.Info("Development hot reload test", "job", req.NamespacedName)
   ```
3. Save the file
4. Watch the Tilt UI rebuild and redeploy automatically

## Step 4: Test with Sample Resources

Deploy a sample AnsibleJob:

```bash
# Create a test AnsibleJob
kubectl apply -f - <<EOF
apiVersion: maintenance.metal.ironcore.dev/v1alpha1
kind: AnsibleJob
metadata:
  name: dev-test-job
  namespace: default
spec:
  playbook: hello-world.yml
  playbookRepo: "https://github.com/ansible/ansible-tower-samples.git"
  playbookGitRef: "master"
  inventory:
    inline: |
      [localhost]
      127.0.0.1 ansible_connection=local
  extraVars:
    message: "Hello from development!"
  jobTemplate:
    image: "quay.io/ansible/ansible-runner:latest"
    serviceAccountName: "ansible-runner"
    backoffLimit: 1
    resources:
      limits:
        cpu: "500m"
        memory: "512Mi"
      requests:
        cpu: "250m"
        memory: "256Mi"
EOF
```

## Step 5: Monitor the Job

```bash
# Watch the job status
kubectl get ansiblejob dev-test-job -w

# Check job details
kubectl describe ansiblejob dev-test-job

# View controller logs (in Tilt UI or via kubectl)
kubectl logs -f deployment/maintenance-operator-controller-manager -n maintenance-operator-system
```

## Step 6: Check Metrics

With port forwarding enabled, you can access:

- **Metrics**: http://localhost:8080/metrics
- **Health**: http://localhost:8081/healthz
- **Readiness**: http://localhost:8081/readyz

## Step 7: Cleanup

```bash
# Remove test resources
kubectl delete ansiblejob dev-test-job

# Stop development environment
./scripts/dev-setup.sh stop
# or
make dev-stop
# or
tilt down
```

## Tips for Development

### Fast Iteration

1. **Code changes** trigger automatic rebuilds
2. **Live updates** sync files directly to running containers
3. **Binary recompilation** happens inside the container

### Debugging

1. **Add logging**: Use the standard controller-runtime logger
2. **Port forward**: Connect debuggers to port 40000 (if configured)
3. **Resource inspection**: Use `kubectl` or the Tilt UI

### Testing Different Configurations

1. **Ansible execution**: Test different playbook repositories and configurations
2. **Resource limits**: Modify job templates and see immediate effects
3. **Error scenarios**: Test validation, missing secrets, etc.
4. **Inventory types**: Test inline, ConfigMap, and Secret-based inventories

### Performance Monitoring

Monitor the development environment:

```bash
# Watch resource usage
kubectl top pods -n maintenance-operator-system

# Check metrics
curl http://localhost:8080/metrics | grep ansible_job
```

This workflow provides a complete development experience with fast feedback loops!
