# argus-agent

Run it on a host or node and everything running there shows up in Argus under **Services**. There is nothing to configure per service.

| Mode | Source | Service name |
|------|--------|--------------|
| `journal` | systemd journal (`journalctl -f`) | the unit, e.g. `nginx.service` becomes `nginx` |
| `kubernetes` | `/var/log/pods` container logs | the owning workload, e.g. pod `api-7d9f8b6c5d-x2k4p` becomes `api` |

`ARGUS_AGENT_MODE=auto` (the default) uses `kubernetes` if `/var/log/pods` exists, else `journal`.

Pod and workload names come from the log file path, not the Kubernetes API, so the agent needs no RBAC. Labels attached: `service`, `namespace`, `pod`, `container` (Kubernetes); `service`, `unit`, `host` (journal); plus `source` and anything in `ARGUS_LABELS`.

## Configuration

| Variable | Default | Meaning |
|----------|---------|---------|
| `ARGUS_API_KEY` | required | ingest key (`POST /api/v1/api-keys` as admin) |
| `ARGUS_URL` | `http://localhost:8080` | Argus base URL |
| `ARGUS_AGENT_MODE` | `auto` | `auto`, `journal` or `kubernetes` |
| `ARGUS_LABELS` | | extra labels for every stream: `env=prod,cluster=a` |
| `ARGUS_EXCLUDE_NAMESPACES` | | comma list of namespaces to skip (Kubernetes) |
| `ARGUS_PODS_DIR` | `/var/log/pods` | pod log root |
| `ARGUS_STATE_DIR` | `/var/lib/argus-agent` | where read positions are kept |

## Install

systemd host:

```sh
cd agent && GOOS=linux go build -o argus-agent . && sudo install argus-agent /usr/local/bin/
printf 'ARGUS_URL=http://argus:8080\nARGUS_API_KEY=argus_xxx\n' | sudo tee /etc/argus-agent.env
sudo cp deploy/argus-agent.service /etc/systemd/system/ && sudo systemctl enable --now argus-agent
```

Kubernetes: build `Dockerfile` from the repo root, push it, then follow the header of `deploy/daemonset.yaml`.

## Behavior to know

- On first start the agent follows files and the journal from now on; it does not replay history. After that it resumes from the saved position, so a restart neither loses nor duplicates lines.
- New pod log files are found within about 5 seconds and read from their start.
- Kubernetes rotates a container's log by renaming `0.log`; the last lines written to the old file just before a rotation can be missed.
- Every distinct label set is one stream with its own queue. The agent stops creating new ones after 500, and Argus enforces `MaxStreamsPerKey` too. Keep `ARGUS_LABELS` low-cardinality.
- If Argus is down, lines are retried a few times and then dropped rather than buffered without limit.
- Not yet verified against a real cluster or a real journal; the unit tests cover parsing, discovery and the offset logic with a fake log directory.
