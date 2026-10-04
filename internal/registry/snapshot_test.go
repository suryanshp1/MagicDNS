package registry

import (
	"testing"

	"github.com/devmesh-dns/devmesh/internal/config"
	"github.com/miekg/dns"
)

func TestSnapshotExactWildcardAndNODATA(t *testing.T) {
	t.Parallel()
	snapshot := mustSnapshot(t, []config.Zone{{
		Name: "project.internal.",
		TTL:  30,
		Records: []config.Record{
			{Name: "api.project.internal.", Type: dns.TypeA, TTL: 30, Values: []string{"127.0.0.1"}},
			{Name: "*.web.project.internal.", Type: dns.TypeCNAME, TTL: 30, Values: []string{"api.project.internal."}},
		},
	}})

	exact := snapshot.Lookup("API.PROJECT.INTERNAL", dns.TypeA)
	if !exact.Managed || !exact.Found || len(exact.Answers) != 1 {
		t.Fatalf("exact lookup = %+v", exact)
	}
	if got := exact.Answers[0].(*dns.A).A.String(); got != "127.0.0.1" {
		t.Fatalf("exact address = %q", got)
	}

	wildcard := snapshot.Lookup("foo.web.project.internal.", dns.TypeA)
	if !wildcard.Managed || !wildcard.Found || len(wildcard.Answers) != 1 {
		t.Fatalf("wildcard lookup = %+v", wildcard)
	}
	if got := wildcard.Answers[0].Header().Name; got != "foo.web.project.internal." {
		t.Fatalf("wildcard owner = %q", got)
	}

	nodata := snapshot.Lookup("api.project.internal.", dns.TypeAAAA)
	if !nodata.Managed || !nodata.Found || len(nodata.Answers) != 0 {
		t.Fatalf("NODATA lookup = %+v", nodata)
	}

	missing := snapshot.Lookup("missing.project.internal.", dns.TypeA)
	if !missing.Managed || missing.Found {
		t.Fatalf("missing lookup = %+v", missing)
	}

	public := snapshot.Lookup("example.com.", dns.TypeA)
	if public.Managed {
		t.Fatalf("public lookup unexpectedly managed: %+v", public)
	}
}

func TestWildcardStopsAtClosestEncloser(t *testing.T) {
	t.Parallel()
	snapshot := mustSnapshot(t, []config.Zone{{
		Name: "project.internal.",
		TTL:  30,
		Records: []config.Record{
			{Name: "*.project.internal.", Type: dns.TypeA, TTL: 30, Values: []string{"192.0.2.1"}},
			{Name: "api.project.internal.", Type: dns.TypeTXT, TTL: 30, Values: []string{"exists"}},
		},
	}})

	result := snapshot.Lookup("missing.api.project.internal.", dns.TypeA)
	if result.Found {
		t.Fatalf("wildcard crossed closest encloser: %+v", result)
	}
}

func TestSnapshotUsesLongestZone(t *testing.T) {
	t.Parallel()
	snapshot := mustSnapshot(t, []config.Zone{
		{Name: "internal.", TTL: 30, Records: []config.Record{{Name: "*.internal.", Type: dns.TypeA, TTL: 30, Values: []string{"192.0.2.1"}}}},
		{Name: "project.internal.", TTL: 30, Records: []config.Record{{Name: "api.project.internal.", Type: dns.TypeA, TTL: 30, Values: []string{"192.0.2.2"}}}},
	})
	result := snapshot.Lookup("api.project.internal.", dns.TypeA)
	if got := result.Answers[0].(*dns.A).A.String(); got != "192.0.2.2" {
		t.Fatalf("answer = %s, want child zone answer", got)
	}
}

func mustSnapshot(t *testing.T, zones []config.Zone) *Snapshot {
	t.Helper()
	snapshot, err := New(zones)
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}
	return snapshot
}
