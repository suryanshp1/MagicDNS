// Package registry compiles records into immutable DNS lookup snapshots.
package registry

import (
	"fmt"
	"sort"
	"strings"

	"github.com/devmesh-dns/devmesh/internal/config"
	"github.com/miekg/dns"
)

type Snapshot struct {
	zones []*Zone
}

type Zone struct {
	name    string
	ttl     uint32
	records map[string]map[uint16][]dns.RR
}

type Result struct {
	Managed bool
	Found   bool
	Answers []dns.RR
	SOA     dns.RR
}

func New(zones []config.Zone) (*Snapshot, error) {
	snapshot := &Snapshot{zones: make([]*Zone, 0, len(zones))}
	for _, source := range zones {
		zone := &Zone{
			name:    source.Name,
			ttl:     source.TTL,
			records: make(map[string]map[uint16][]dns.RR),
		}
		for _, record := range source.Records {
			if zone.records[record.Name] == nil {
				zone.records[record.Name] = make(map[uint16][]dns.RR)
			}
			for _, value := range record.Values {
				rr, err := dns.NewRR(fmt.Sprintf("%s %d IN %s %s", record.Name, record.TTL, dns.TypeToString[record.Type], value))
				if err != nil {
					return nil, fmt.Errorf("compile record %s: %w", record.Name, err)
				}
				zone.records[record.Name][record.Type] = append(zone.records[record.Name][record.Type], rr)
			}
		}
		snapshot.zones = append(snapshot.zones, zone)
	}
	sort.Slice(snapshot.zones, func(i, j int) bool {
		return dns.CountLabel(snapshot.zones[i].name) > dns.CountLabel(snapshot.zones[j].name)
	})
	return snapshot, nil
}

func (s *Snapshot) Lookup(name string, qtype uint16) Result {
	name = strings.ToLower(dns.Fqdn(name))
	zone := s.findZone(name)
	if zone == nil {
		return Result{}
	}
	if byType, ok := zone.records[name]; ok {
		return Result{Managed: true, Found: true, Answers: answersFor(byType, qtype, name), SOA: zone.soa()}
	}
	if byType, ok := zone.wildcard(name); ok {
		return Result{Managed: true, Found: true, Answers: answersFor(byType, qtype, name), SOA: zone.soa()}
	}
	return Result{Managed: true, Found: false, SOA: zone.soa()}
}

func (s *Snapshot) findZone(name string) *Zone {
	for _, zone := range s.zones {
		if dns.IsSubDomain(zone.name, name) {
			return zone
		}
	}
	return nil
}

func (z *Zone) wildcard(name string) (map[uint16][]dns.RR, bool) {
	labels := dns.SplitDomainName(name)
	zoneLabels := dns.CountLabel(z.name)
	for remove := 1; remove <= len(labels)-zoneLabels; remove++ {
		closest := strings.Join(labels[remove:], ".") + "."
		if z.nodeExists(closest) {
			records, ok := z.records["*."+closest]
			return records, ok
		}
	}
	return nil, false
}

func (z *Zone) nodeExists(name string) bool {
	if name == z.name {
		return true
	}
	if _, ok := z.records[name]; ok {
		return true
	}
	for owner := range z.records {
		if owner != name && dns.IsSubDomain(name, owner) {
			return true
		}
	}
	return false
}

func (z *Zone) soa() dns.RR {
	return &dns.SOA{
		Hdr:     dns.RR_Header{Name: z.name, Rrtype: dns.TypeSOA, Class: dns.ClassINET, Ttl: z.ttl},
		Ns:      "ns1." + z.name,
		Mbox:    "hostmaster." + z.name,
		Serial:  1,
		Refresh: 300,
		Retry:   60,
		Expire:  86400,
		Minttl:  z.ttl,
	}
}

func answersFor(byType map[uint16][]dns.RR, qtype uint16, owner string) []dns.RR {
	var source []dns.RR
	if qtype == dns.TypeANY {
		types := make([]int, 0, len(byType))
		for recordType := range byType {
			types = append(types, int(recordType))
		}
		sort.Ints(types)
		for _, recordType := range types {
			source = append(source, byType[uint16(recordType)]...)
		}
	} else {
		source = append(source, byType[qtype]...)
		if qtype != dns.TypeCNAME && len(source) == 0 {
			source = append(source, byType[dns.TypeCNAME]...)
		}
	}
	answers := make([]dns.RR, 0, len(source))
	for _, rr := range source {
		clone := dns.Copy(rr)
		clone.Header().Name = owner
		answers = append(answers, clone)
	}
	return answers
}
