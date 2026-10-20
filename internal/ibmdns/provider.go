package ibmdns

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/IBM/go-sdk-core/v5/core"
	"github.com/IBM/networking-go-sdk/dnssvcsv1"
	log "github.com/sirupsen/logrus"

	"sigs.k8s.io/external-dns/endpoint"
	"sigs.k8s.io/external-dns/plan"
	"sigs.k8s.io/external-dns/provider"
)

const (
	DefaultTTL = 300
	pageSize   = 200
)

var errNotFound = errors.New("record not found")

var rdataKey = map[string]string{
	endpoint.RecordTypeA:     "ip",
	endpoint.RecordTypeCNAME: "cname",
	endpoint.RecordTypeTXT:   "text",
}

type Client interface {
	ListDnszonesWithContext(context.Context, *dnssvcsv1.ListDnszonesOptions) (*dnssvcsv1.ListDnszones, *core.DetailedResponse, error)
	ListResourceRecordsWithContext(context.Context, *dnssvcsv1.ListResourceRecordsOptions) (*dnssvcsv1.ListResourceRecords, *core.DetailedResponse, error)
	CreateResourceRecordWithContext(context.Context, *dnssvcsv1.CreateResourceRecordOptions) (*dnssvcsv1.ResourceRecord, *core.DetailedResponse, error)
	UpdateResourceRecordWithContext(context.Context, *dnssvcsv1.UpdateResourceRecordOptions) (*dnssvcsv1.ResourceRecord, *core.DetailedResponse, error)
	DeleteResourceRecordWithContext(context.Context, *dnssvcsv1.DeleteResourceRecordOptions) (*core.DetailedResponse, error)
}

type Provider struct {
	provider.BaseProvider
	client       Client
	instanceID   string
	domainFilter *endpoint.DomainFilter
	dryRun       bool
}

func New(client Client, instanceID string, domainFilter *endpoint.DomainFilter, dryRun bool) *Provider {
	return &Provider{client: client, instanceID: instanceID, domainFilter: domainFilter, dryRun: dryRun}
}

type record struct {
	id    string
	name  string
	typ   string
	value string
	ttl   int64
}

func (p *Provider) GetDomainFilter() endpoint.DomainFilterInterface {
	return p.domainFilter
}

func (p *Provider) Zones(ctx context.Context) (provider.ZoneIDName, error) {
	zones := provider.ZoneIDName{}
	for offset := int64(0); ; offset += pageSize {
		res, _, err := p.client.ListDnszonesWithContext(ctx, &dnssvcsv1.ListDnszonesOptions{
			InstanceID: core.StringPtr(p.instanceID),
			Offset:     core.Int64Ptr(offset),
			Limit:      core.Int64Ptr(pageSize),
		})
		if err != nil {
			return nil, fmt.Errorf("listing zones: %w", err)
		}
		for _, z := range res.Dnszones {
			if p.domainFilter.Match(*z.Name) {
				zones.Add(*z.ID, *z.Name)
			}
		}
		if len(res.Dnszones) < pageSize {
			return zones, nil
		}
	}
}

func (p *Provider) Records(ctx context.Context) ([]*endpoint.Endpoint, error) {
	zones, err := p.Zones(ctx)
	if err != nil {
		return nil, err
	}
	var endpoints []*endpoint.Endpoint
	for zoneID := range zones {
		records, err := p.listRecords(ctx, zoneID)
		if err != nil {
			return nil, err
		}
		endpoints = append(endpoints, group(records)...)
	}
	return endpoints, nil
}

func (p *Provider) AdjustEndpoints(endpoints []*endpoint.Endpoint) ([]*endpoint.Endpoint, error) {
	for _, ep := range endpoints {
		if !ep.RecordTTL.IsConfigured() {
			ep.RecordTTL = DefaultTTL
		}
	}
	return endpoints, nil
}

