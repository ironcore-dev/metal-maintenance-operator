<!--
SPDX-FileCopyrightText: 2025 SAP SE or an SAP affiliate company and IronCore contributors
SPDX-License-Identifier: Apache-2.0
-->

# Maintenance Operator Style Guide

This document defines the coding standards and style guidelines for the Maintenance Operator project. It incorporates best practices from the Kubernetes community and ensures consistency across our codebase.

## Table of Contents

- [General Principles](#general-principles)
- [Go Code Style](#go-code-style)
- [File Organization](#file-organization)
- [Licensing and Copyright](#licensing-and-copyright)
- [YAML and Configuration](#yaml-and-configuration)
- [Documentation](#documentation)
- [Testing](#testing)
- [Git Conventions](#git-conventions)
- [Tools and Automation](#tools-and-automation)

## General Principles

1. **Follow Kubernetes Community Guidelines**: We adhere to the [Kubernetes development guide](https://github.com/kubernetes/community/tree/master/contributors/devel)
2. **Consistency Over Preference**: Maintain consistency with existing code patterns
3. **Readability First**: Code should be self-documenting and easy to understand
4. **Automation**: Use tools to enforce style automatically

## Go Code Style

### Basic Formatting

- **Line Length**: Maximum 120 characters per line
- **Indentation**: Use tabs, not spaces
- **Imports**: Group imports using `gci` with the following order:
  1. Standard library packages
  2. Third-party packages
  3. Local packages (prefixed with `github.com/ironcore-dev/maintenance-operator`)

```go
import (
    "context"
    "fmt"

    "k8s.io/client-go/kubernetes"
    "sigs.k8s.io/controller-runtime/pkg/client"

    "github.com/ironcore-dev/maintenance-operator/api/v1alpha1"
    "github.com/ironcore-dev/maintenance-operator/internal/controller"
)
```

### Naming Conventions

- **Packages**: Short, lowercase, single word when possible
- **Variables**: Use camelCase; prefer descriptive names over brevity
- **Constants**: Use SCREAMING_SNAKE_CASE for package-level constants
- **Functions/Methods**: Use camelCase; exported functions start with capital letter
- **Structs**: Use PascalCase
- **Interfaces**: Use PascalCase, often ending with `-er` (e.g., `Reconciler`)

### Error Handling

```go
// Good: Wrap errors with context
if err := doSomething(); err != nil {
    return fmt.Errorf("failed to do something: %w", err)
}

// Bad: Don't ignore errors
doSomething() // missing error check

// Good: Use early returns to reduce nesting
func processRequest(req *Request) error {
    if req == nil {
        return errors.New("request cannot be nil")
    }

    if err := validateRequest(req); err != nil {
        return fmt.Errorf("invalid request: %w", err)
    }

    // Continue with main logic...
    return nil
}
```

### Controller Patterns

Follow Kubernetes controller conventions:

```go
// Use structured logging with consistent keys
func (r *AnsibleJobReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
    log := log.FromContext(ctx).WithValues("ansiblejob", req.NamespacedName)

    var ansibleJob v1alpha1.AnsibleJob
    if err := r.Get(ctx, req.NamespacedName, &ansibleJob); err != nil {
        if errors.IsNotFound(err) {
            log.Info("AnsibleJob not found, likely deleted")
            return ctrl.Result{}, nil
        }
        return ctrl.Result{}, fmt.Errorf("failed to get AnsibleJob: %w", err)
    }

    // Main reconciliation logic...
    return ctrl.Result{}, nil
}
```

### Resource Management

```go
// Always check for nil pointers when accessing optional fields
if ansibleJob.Spec.JobTemplate != nil && ansibleJob.Spec.JobTemplate.Resources != nil {
    // Safe to access Resources fields
}

// Use helper functions for conversion between formats
extraVarsMap := v1alpha1.KeyValuePairsToMap(ansibleJob.Spec.ExtraVars)
```

## File Organization

### Directory Structure

```
├── api/                    # API definitions
│   └── v1alpha1/          # API version
├── cmd/                   # Main applications
├── config/               # Kubernetes configurations
├── docs/                 # Documentation
├── hack/                 # Scripts and tools
├── internal/            # Internal packages
│   └── controller/      # Controller implementations
├── test/                # Tests
└── templates/           # File templates
```

### File Naming

- Go files: `snake_case.go`
- Test files: `*_test.go`
- YAML files: `kebab-case.yaml`
- Documentation: `UPPERCASE.md` for important docs, otherwise `lowercase.md`

## Licensing and Copyright

Every source file must include the SPDX license header:

### Go Files
```go
// SPDX-FileCopyrightText: 2025 SAP SE or an SAP affiliate company and IronCore contributors
// SPDX-License-Identifier: Apache-2.0

package main
```

### YAML Files
```yaml
# SPDX-FileCopyrightText: 2025 SAP SE or an SAP affiliate company and IronCore contributors
# SPDX-License-Identifier: Apache-2.0

apiVersion: v1
kind: ConfigMap
```

### Markdown Files
```markdown
<!--
SPDX-FileCopyrightText: 2025 SAP SE or an SAP affiliate company and IronCore contributors
SPDX-License-Identifier: Apache-2.0
-->

# Document Title
```

### Dockerfile
```dockerfile
# SPDX-FileCopyrightText: 2025 SAP SE or an SAP affiliate company and IronCore contributors
# SPDX-License-Identifier: Apache-2.0

FROM golang:1.21
```

## YAML and Configuration

### Kubernetes Manifests

- Use 2 spaces for indentation
- Always specify `apiVersion` and `kind`
- Use descriptive names and labels
- Include resource limits and requests

```yaml
apiVersion: batch/v1
kind: Job
metadata:
  name: ansible-job-example
  labels:
    app.kubernetes.io/name: maintenance-operator
    app.kubernetes.io/component: ansible-job
spec:
  template:
    spec:
      containers:
      - name: ansible-runner
        image: quay.io/ansible/ansible-runner:latest
        resources:
          requests:
            cpu: 100m
            memory: 128Mi
          limits:
            cpu: 500m
            memory: 512Mi
```

### Configuration Files

- Use clear, descriptive keys
- Group related settings
- Document complex configurations with comments

## Documentation

### Go Documentation

```go
// AnsibleJobReconciler reconciles an AnsibleJob object
type AnsibleJobReconciler struct {
    client.Client
    Scheme *runtime.Scheme
}

// Reconcile handles the reconciliation of AnsibleJob resources.
// It ensures that the desired state matches the actual state in the cluster.
func (r *AnsibleJobReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
    // Implementation...
}
```

### API Documentation

Use kubebuilder markers for API documentation:

```go
// AnsibleJobSpec defines the desired state of AnsibleJob
type AnsibleJobSpec struct {
    // Playbook specifies the Ansible playbook to run
    // +kubebuilder:validation:Required
    Playbook string `json:"playbook"`

    // ExtraVars are additional variables to pass to the playbook
    // +optional
    ExtraVars []KeyValuePair `json:"extraVars,omitempty"`
}
```

## Testing

### Test Organization

```
test/
├── e2e/              # End-to-end tests
├── integration/      # Integration tests
└── unit/            # Unit tests (alongside source files)
```

### Test Naming

```go
func TestAnsibleJobReconciler_Reconcile(t *testing.T) {
    tests := []struct {
        name    string
        setup   func(*testing.T) *AnsibleJob
        want    ctrl.Result
        wantErr bool
    }{
        {
            name: "creates job when AnsibleJob is valid",
            setup: func(t *testing.T) *AnsibleJob {
                return &AnsibleJob{/* ... */}
            },
            want: ctrl.Result{},
            wantErr: false,
        },
    }

    for _, tt := range tests {
        t.Run(tt.name, func(t *testing.T) {
            // Test implementation...
        })
    }
}
```

### Test Patterns

- Use table-driven tests for multiple scenarios
- Use meaningful test names that describe the scenario
- Include both positive and negative test cases
- Use testify/assert for assertions when appropriate

## Git Conventions

### Commit Messages

Follow the [Conventional Commits](https://www.conventionalcommits.org/) format:

```
<type>[optional scope]: <description>

[optional body]

[optional footer(s)]
```

Examples:
- `feat(controller): add support for role repository integration`
- `fix(api): validate required fields in AnsibleJob spec`
- `docs: update installation guide`
- `test: add unit tests for job builder`

### Branch Naming

- Feature branches: `feature/description-of-feature`
- Bug fixes: `fix/issue-number-description`
- Documentation: `docs/description`

## Tools and Automation

### Required Tools

1. **golangci-lint**: Code linting and style checking
2. **gofmt**: Code formatting
3. **goimports**: Import organization
4. **controller-gen**: Kubernetes code generation

### Make Targets

```bash
# Code quality and formatting
make lint          # Run all linters
make fmt           # Format Go code
make vet           # Run go vet
make test          # Run all tests

# License compliance
make license-check # Check license headers
make license-fix   # Add missing license headers

# Style enforcement
make style-check   # Check code style compliance
make style-fix     # Fix code style issues
```

### VS Code Configuration

The project includes VS Code settings for:
- Automatic formatting on save
- Import organization
- License header insertion
- Go tooling integration

### Pre-commit Hooks

Set up pre-commit hooks to enforce style automatically:

```bash
# Install pre-commit
pip install pre-commit

# Install hooks
pre-commit install

# Run manually
pre-commit run --all-files
```

## Enforcement

This style guide is enforced through:

1. **Automated Linting**: golangci-lint configuration in `.golangci.yml`
2. **CI/CD Pipeline**: Style checks in GitHub Actions
3. **Pre-commit Hooks**: Local validation before commits
4. **Code Reviews**: Manual review for adherence to guidelines
5. **Documentation**: Regular updates to this guide

## Contributing

When contributing to the project:

1. Read this style guide thoroughly
2. Set up the recommended development environment
3. Run `make style-check` before submitting PRs
4. Ensure all automated checks pass
5. Follow the code review guidelines

For questions about style decisions not covered here, refer to:
- [Effective Go](https://golang.org/doc/effective_go.html)
- [Go Code Review Comments](https://github.com/golang/go/wiki/CodeReviewComments)
- [Kubernetes Coding Conventions](https://github.com/kubernetes/community/blob/master/contributors/guide/coding-conventions.md)
