# observability-deploy — Prometheus + Grafana (non-flux)

Standalone, non-flux-coupled observability stack for the cluster and the beehive
swarm. Manifests live at `packaging/observability/`; deployed directly via in-cluster
`kubectl` (the post-flux path), never through the flux GitOps stack being retired.

## Contents (`packaging/observability/`)
- `00-namespace.yaml` — `monitoring` namespace.
- `10-prometheus.yaml` — ServiceAccount + ClusterRole/Binding, scrape config
  ConfigMap (self, kubernetes-nodes, annotation-driven kubernetes-pods), Deployment
  `prom/prometheus:v2.54.1` (emptyDir TSDB, 7d retention), Service `prometheus:9090`.
- `20-grafana.yaml` — Deployment `grafana/grafana:11.2.0`, pre-provisioned Prometheus
  datasource (`http://prometheus.monitoring.svc:9090`, default), Service
  `grafana:80 → 3000`.

## Deploy / re-apply
```
kubectl apply -f packaging/observability/00-namespace.yaml \
              -f packaging/observability/10-prometheus.yaml \
              -f packaging/observability/20-grafana.yaml
kubectl -n monitoring rollout status deploy/prometheus --timeout=180s
kubectl -n monitoring rollout status deploy/grafana    --timeout=180s
```

## Design notes
- Named `prometheus` / `grafana` (distinct from the legacy flux `kube-prometheus-stack-*`
  workloads) so the two can coexist during the flux decommission and the flux stack
  can be removed without an observability outage.
- `emptyDir` storage keeps the rollout dependency-free (default `local-path` SC is
  `WaitForFirstConsumer`); swap in a PVC when persistence is required.
- Grafana admin defaults to `admin`/`admin` — rotate via `GF_SECURITY_ADMIN_PASSWORD`
  (a Secret) for any exposed/production use.

## Definition of done
`kubectl -n monitoring rollout status deploy/grafana --timeout=180s` (approved
`k8s-rollout` framework). Verified live: both deployments rolled out Ready, Grafana
`/api/health` returns `database: ok`, Prometheus `/-/ready` OK, the provisioned
datasource answers `query=up` with live cluster series.
