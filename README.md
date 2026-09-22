# metal-maintenance-operator

[![REUSE status](https://api.reuse.software/badge/github.com/ironcore-dev/metal-maintenance-operator)](https://api.reuse.software/info/github.com/ironcore-dev/metal-maintenance-operator)
[![GitHub License](https://img.shields.io/static/v1?label=License&message=Apache-2.0&color=blue)](LICENSE)
[![PRs Welcome](https://img.shields.io/badge/PRs-welcome-brightgreen.svg)](https://makeapullrequest.com)

// TODO(user): Add simple overview of use/purpose

## Description
// TODO(user): An in-depth paragraph about your project and overview of use

## Getting Started

### Prerequisites
- go version v1.24.0+
- docker version 17.03+.
- kubectl version v1.11.3+.
- Access to a Kubernetes v1.11.3+ cluster.

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

## kube-state-metrics integration

When `kubeStateMetrics.enabled: true`, the chart ships a `CustomResourceStateMetrics` ConfigMap that makes kube-state-metrics expose the operator's Custom Resources as `kube_ironcore_info` metrics (which the dashboards below join against).

**Note:** upstream kube-state-metrics has no label-based ConfigMap discovery, so this ConfigMap is inert until a sidecar/initContainer (e.g. the open-source [`kiwigrid/k8s-sidecar`](https://github.com/kiwigrid/k8s-sidecar)) feeds it into KSM. See [`docs/concepts/kube-state-metrics.md`](docs/concepts/kube-state-metrics.md) for the wiring, consumers, and limitations.

## Developing Dashboards

Dashboards are distributed alongside the operator as [Perses](https://perses.dev) dashboards defined as Go code.
All dashboard source lives under [`dashboards/`](dashboards/) and the generated JSON is committed to [`dashboards/built/`](dashboards/built/) so that PR diffs show exactly what changes.

### Prerequisites

Download the Perses CLI (`percli`) and server binary (requires `curl`):

```sh
make percli perses-server
```

This also extracts the plugin archive that the Perses server needs at runtime. The first download is ~140 MB.

**Configure a local datasource**

The dashboards query a Prometheus datasource. Copy the example and fill in your Prometheus URL:

```sh
cp dashboards/dev-provisioning/datasource.yaml.example dashboards/dev-provisioning/datasource.yaml
# Edit datasource.yaml and replace https://your-prometheus-instance/ with your Prometheus URL
```

`datasource.yaml` is gitignored — each developer configures it locally with their own Prometheus instance. The datasource is named `prometheus` and scoped to the `metal-maintenance-operator` project.

### Local development workflow

**1. Start a local Perses instance**

```sh
make perses-start
```

This starts a Perses server on `http://localhost:8088` backed by a local file database. It pre-provisions a `metal-maintenance-operator` project and loads any previously built dashboards from `dashboards/built/`. Plugin loading takes ~15 seconds; the server is ready once you see `⇨ http server started`.

**2. Watch dashboards for changes**

In a second terminal:

```sh
make dashboard-watch
```

This runs `percli dac watch` inside `dashboards/`, rebuilding any changed `main.go` files to JSON in `dashboards/built/` whenever you save. Run `make dashboard-apply` once to push the current build to Perses, then iterate: edit Go source, save, the watcher rebuilds, re-run `make dashboard-apply` to see changes in the UI.

**3. Apply dashboards manually**

```sh
make dashboard-apply
```

Builds and pushes all dashboards to the running Perses instance.

**4. Lint dashboards**

```sh
make dashboard-lint
```

Validates all built JSON files against Perses schemas. This runs in CI on every PR.

**5. Target a different Perses instance**

Override `PERSES_URL` to apply dashboards to the shared instance (requires `PERSES_TOKEN` if authentication is enabled):

```sh
make dashboard-apply PERSES_URL=https://perses.example.com PERSES_TOKEN=<token>
```

### Adding a new dashboard

Each dashboard is a standalone Go program in its own subdirectory:

```
dashboards/
└── my-new-dashboard/
    └── main.go
```

Use the example as a starting point:

```go
package main

import (
    "flag"

    sdk "github.com/perses/perses/go-sdk"
    "github.com/perses/perses/go-sdk/dashboard"
)

func main() {
    flag.Parse()

    exec := sdk.NewExec()
    builder, buildErr := dashboard.New("MyNewDashboard",
        dashboard.ProjectName("metal-maintenance-operator"),
    )
    exec.BuildDashboard(builder, buildErr)
}
```

After editing, run `make dashboard-build` to produce `dashboards/built/my-new-dashboard.json` and commit both files. The Go SDK documentation is at [perses.dev/perses/docs/dac/go/](https://perses.dev/perses/docs/dac/go/).

### Referencing the datasource in Go code

Panels that query Prometheus use a `DatasourceVariable` to let users pick their
Prometheus instance at runtime via a `$datasource` drop-down, and the
`mmo.PromQL` helper to wire that variable into each query:

```go
import (
    sdk "github.com/perses/perses/go-sdk"
    "github.com/perses/perses/go-sdk/dashboard"
    "github.com/perses/perses/go-sdk/panel"
    panelgroup "github.com/perses/perses/go-sdk/panel-group"
    listvariable "github.com/perses/perses/go-sdk/variable/list-variable"

    datasourcevariable "github.com/perses/plugins/datasourcevariable/sdk/go"
    promDs "github.com/perses/plugins/prometheus/sdk/go/datasource"
    timeseries "github.com/perses/plugins/timeserieschart/sdk/go"

    mmo "github.com/ironcore-dev/metal-maintenance-operator/dashboards/common"
)

func main() {
    exec := sdk.NewExec()
    builder, buildErr := dashboard.New("my-dashboard",
        dashboard.ProjectName("metal-maintenance-operator"),

        // Declare $datasource — users pick their Prometheus instance at runtime.
        dashboard.AddVariable("datasource",
            listvariable.List(
                datasourcevariable.Datasource(promDs.PluginKind),
            ),
        ),

        dashboard.AddPanelGroup("My Group",
            panelgroup.AddPanel("My Panel",
                timeseries.Chart(),
                panel.AddQuery(
                    mmo.PromQL(
                        `up{job="metal-maintenance-operator"}`,
                        "$datasource",
                    ),
                ),
            ),
        ),
    )
    exec.BuildDashboard(builder, buildErr)
}
```

`mmo.PromQL` passes the datasource reference as a bare string (`"$datasource"`),
which Perses resolves as a variable at render time. Using the SDK's typed
`datasource.Selector` instead would serialise the datasource as a JSON object
and bypass variable substitution.

### Topology configuration

The firmware dashboards display three topology dimensions for each server (building block, node name, zone), sourced from `kube_ironcore_info` metric labels via a PromQL join. The mapping between your deployment's Kubernetes CR labels and the Prometheus metric label names used in the PromQL is controlled by [`dashboards/common/config.yaml`](dashboards/common/config.yaml), which is embedded into the dashboard binary as the default.

**Defaults** (for new deployments using the MMO Helm chart's built-in KSM config):

| Field | Display name | `labelKey` (Prometheus metric label on `kube_ironcore_info`) |
|---|---|---|
| `bb` | Building Block | `buildingBlock` |
| `nodename` | Node | `nodename` |
| `zone` | Zone | `topology_kubernetes_io_zone` |

`labelKey` is the Prometheus metric label name as KSM emits it. KSM sanitizes Kubernetes label keys — replacing `.` and `/` with `_` — so the standard Kubernetes zone label `topology.kubernetes.io/zone` becomes `topology_kubernetes_io_zone` in the metric.

**These defaults must match your actual deployment.** If your BMC and Server CRs use different label keys, create a config override file and pass it at build time:

```yaml
# my-config.yaml — override only the fields that differ from the defaults
topology:
  bb:
    displayName: Rack
    labelKey: "my_org_io_rack"     # from Kubernetes label my-org.io/rack
  nodename:
    labelKey: "hostname"
  # zone: omit to keep the default
```

```sh
# using make (relative path is fine):
make dashboard-build DASHBOARD_CONFIG=dashboards/common/sci-config.yaml

# or directly with percli (requires an absolute path):
percli dac build -f dashboards/firmware-version-compliance/main.go \
  -ojson -m stdout -- -config "$(pwd)/my-config.yaml"
```

Each environment that uses different label conventions needs its own build. Leaving a `labelKey` empty falls back to the canonical metric label name (`bb`, `nodename`, or `zone`).

## Licensing

Copyright 2025 SAP SE or an SAP affiliate company and IronCore contributors. Please see our [LICENSE](LICENSE) for
copyright and license information. Detailed information including third-party components and their licensing/copyright
information is available [via the REUSE tool](https://api.reuse.software/info/github.com/ironcore-dev/metal-maintenance-operator).

<p align="center"><img alt="Bundesministerium für Wirtschaft und Energie (BMWE)-EU funding logo" src="https://apeirora.eu/assets/img/BMWK-EU.png" width="400"/></p>
