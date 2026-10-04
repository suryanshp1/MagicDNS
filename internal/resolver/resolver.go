// Package resolver answers managed names and forwards all other questions.
package resolver

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/devmesh-dns/devmesh/internal/registry"
	"github.com/miekg/dns"
)

type Resolver struct {
	snapshot  *registry.Snapshot
	upstreams []string
	timeout   time.Duration
	cache     *messageCache
	exchange  exchangeFunc
}

type exchangeFunc func(context.Context, string, *dns.Msg, string, time.Duration) (*dns.Msg, error)

func New(snapshot *registry.Snapshot, upstreams []string, timeout, cacheTTL time.Duration, cacheSize int) *Resolver {
	return &Resolver{
		snapshot:  snapshot,
		upstreams: append([]string(nil), upstreams...),
		timeout:   timeout,
		cache:     newMessageCache(cacheTTL, cacheSize),
		exchange:  exchangeDNS,
	}
}

func (r *Resolver) Resolve(ctx context.Context, request *dns.Msg) *dns.Msg {
	if request == nil {
		return new(dns.Msg)
	}
	if len(request.Question) != 1 {
		response := new(dns.Msg)
		response.SetRcode(request, dns.RcodeFormatError)
		return response
	}
	question := request.Question[0]
	if question.Qclass != dns.ClassINET {
		response := new(dns.Msg)
		response.SetRcode(request, dns.RcodeNotImplemented)
		return response
	}

	lookup := r.snapshot.Lookup(question.Name, question.Qtype)
	if lookup.Managed {
		response := new(dns.Msg)
		response.SetReply(request)
		response.Authoritative = true
		if !lookup.Found {
			response.Rcode = dns.RcodeNameError
			response.Ns = []dns.RR{lookup.SOA}
			return response
		}
		response.Answer = lookup.Answers
		if len(response.Answer) == 0 {
			response.Ns = []dns.RR{lookup.SOA}
		}
		return response
	}

	response, err := r.forward(ctx, request)
	if err == nil {
		return response
	}
	failure := new(dns.Msg)
	failure.SetRcode(request, dns.RcodeServerFailure)
	return failure
}

func (r *Resolver) forward(ctx context.Context, request *dns.Msg) (*dns.Msg, error) {
	if len(r.upstreams) == 0 {
		return nil, errors.New("no upstream resolvers configured")
	}
	question := request.Question[0]
	key := fmt.Sprintf("%s/%d/%d", strings.ToLower(dns.Fqdn(question.Name)), question.Qtype, question.Qclass)
	if cached, ok := r.cache.get(key, request.Id); ok {
		return cached, nil
	}

	var failures []error
	for _, upstream := range r.upstreams {
		response, err := r.exchange(ctx, "udp", request, upstream, r.timeout)
		if err != nil {
			failures = append(failures, fmt.Errorf("%s: %w", upstream, err))
			continue
		}
		if response.Truncated {
			response, err = r.exchange(ctx, "tcp", request, upstream, r.timeout)
			if err != nil {
				failures = append(failures, fmt.Errorf("%s TCP retry: %w", upstream, err))
				continue
			}
		}
		r.cache.put(key, response)
		return response, nil
	}
	return nil, errors.Join(failures...)
}

func exchangeDNS(ctx context.Context, network string, request *dns.Msg, upstream string, timeout time.Duration) (*dns.Msg, error) {
	queryCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	response, _, err := (&dns.Client{Net: network, Timeout: timeout}).ExchangeContext(queryCtx, request, upstream)
	return response, err
}
