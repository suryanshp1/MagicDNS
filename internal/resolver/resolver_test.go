package resolver

import (
	"context"
	"net"
	"sync/atomic"
	"testing"
	"time"

	"github.com/devmesh-dns/devmesh/internal/config"
	"github.com/devmesh-dns/devmesh/internal/registry"
	"github.com/miekg/dns"
)

func TestResolveManagedAnswersAndNegativeResponses(t *testing.T) {
	t.Parallel()
	engine := newTestResolver(t, nil)

	answer := engine.Resolve(context.Background(), question("api.project.internal.", dns.TypeA, 10))
	if !answer.Authoritative || answer.Rcode != dns.RcodeSuccess || len(answer.Answer) != 1 {
		t.Fatalf("answer = %+v", answer)
	}

	nodata := engine.Resolve(context.Background(), question("api.project.internal.", dns.TypeAAAA, 11))
	if !nodata.Authoritative || nodata.Rcode != dns.RcodeSuccess || len(nodata.Answer) != 0 || len(nodata.Ns) != 1 {
		t.Fatalf("NODATA response = %+v", nodata)
	}

	nxdomain := engine.Resolve(context.Background(), question("missing.project.internal.", dns.TypeA, 12))
	if !nxdomain.Authoritative || nxdomain.Rcode != dns.RcodeNameError || len(nxdomain.Ns) != 1 {
		t.Fatalf("NXDOMAIN response = %+v", nxdomain)
	}
}

func TestResolveRejectsInvalidQuestionShape(t *testing.T) {
	t.Parallel()
	engine := newTestResolver(t, nil)
	request := new(dns.Msg)
	request.Id = 42
	response := engine.Resolve(context.Background(), request)
	if response.Rcode != dns.RcodeFormatError || response.Id != request.Id {
		t.Fatalf("response = %+v", response)
	}
}

func TestForwardAndCache(t *testing.T) {
	t.Parallel()
	var requests atomic.Int32
	engine := newTestResolver(t, []string{"192.0.2.53:53"})
	engine.exchange = func(_ context.Context, network string, request *dns.Msg, upstream string, _ time.Duration) (*dns.Msg, error) {
		requests.Add(1)
		if network != "udp" {
			t.Fatalf("network = %q, want udp", network)
		}
		if upstream != "192.0.2.53:53" {
			t.Fatalf("upstream = %q", upstream)
		}
		response := new(dns.Msg)
		response.SetReply(request)
		response.Answer = []dns.RR{&dns.A{
			Hdr: dns.RR_Header{Name: request.Question[0].Name, Rrtype: dns.TypeA, Class: dns.ClassINET, Ttl: 60},
			A:   net.ParseIP("192.0.2.44").To4(),
		}}
		return response, nil
	}
	first := engine.Resolve(context.Background(), question("example.com.", dns.TypeA, 100))
	second := engine.Resolve(context.Background(), question("example.com.", dns.TypeA, 101))
	if first.Rcode != dns.RcodeSuccess || second.Rcode != dns.RcodeSuccess {
		t.Fatalf("forward response codes = %d, %d", first.Rcode, second.Rcode)
	}
	if second.Id != 101 {
		t.Fatalf("cached response ID = %d, want 101", second.Id)
	}
	if got := requests.Load(); got != 1 {
		t.Fatalf("upstream requests = %d, want 1", got)
	}
}

func TestForwardRetriesTruncatedResponseOverTCP(t *testing.T) {
	t.Parallel()
	engine := newTestResolver(t, []string{"192.0.2.53:53"})
	var networks []string
	engine.exchange = func(_ context.Context, network string, request *dns.Msg, _ string, _ time.Duration) (*dns.Msg, error) {
		networks = append(networks, network)
		response := new(dns.Msg)
		response.SetReply(request)
		if network == "udp" {
			response.Truncated = true
		} else {
			response.Answer = []dns.RR{&dns.A{Hdr: dns.RR_Header{Name: request.Question[0].Name, Rrtype: dns.TypeA, Class: dns.ClassINET, Ttl: 60}}}
		}
		return response, nil
	}
	response := engine.Resolve(context.Background(), question("example.net.", dns.TypeA, 200))
	if response.Rcode != dns.RcodeSuccess || len(response.Answer) != 1 {
		t.Fatalf("response = %+v", response)
	}
	if len(networks) != 2 || networks[0] != "udp" || networks[1] != "tcp" {
		t.Fatalf("networks = %v, want [udp tcp]", networks)
	}
}

func TestCacheAgesTTL(t *testing.T) {
	t.Parallel()
	now := time.Unix(100, 0)
	cache := newMessageCache(time.Minute, 10)
	cache.now = func() time.Time { return now }
	message := new(dns.Msg)
	message.Answer = []dns.RR{&dns.A{Hdr: dns.RR_Header{Name: "example.com.", Rrtype: dns.TypeA, Class: dns.ClassINET, Ttl: 30}}}
	cache.put("key", message)
	now = now.Add(7 * time.Second)
	result, ok := cache.get("key", 99)
	if !ok {
		t.Fatal("cache miss")
	}
	if got := result.Answer[0].Header().Ttl; got != 23 {
		t.Fatalf("aged TTL = %d, want 23", got)
	}
}

func TestCacheIsSizeBounded(t *testing.T) {
	t.Parallel()
	cache := newMessageCache(time.Minute, 2)
	now := time.Unix(100, 0)
	cache.now = func() time.Time {
		now = now.Add(time.Second)
		return now
	}
	for _, key := range []string{"one", "two", "three"} {
		message := new(dns.Msg)
		message.Answer = []dns.RR{&dns.A{Hdr: dns.RR_Header{Name: "example.com.", Rrtype: dns.TypeA, Class: dns.ClassINET, Ttl: 30}}}
		cache.put(key, message)
	}
	if got := len(cache.entries); got != 2 {
		t.Fatalf("cache entries = %d, want 2", got)
	}
	if _, ok := cache.get("one", 1); ok {
		t.Fatal("oldest cache entry was not evicted")
	}
}

func newTestResolver(t *testing.T, upstreams []string) *Resolver {
	t.Helper()
	snapshot, err := registry.New([]config.Zone{{
		Name: "project.internal.",
		TTL:  30,
		Records: []config.Record{{
			Name: "api.project.internal.", Type: dns.TypeA, TTL: 30, Values: []string{"127.0.0.1"},
		}},
	}})
	if err != nil {
		t.Fatalf("registry.New() error = %v", err)
	}
	return New(snapshot, upstreams, time.Second, time.Minute, 100)
}

func question(name string, qtype uint16, id uint16) *dns.Msg {
	request := new(dns.Msg)
	request.SetQuestion(name, qtype)
	request.Id = id
	return request
}
