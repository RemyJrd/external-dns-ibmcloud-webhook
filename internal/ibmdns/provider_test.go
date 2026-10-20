package ibmdns

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"strings"
	"testing"

	"github.com/IBM/go-sdk-core/v5/core"
	"github.com/IBM/networking-go-sdk/dnssvcsv1"

	"sigs.k8s.io/external-dns/endpoint"
	"sigs.k8s.io/external-dns/plan"
)

type fakeClient struct {
	zones   []dnssvcsv1.Dnszone
	records map[string][]dnssvcsv1.ResourceRecord
	nextID  int
	failOn  string
	calls   []string
}

func newFake() *fakeClient {
	return &fakeClient{
		zones: []dnssvcsv1.Dnszone{
			{ID: core.StringPtr("z-lan"), Name: core.StringPtr("estreem.lan")},
			{ID: core.StringPtr("z-io"), Name: core.StringPtr("sit.estreem.io")},
		},
		records: map[string][]dnssvcsv1.ResourceRecord{},
	}
}

func (f *fakeClient) add(zoneID, name, typ, key, value string, ttl int64) {
	f.nextID++
	f.records[zoneID] = append(f.records[zoneID], dnssvcsv1.ResourceRecord{
		ID:    core.StringPtr(fmt.Sprintf("r%d", f.nextID)),
		Name:  core.StringPtr(name),
		Type:  core.StringPtr(typ),
		TTL:   core.Int64Ptr(ttl),
		Rdata: map[string]interface{}{key: value},
	})
}

func page[T any](items []T, offset, limit *int64) []T {
	start := int(*offset)
	if start > len(items) {
		start = len(items)
	}
	end := start + int(*limit)
	if end > len(items) {
		end = len(items)
	}
	return items[start:end]
}

func (f *fakeClient) ListDnszonesWithContext(_ context.Context, o *dnssvcsv1.ListDnszonesOptions) (*dnssvcsv1.ListDnszones, *core.DetailedResponse, error) {
	return &dnssvcsv1.ListDnszones{Dnszones: page(f.zones, o.Offset, o.Limit)}, nil, nil
}

func (f *fakeClient) ListResourceRecordsWithContext(_ context.Context, o *dnssvcsv1.ListResourceRecordsOptions) (*dnssvcsv1.ListResourceRecords, *core.DetailedResponse, error) {
	return &dnssvcsv1.ListResourceRecords{ResourceRecords: page(f.records[*o.DnszoneID], o.Offset, o.Limit)}, nil, nil
}

func (f *fakeClient) CreateResourceRecordWithContext(_ context.Context, o *dnssvcsv1.CreateResourceRecordOptions) (*dnssvcsv1.ResourceRecord, *core.DetailedResponse, error) {

	if err := core.ValidateStruct(o, "createResourceRecordOptions"); err != nil {
		return nil, nil, err
	}
	if *o.Name == f.failOn {
		return nil, nil, errors.New("boom")
	}
	var key, value string
	switch r := o.Rdata.(type) {
	case *dnssvcsv1.ResourceRecordInputRdataRdataARecord:
		key, value = "ip", *r.Ip
	case *dnssvcsv1.ResourceRecordInputRdataRdataCnameRecord:
		key, value = "cname", *r.Cname
	case *dnssvcsv1.ResourceRecordInputRdataRdataTxtRecord:
		key, value = "text", *r.Text
	}
	f.calls = append(f.calls, fmt.Sprintf("create %s %s %s %s %d", *o.DnszoneID, *o.Type, *o.Name, value, *o.TTL))
	f.add(*o.DnszoneID, *o.Name, *o.Type, key, value, *o.TTL)
	return nil, nil, nil
}

func (f *fakeClient) UpdateResourceRecordWithContext(_ context.Context, o *dnssvcsv1.UpdateResourceRecordOptions) (*dnssvcsv1.ResourceRecord, *core.DetailedResponse, error) {
	if err := core.ValidateStruct(o, "updateResourceRecordOptions"); err != nil {
		return nil, nil, err
	}
	f.calls = append(f.calls, fmt.Sprintf("update %s %s %d", *o.DnszoneID, *o.RecordID, *o.TTL))
	return nil, nil, nil
}

