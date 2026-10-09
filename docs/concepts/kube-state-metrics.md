# kube-state-metrics integration

The chart can ship a [kube-state-metrics](https://github.com/kubernetes/kube-state-metrics)
(KSM) **CustomResourceState** configuration so KSM turns the operator's Custom
Resources into Prometheus metrics. When `kubeStateMetrics.enabled: true`, the chart
renders a ConfigMap (`<release>-ironcore-crs`, from
[`templates/extras/kube-state-metrics-crs.yaml`](../../dist/chart/templates/extras/kube-state-metrics-crs.yaml))
whose `custom.yaml` key holds a `CustomResourceStateMetrics` spec covering the
`ironcore.dev` sub-groups. It produces:

- `kube_ironcore_info` — one info metric per ironcore.dev CR, all CR labels passed
  through, distinguished by the auto-added `customresource_group` /
  `customresource_kind` labels.

The firmware-version-compliance dashboard joins its own metrics onto
`kube_ironcore_info` (e.g. `... * on(bmc) group_left(...) kube_ironcore_info{customresource_kind="BMC"}`),
so this config is the contract between the operator's CRs and the dashboards.

## The ConfigMap does nothing on its own

This is the important part. **Upstream kube-state-metrics has no label-based
ConfigMap discovery.** KSM reads a single CRS file, passed via
`--custom-resource-state-config-file=/path/to/custom.yaml` (or inline via
`--custom-resource-state-config`), and watches that file for changes — edits to
the file on disk are picked up automatically without a restart. There is no
directory/glob support, and it exits with an error if the merged config contains
duplicate entries for the same resource.

So the ConfigMap this chart renders is inert until KSM is pointed at its content.
You can do this by mounting the ConfigMap directly as a Pod volume at the path KSM
reads (KSM watches that file for changes), or by using a sidecar or initContainer
that discovers ConfigMaps by the label and copies the content:

```
dev.custom.kube-state-metrics: "true"
```

(configurable via `kubeStateMetrics.labels`; if you change the key or value there,
update the sidecar's `LABEL` and `LABEL_VALUE` env vars in the `load-crs` container
to match, otherwise the sidecar will not find the ConfigMap).

## Consuming the ConfigMap

Pick the consumer that matches your environment:

| Consumer | Open source | Multiple ConfigMaps | Notes |
|----------|:-----------:|:-------------------:|-------|
| [`kiwigrid/k8s-sidecar`](https://github.com/kiwigrid/k8s-sidecar) | ✅ | ❌ (one file per key) | Simplest; ideal when this operator is the only producer. |
| Greenhouse `kube-monitoring` plugin | ✅ | depends on setup | Used by the SAP Greenhouse deployment path. |

Because vanilla KSM reads a single file, `kiwigrid/k8s-sidecar` (which writes one
file per ConfigMap key) cleanly supports a **single** producer ConfigMap.

### Reference wiring (open source, single producer)

Run the community `prometheus-community/kube-state-metrics` chart with
`kiwigrid/k8s-sidecar` as an initContainer that copies the labeled ConfigMap into a
shared volume KSM reads:

```yaml
# kube-state-metrics chart values
extraArgs:
  - --custom-resource-state-config-file=/data/custom.yaml
volumes:
  - name: crs
    emptyDir: {}
volumeMounts:
  - name: crs
    mountPath: /data
initContainers:
  # Seed an empty valid file so KSM never starts against a missing path.
  - name: seed-crs
    image: busybox:1.37
    command: ["/bin/sh", "-ec", "printf 'kind: CustomResourceStateMetrics\nspec:\n  resources: []\n' > /data/custom.yaml"]
    volumeMounts: [{name: crs, mountPath: /data}]
  # Copy the operator's ConfigMap (key custom.yaml) into /data/custom.yaml.
  - name: load-crs
    image: ghcr.io/kiwigrid/k8s-sidecar:2.11.2
    env:
      - {name: METHOD, value: LIST}          # run once, then exit
      - {name: LABEL, value: dev.custom.kube-state-metrics}
      - {name: LABEL_VALUE, value: "true"}
      - {name: RESOURCE, value: configmap}
      - {name: NAMESPACE, value: <operator-namespace>}
      - {name: FOLDER, value: /data}
    volumeMounts: [{name: crs, mountPath: /data}]
rbac:
  extraRules:                                # read the ConfigMap, CRDs, and the CRs
    - {apiGroups: [""], resources: ["configmaps"], verbs: ["get","list","watch"]}
    - {apiGroups: ["apiextensions.k8s.io"], resources: ["customresourcedefinitions"], verbs: ["get","list","watch"]}
    - apiGroups: [metal.ironcore.dev, baseboard.metal.ironcore.dev, config.metal.ironcore.dev,
                  maintenance.metal.ironcore.dev, readiness.metal.ironcore.dev, system.metal.ironcore.dev,
                  vendorconsole.metal.ironcore.dev, boot.ironcore.dev]
      resources: ["*"]    # grants read access to every current and future resource in these groups
      verbs: ["get","list","watch"]
```

Because `load-crs` is a one-shot initContainer, it only runs when the Pod starts.
If the ConfigMap changes after startup, the Pod must be restarted to rerun
`load-crs` and write the updated content to `/data/custom.yaml`.

## Enabling

Set in the chart values:

```yaml
kubeStateMetrics:
  enabled: true
  # extraResources: []   # append third-party CustomResourceState resource entries
```

Then deploy a KSM consumer as described above. Verify with:

```sh
kubectl port-forward -n <ksm-namespace> svc/kube-state-metrics 8080:8080
curl -s localhost:8080/metrics | grep kube_ironcore_info
```
