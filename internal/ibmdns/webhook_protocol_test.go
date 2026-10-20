package ibmdns

import (
	"context"
	"fmt"
	"net"
	"testing"
	"time"

	"sigs.k8s.io/external-dns/endpoint"
	"sigs.k8s.io/external-dns/pkg/apis/externaldns"
	"sigs.k8s.io/external-dns/plan"
	"sigs.k8s.io/external-dns/provider/webhook"
	"sigs.k8s.io/external-dns/provider/webhook/api"
)

func TestWebhookProtocol(t *testing.T) {
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	addr := l.Addr().String()
	l.Close()

	f := newFake()
	f.add("z-lan", "auth.int.estreem.lan", "CNAME", "cname", "aasc-alb.int.estreem.lan", 43200)

	started := make(chan struct{})
	go api.StartHTTPApi(api.ServerOptions{
		Provider:     newProvider(f, false),
		StartedChan:  started,
		ProviderPort: addr,
		ReadTimeout:  5 * time.Second,
		WriteTimeout: 5 * time.Second,
	})
	<-started

	ctx := context.Background()
	client, err := webhook.New(ctx, &externaldns.Config{
		WebhookProviderURL:          "http://" + addr,
		WebhookProviderReadTimeout:  5 * time.Second,
		WebhookProviderWriteTimeout: 5 * time.Second,
	}, nil)
	if err != nil {
		t.Fatal(err)
	}

	if !client.GetDomainFilter().Match("aasc-alb.int.estreem.lan") || client.GetDomainFilter().Match("example.com") {
		t.Fatal("domain filter not negotiated")
	}

	eps, err := client.Records(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(eps) != 1 || eps[0].DNSName != "auth.int.estreem.lan" || eps[0].Targets[0] != "aasc-alb.int.estreem.lan" {
		t.Fatalf("records %v", eps)
	}

	adjusted, err := client.AdjustEndpoints([]*endpoint.Endpoint{endpoint.NewEndpoint("aasc-alb.int.estreem.lan", "CNAME", "h-eu-fr2.lb.appdomain.cloud")})
	if err != nil {
		t.Fatal(err)
	}
	if adjusted[0].RecordTTL != DefaultTTL {
		t.Fatalf("adjusted ttl %d", adjusted[0].RecordTTL)
	}

	if err := client.ApplyChanges(ctx, &plan.Changes{Create: adjusted}); err != nil {
		t.Fatal(err)
	}
	want := fmt.Sprintf("[create z-lan CNAME aasc-alb.int.estreem.lan h-eu-fr2.lb.appdomain.cloud %d]", DefaultTTL)
	if fmt.Sprint(f.calls) != want {
		t.Fatalf("calls %v", f.calls)
	}
}
