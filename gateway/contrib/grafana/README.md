# Grafana dashboard for the PEB gateway

`peb-dashboard.json` (Grafana 10 and 11) shows backups by status, data added, backup duration per server, repository size against capacity, storage per server, maintenance state, the offsite copy backlog and lag, and the spool.

## 1. Let Prometheus scrape the gateway

Create a token (at least 32 characters) and set it as `PEB_METRICS_TOKEN` on the gateway. If you leave it empty, `PEB_ADMIN_TOKEN` is accepted on `/metrics` instead. Store the token alone in a file readable by Prometheus:

```yaml
scrape_configs:
  - job_name: peb-gateway
    scheme: https
    metrics_path: /metrics
    scrape_interval: 60s
    scrape_timeout: 10s   # the gateway answers within about 6 s even if the repository hangs
    authorization:
      type: Bearer
      credentials_file: /etc/prometheus/peb-token
    static_configs:
      - targets: ["gateway.example.org"]
```

(`bearer_token_file: /etc/prometheus/peb-token` is the older spelling of the same setting.) Check with `curl -H "Authorization: Bearer $(cat /etc/prometheus/peb-token)" https://gateway.example.org/metrics`.

## 2. Import the dashboard

Grafana: Dashboards, New, Import, upload `peb-dashboard.json`, and pick your Prometheus data source for `DS_PROMETHEUS`.

Notes:

- Server panels are labelled with the server UUID, because the gateway does not know server names. Use a Grafana value mapping or a transformation if you want names.
- Metrics that are not measured yet (for example the offsite panels when no secondary repository is configured) are absent, so those panels stay empty.
- Panels that need a long history (data added per day) fill up as Prometheus collects data.