func (p *Provider) ApplyChanges(ctx context.Context, changes *plan.Changes) error {
	zones, err := p.Zones(ctx)
	if err != nil {
		return err
	}
	current := map[string][]record{}
	recordsOf := func(zoneID string) ([]record, error) {
		if r, ok := current[zoneID]; ok {
			return r, nil
		}
		r, err := p.listRecords(ctx, zoneID)
		if err != nil {
			return nil, err
		}
		current[zoneID] = r
		return r, nil
	}

	var errs []error
	for _, ep := range changes.Delete {
		for _, target := range ep.Targets {
			errs = append(errs, p.deleteRecord(ctx, zones, recordsOf, ep, target))
		}
	}

	old := map[updateKey]*endpoint.Endpoint{}
	for _, ep := range changes.UpdateOld {
		old[keyOf(ep)] = ep
	}
	for _, desired := range changes.UpdateNew {
		previous, ok := old[keyOf(desired)]
		if !ok {
			errs = append(errs, fmt.Errorf("update of %s %s has no previous state", desired.RecordType, desired.DNSName))
			continue
		}
		add, remove, leave := provider.Difference(previous.Targets, desired.Targets)

		n := min(len(add), len(remove))
		for i := 0; i < n; i++ {
			errs = append(errs, p.updateRecord(ctx, zones, recordsOf, desired, remove[i], add[i]))
		}
		for _, target := range remove[n:] {
			errs = append(errs, p.deleteRecord(ctx, zones, recordsOf, previous, target))
		}
		for _, target := range leave {
			if previous.RecordTTL != desired.RecordTTL {
				errs = append(errs, p.updateRecord(ctx, zones, recordsOf, desired, target, target))
			}
		}
		for _, target := range add[n:] {
			errs = append(errs, p.createRecord(ctx, zones, recordsOf, desired, target))
		}
	}

	for _, ep := range changes.Create {
		for _, target := range ep.Targets {
			errs = append(errs, p.createRecord(ctx, zones, recordsOf, ep, target))
		}
	}
	return errors.Join(errs...)
}

type updateKey struct{ name, typ, setID string }

func keyOf(ep *endpoint.Endpoint) updateKey {
	return updateKey{normalize(ep.DNSName), ep.RecordType, ep.SetIdentifier}
}

func (p *Provider) listRecords(ctx context.Context, zoneID string) ([]record, error) {
	var records []record
	for offset := int64(0); ; offset += pageSize {
		res, _, err := p.client.ListResourceRecordsWithContext(ctx, &dnssvcsv1.ListResourceRecordsOptions{
			InstanceID: core.StringPtr(p.instanceID),
			DnszoneID:  core.StringPtr(zoneID),
			Offset:     core.Int64Ptr(offset),
			Limit:      core.Int64Ptr(pageSize),
		})
		if err != nil {
			return nil, fmt.Errorf("listing records of zone %s: %w", zoneID, err)
		}
		for _, r := range res.ResourceRecords {
			key, ok := rdataKey[*r.Type]
			if !ok {
				continue
			}
			value, _ := r.Rdata[key].(string)
			records = append(records, record{id: *r.ID, name: normalize(*r.Name), typ: *r.Type, value: value, ttl: *r.TTL})
		}
		if len(res.ResourceRecords) < pageSize {
			return records, nil
		}
	}
}

func group(records []record) []*endpoint.Endpoint {
	type nameType struct{ name, typ string }
	byKey := map[nameType]*endpoint.Endpoint{}
	var endpoints []*endpoint.Endpoint
	for _, r := range records {
		k := nameType{r.name, r.typ}
		if ep, ok := byKey[k]; ok {
			ep.Targets = append(ep.Targets, r.value)
			continue
		}
		ep := endpoint.NewEndpointWithTTL(r.name, r.typ, endpoint.TTL(r.ttl), r.value)
		byKey[k] = ep
		endpoints = append(endpoints, ep)
	}
	return endpoints
}

func (p *Provider) createRecord(ctx context.Context, zones provider.ZoneIDName, recordsOf func(string) ([]record, error), ep *endpoint.Endpoint, target string) error {
	zoneID, err := zoneFor(zones, ep.DNSName)
	if err != nil {
		return err
	}

	if _, _, err := p.find(zones, recordsOf, ep, target); err == nil {
		log.WithFields(log.Fields{"record": ep.DNSName, "type": ep.RecordType, "target": target}).Info("Record already present")
		return nil
	}
	opts := &dnssvcsv1.CreateResourceRecordOptions{
		InstanceID: core.StringPtr(p.instanceID),
		DnszoneID:  core.StringPtr(zoneID),
		Name:       core.StringPtr(normalize(ep.DNSName)),
		Type:       core.StringPtr(ep.RecordType),
		TTL:        core.Int64Ptr(ttlOf(ep)),
	}
	switch ep.RecordType {
	case endpoint.RecordTypeA:
		opts.Rdata = &dnssvcsv1.ResourceRecordInputRdataRdataARecord{Ip: core.StringPtr(target)}
	case endpoint.RecordTypeCNAME:
		opts.Rdata = &dnssvcsv1.ResourceRecordInputRdataRdataCnameRecord{Cname: core.StringPtr(target)}
	case endpoint.RecordTypeTXT:
		opts.Rdata = &dnssvcsv1.ResourceRecordInputRdataRdataTxtRecord{Text: core.StringPtr(target)}
	default:
		return fmt.Errorf("record type %s of %s is not supported", ep.RecordType, ep.DNSName)
	}
	p.logChange("create", ep, target)
	if p.dryRun {
		return nil
	}
	if _, _, err := p.client.CreateResourceRecordWithContext(ctx, opts); err != nil {
		return fmt.Errorf("creating %s %s -> %s: %w", ep.RecordType, ep.DNSName, target, err)
	}
	return nil
}

