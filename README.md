# external-dns-ibmcloud-webhook

[ExternalDNS](https://github.com/kubernetes-sigs/external-dns) webhook provider for the
**private zones of an IBM Cloud DNS Services instance**.

ExternalDNS removed its in-tree `ibmcloud` provider in v0.18.0 and no IBM Cloud webhook
exists upstream. This sidecar fills the gap: ExternalDNS (latest upstream image) talks to it
over the webhook protocol on `127.0.0.1:8888`, and it reads and writes A, CNAME and TXT
records through the DNS Services API.

## Behaviour

- Manages the zones of `DNS_INSTANCE_ID` whose name matches `DOMAIN_FILTER`.
- A record is matched by name, type **and value**: one value of a multi-value name is never
  mistaken for another.
- Every API failure is returned to ExternalDNS, which retries on its next loop. Creates and
  deletes are idempotent, so a replayed request neither duplicates nor fails.
- Endpoints without a TTL get 300 s.
- Exits at startup if the key, the instance or the filter yields no zone.

## Configuration

| Variable | Default | |
|---|---|---|
| `IBMCLOUD_API_KEY` | | API key with `Manager` on the DNS Services instance |
| `DNS_INSTANCE_ID` | | GUID of the DNS Services instance |
| `DOMAIN_FILTER` | | Comma-separated zones, e.g. `estreem.lan` |
| `IAM_URL` | `https://private.iam.cloud.ibm.com` | |
| `DNS_API_URL` | `https://api.private.dns-svcs.cloud.ibm.com/v1` | |
| `DRY_RUN` | `false` | Log changes without applying them |
| `LOG_LEVEL` | `info` | |
| `PROVIDER_ADDR` | `127.0.0.1:8888` | Webhook API, localhost only |
| `HEALTH_ADDR` | `:8080` | `GET /healthz`, used by the chart probes |

## Development

```sh
go vet ./... && go test -race ./...
docker build -t external-dns-ibmcloud-webhook:local .
```

`internal/ibmdns/webhook_protocol_test.go` runs the provider behind the real ExternalDNS
webhook server and client, so a protocol change upstream breaks the build.

## Release

Tag `vX.Y.Z`: the pipeline pushes `$CI_REGISTRY_IMAGE:vX.Y.Z`. The image then reaches the
clusters through the ICR mirror (`ocp-gitops/container-images`).