func (f *fakeClient) DeleteResourceRecordWithContext(_ context.Context, o *dnssvcsv1.DeleteResourceRecordOptions) (*core.DetailedResponse, error) {
	if err := core.ValidateStruct(o, "deleteResourceRecordOptions"); err != nil {
		return nil, err
	}
	f.calls = append(f.calls, fmt.Sprintf("delete %s %s", *o.DnszoneID, *o.RecordID))
	return nil, nil
}

func newProvider(f *fakeClient, dryRun bool) *Provider {
	return New(f, "instance", endpoint.NewDomainFilter([]string{"estreem.lan"}), dryRun)
}

func TestRecordsFiltersZonesTypesAndGroupsTargets(t *testing.T) {
	f := newFake()
	f.add("z-lan", "auth.int.estreem.lan", "CNAME", "cname", "aasc-alb.int.estreem.lan", 43200)
	f.add("z-lan", "multi.int.estreem.lan", "A", "ip", "10.0.0.1", 300)
	f.add("z-lan", "multi.int.estreem.lan", "A", "ip", "10.0.0.2", 300)
	f.add("z-lan", "mx.estreem.lan", "MX", "exchange", "mail.estreem.lan", 300)
	f.add("z-io", "front.sit.estreem.io", "CNAME", "cname", "x.appdomain.cloud", 300)

	eps, err := newProvider(f, false).Records(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	got := map[string]string{}
	for _, ep := range eps {
		targets := append([]string(nil), ep.Targets...)
		sort.Strings(targets)
		got[ep.RecordType+" "+ep.DNSName] = fmt.Sprintf("%v ttl=%d", targets, ep.RecordTTL)
	}
	want := map[string]string{
		"CNAME auth.int.estreem.lan": "[aasc-alb.int.estreem.lan] ttl=43200",
		"A multi.int.estreem.lan":    "[10.0.0.1 10.0.0.2] ttl=300",
	}
	if fmt.Sprint(got) != fmt.Sprint(want) {
		t.Fatalf("got %v, want %v", got, want)
	}
}

func TestRecordsPaginates(t *testing.T) {
	f := newFake()
	for i := 0; i < pageSize+5; i++ {
		f.add("z-lan", fmt.Sprintf("r%d.estreem.lan", i), "A", "ip", "10.0.0.1", 300)
	}
	eps, err := newProvider(f, false).Records(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(eps) != pageSize+5 {
		t.Fatalf("got %d endpoints, want %d", len(eps), pageSize+5)
	}
}

func TestApplyChangesDeletesOnlyTheMatchingValue(t *testing.T) {
	f := newFake()
	f.add("z-lan", "multi.int.estreem.lan", "A", "ip", "10.0.0.1", 300)
	f.add("z-lan", "multi.int.estreem.lan", "A", "ip", "10.0.0.2", 300)

	err := newProvider(f, false).ApplyChanges(context.Background(), &plan.Changes{
		Delete: []*endpoint.Endpoint{endpoint.NewEndpoint("multi.int.estreem.lan", "A", "10.0.0.2")},
	})
	if err != nil {
		t.Fatal(err)
	}
	if fmt.Sprint(f.calls) != "[delete z-lan r2]" {
		t.Fatalf("calls %v", f.calls)
	}
}

func TestApplyChangesUpdatePairsByNameAndType(t *testing.T) {
	f := newFake()
	f.add("z-lan", "aasc-alb.int.estreem.lan", "CNAME", "cname", "old-eu-fr2.lb.appdomain.cloud", 300)
	f.add("z-lan", "tlp-alb.int.estreem.lan", "CNAME", "cname", "same-eu-fr2.lb.appdomain.cloud", 300)

	err := newProvider(f, false).ApplyChanges(context.Background(), &plan.Changes{

		UpdateOld: []*endpoint.Endpoint{
			endpoint.NewEndpointWithTTL("tlp-alb.int.estreem.lan", "CNAME", 300, "same-eu-fr2.lb.appdomain.cloud"),
			endpoint.NewEndpointWithTTL("aasc-alb.int.estreem.lan", "CNAME", 300, "old-eu-fr2.lb.appdomain.cloud"),
		},
		UpdateNew: []*endpoint.Endpoint{
			endpoint.NewEndpointWithTTL("aasc-alb.int.estreem.lan", "CNAME", 300, "new-eu-fr2.lb.appdomain.cloud"),
			endpoint.NewEndpointWithTTL("tlp-alb.int.estreem.lan", "CNAME", 600, "same-eu-fr2.lb.appdomain.cloud"),
		},
	})
	if err != nil {
		t.Fatal(err)
	}

	want := "[update z-lan r1 300 update z-lan r2 600]"
	if fmt.Sprint(f.calls) != want {
		t.Fatalf("calls %v\nwant  %v", f.calls, want)
	}
}

func TestApplyChangesCreatesInTheLongestMatchingZone(t *testing.T) {
	f := newFake()
	f.zones = append(f.zones, dnssvcsv1.Dnszone{ID: core.StringPtr("z-int"), Name: core.StringPtr("int.estreem.lan")})

	err := newProvider(f, false).ApplyChanges(context.Background(), &plan.Changes{
		Create: []*endpoint.Endpoint{endpoint.NewEndpoint("aasc-alb.int.estreem.lan", "CNAME", "h-eu-fr2.lb.appdomain.cloud")},
	})
	if err != nil {
		t.Fatal(err)
	}
	if fmt.Sprint(f.calls) != "[create z-int CNAME aasc-alb.int.estreem.lan h-eu-fr2.lb.appdomain.cloud 300]" {
		t.Fatalf("calls %v", f.calls)
	}
}

func TestApplyChangesReturnsEveryError(t *testing.T) {
	f := newFake()
	f.failOn = "bad.estreem.lan"

	err := newProvider(f, false).ApplyChanges(context.Background(), &plan.Changes{
		Create: []*endpoint.Endpoint{
			endpoint.NewEndpoint("bad.estreem.lan", "CNAME", "x"),
			endpoint.NewEndpoint("good.estreem.lan", "CNAME", "y"),
			endpoint.NewEndpoint("outside.example.com", "CNAME", "z"),
		},
	})
	if err == nil {
		t.Fatal("expected an error")
	}
	for _, part := range []string{"bad.estreem.lan", "outside.example.com"} {
		if !strings.Contains(err.Error(), part) {
			t.Errorf("error %q does not mention %s", err, part)
		}
	}
	if len(f.calls) != 1 || !strings.Contains(f.calls[0], "good.estreem.lan") {
		t.Fatalf("the valid create must still be applied, calls %v", f.calls)
	}
}

func TestDryRunWritesNothing(t *testing.T) {
	f := newFake()
	f.add("z-lan", "a.estreem.lan", "CNAME", "cname", "x", 300)

	err := newProvider(f, true).ApplyChanges(context.Background(), &plan.Changes{
		Create: []*endpoint.Endpoint{endpoint.NewEndpoint("b.estreem.lan", "CNAME", "y")},
		Delete: []*endpoint.Endpoint{endpoint.NewEndpoint("a.estreem.lan", "CNAME", "x")},
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(f.calls) != 0 {
		t.Fatalf("dry run made calls %v", f.calls)
	}
}

func TestAdjustEndpointsDefaultsTTL(t *testing.T) {
	eps, _ := newProvider(newFake(), false).AdjustEndpoints([]*endpoint.Endpoint{
		endpoint.NewEndpoint("a.estreem.lan", "CNAME", "x"),
		endpoint.NewEndpointWithTTL("b.estreem.lan", "CNAME", 900, "y"),
	})
	if eps[0].RecordTTL != DefaultTTL || eps[1].RecordTTL != 900 {
		t.Fatalf("ttls %d %d", eps[0].RecordTTL, eps[1].RecordTTL)
	}
}

func TestApplyChangesIsIdempotent(t *testing.T) {
	f := newFake()
	f.add("z-lan", "a.estreem.lan", "CNAME", "cname", "x", 300)

	err := newProvider(f, false).ApplyChanges(context.Background(), &plan.Changes{
		Create: []*endpoint.Endpoint{endpoint.NewEndpoint("a.estreem.lan", "CNAME", "x")},
		Delete: []*endpoint.Endpoint{endpoint.NewEndpoint("gone.estreem.lan", "CNAME", "y")},
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(f.calls) != 0 {
		t.Fatalf("replay made calls %v", f.calls)
	}
}