func (p *Provider) updateRecord(ctx context.Context, zones provider.ZoneIDName, recordsOf func(string) ([]record, error), ep *endpoint.Endpoint, oldTarget, target string) error {
	zoneID, r, err := p.find(zones, recordsOf, ep, oldTarget)
	if err != nil {
		return err
	}
	opts := &dnssvcsv1.UpdateResourceRecordOptions{
		InstanceID: core.StringPtr(p.instanceID),
		DnszoneID:  core.StringPtr(zoneID),
		RecordID:   core.StringPtr(r.id),
		Name:       core.StringPtr(r.name),
		TTL:        core.Int64Ptr(ttlOf(ep)),
	}
	switch ep.RecordType {
	case endpoint.RecordTypeA:
		opts.Rdata = &dnssvcsv1.ResourceRecordUpdateInputRdataRdataARecord{Ip: core.StringPtr(target)}
	case endpoint.RecordTypeCNAME:
		opts.Rdata = &dnssvcsv1.ResourceRecordUpdateInputRdataRdataCnameRecord{Cname: core.StringPtr(target)}
	case endpoint.RecordTypeTXT:
		opts.Rdata = &dnssvcsv1.ResourceRecordUpdateInputRdataRdataTxtRecord{Text: core.StringPtr(target)}
	}
	p.logChange("update", ep, target)
	if p.dryRun {
		return nil
	}
	if _, _, err := p.client.UpdateResourceRecordWithContext(ctx, opts); err != nil {
		return fmt.Errorf("updating %s %s -> %s: %w", ep.RecordType, ep.DNSName, target, err)
	}
	return nil
}

func (p *Provider) deleteRecord(ctx context.Context, zones provider.ZoneIDName, recordsOf func(string) ([]record, error), ep *endpoint.Endpoint, target string) error {
	zoneID, r, err := p.find(zones, recordsOf, ep, target)
	if errors.Is(err, errNotFound) {

		log.WithFields(log.Fields{"record": ep.DNSName, "type": ep.RecordType, "target": target}).Warn("Record to delete not found")
		return nil
	}
	if err != nil {
		return err
	}
	p.logChange("delete", ep, target)
	if p.dryRun {
		return nil
	}
	if _, err := p.client.DeleteResourceRecordWithContext(ctx, &dnssvcsv1.DeleteResourceRecordOptions{
		InstanceID: core.StringPtr(p.instanceID),
		DnszoneID:  core.StringPtr(zoneID),
		RecordID:   core.StringPtr(r.id),
	}); err != nil {
		return fmt.Errorf("deleting %s %s -> %s: %w", ep.RecordType, ep.DNSName, target, err)
	}
	return nil
}

func (p *Provider) find(zones provider.ZoneIDName, recordsOf func(string) ([]record, error), ep *endpoint.Endpoint, target string) (string, record, error) {
	zoneID, err := zoneFor(zones, ep.DNSName)
	if err != nil {
		return "", record{}, err
	}
	records, err := recordsOf(zoneID)
	if err != nil {
		return "", record{}, err
	}
	name := normalize(ep.DNSName)
	for _, r := range records {
		if r.name == name && r.typ == ep.RecordType && r.value == target {
			return zoneID, r, nil
		}
	}
	return "", record{}, fmt.Errorf("%w: %s %s -> %s", errNotFound, ep.RecordType, ep.DNSName, target)
}

func zoneFor(zones provider.ZoneIDName, name string) (string, error) {
	zoneID, _ := zones.FindZone(normalize(name))
	if zoneID == "" {
		return "", fmt.Errorf("no managed zone for %s", name)
	}
	return zoneID, nil
}

func ttlOf(ep *endpoint.Endpoint) int64 {
	if ep.RecordTTL.IsConfigured() {
		return int64(ep.RecordTTL)
	}
	return DefaultTTL
}

func normalize(name string) string {
	return strings.ToLower(strings.TrimSuffix(name, "."))
}

func (p *Provider) logChange(action string, ep *endpoint.Endpoint, target string) {
	log.WithFields(log.Fields{
		"action": action,
		"record": ep.DNSName,
		"type":   ep.RecordType,
		"target": target,
		"ttl":    ttlOf(ep),
		"dryRun": p.dryRun,
	}).Info("Changing record")
}
