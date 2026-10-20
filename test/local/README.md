# Local end-to-end test (no IBM Cloud, no shared cluster)

Runs the real ExternalDNS (upstream image), this webhook and the platform charts in a
throwaway `kind` cluster, against `fake_ibmcloud.py`, an in-memory IAM + DNS Services API.
The fake is pre-seeded with a contract name (`auth.int`) and a route name (`aasc.int`) that
the controller must never touch.

```sh
python3 test/local/fake_ibmcloud.py 9999 &          # GET :9999/_state, :9999/_log
kind create cluster --name extdns
docker build -t external-dns-ibmcloud-webhook:local .
kind load docker-image external-dns-ibmcloud-webhook:local registry.k8s.io/external-dns/external-dns:v0.23.0 --name extdns
kubectl create ns external-dns
kubectl -n external-dns create secret generic external-dns-ibmcloud-operator-apikey --from-literal=apikey=fake-apikey
CCG=<cluster-config-gitops>/clusters/npr01-ese01-eu-fr2-ocp-shared/setup-operators
helm install in-cluster-setup-alb-exposure-policy $CCG/setup-alb-exposure-policy -n default
helm install in-cluster-setup-external-dns $CCG/setup-external-dns -n external-dns -f test/local/values-kind.yaml
```

`values-kind.yaml` points the webhook at the fake on the `kind` network gateway
(`docker network inspect kind`, usually `172.18.0.1`) and sets a fixed UID, which OpenShift
`restricted-v2` normally injects.

Then render a Service with the `network` chart into `int-eu-fr2-<project>-01`, and simulate
the cloud controller (kind has none) by patching its status:

```sh
kubectl -n int-eu-fr2-aasc-01 patch svc alb --subresource=status --type=merge \
  -p '{"status":{"loadBalancer":{"ingress":[{"hostname":"3c6f54b5-eu-fr2.lb.appdomain.cloud"}]}}}'
curl -s localhost:9999/_log
```

Scenarios checked on 2026-10-15: nominal record + TXT owner; recreated ALB updated in
place; contract and route names untouched; another namespace claiming `auth.int` warned by
the policy and ignored by ExternalDNS; a `sit` name ignored by the `int`/`qua` controller;
policy in `Deny` refusing a non-contract port unless `portExceptions` allows it.
