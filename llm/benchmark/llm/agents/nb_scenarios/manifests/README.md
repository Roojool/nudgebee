# nb_scenarios — persistent failure scenarios with known ground truth

Ten Kubernetes failure scenarios live in one namespace, `benchmark-scenarios`,
deployed once and left running. Fixtures have no `before_test` / `after_test`;
verify the infrastructure before a run instead.

| # | Workload | Failure | Ground truth the answer must contain |
|---|---|---|---|
| 001 | `payment-worker` | CrashLoopBackOff | env `DB_URL` not set, log `FATAL: DB_URL is not set…`, exit 1 |
| 002 | `memory-hog` | OOMKilled | limit `64Mi`, allocates ~200M |
| 003 | `catalog-api` | ImagePullBackOff | image `nginx:does-not-exist-9.9.9` |
| 004 | `batch-analytics` | Pending | FailedScheduling, requests `64` CPU |
| 005 | `orders-svc` | no endpoints | selector `app=orders-api` vs pods `app=orders-api-v2` |
| 006 | `checkout-api` | Running, not Ready | readiness probe port `8081`, container port `80` |
| 007 | `inventory-api` | app errors in logs | `ERROR failed to connect to inventory-db:5432: connection refused (attempt N)` every 10 s |
| 008 | `shipping-api` | CreateContainerConfigError | ConfigMap `shipping-config` has no key `LOG_LEVEL` |
| 009 | `nightly-report` CronJob | every run fails | exit 2, `cannot write /data/report.csv: permission denied`, every 2 min |
| 010 | `analytics-db` | Pending PVC | PVC `analytics-data` → StorageClass `fast-ssd-v9` does not exist |

```bash
kubectl apply -f all.yaml                      # create / refresh everything
kubectl get pods,pvc,cronjob -n benchmark-scenarios
kubectl get events -n benchmark-scenarios --sort-by=.lastTimestamp | tail -20
kubectl logs -n benchmark-scenarios deploy/inventory-api --tail=4   # 007 lines present?
```

Before trusting 007 and 009, confirm the log pipeline has the namespace
(`{namespace="benchmark-scenarios"}` in Loki returns streams); ingestion lags a
few minutes after first apply.

Run: agent `nb_scenarios`, any account whose cluster hosts the namespace (dev:
`k8s-dev`). Tear down with `kubectl delete namespace benchmark-scenarios`.
