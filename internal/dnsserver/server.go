// Package dnsserver exposes the DevMesh resolver over UDP and TCP.
package dnsserver

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"time"

	"github.com/devmesh-dns/devmesh/internal/resolver"
	"github.com/miekg/dns"
)

type Server struct {
	servers []*dns.Server
}

func New(addresses []string, resolver *resolver.Resolver) *Server {
	handler := dns.HandlerFunc(func(writer dns.ResponseWriter, request *dns.Msg) {
		response := resolver.Resolve(context.Background(), request)
		_ = writer.WriteMsg(response)
	})
	servers := make([]*dns.Server, 0, len(addresses)*2)
	for _, address := range addresses {
		for _, network := range []string{"udp", "tcp"} {
			servers = append(servers, &dns.Server{
				Addr:         address,
				Net:          network,
				Handler:      handler,
				ReadTimeout:  3 * time.Second,
				WriteTimeout: 3 * time.Second,
			})
		}
	}
	return &Server{servers: servers}
}

func (s *Server) Run(ctx context.Context) error {
	if len(s.servers) == 0 {
		return errors.New("no DNS listeners configured")
	}
	errCh := make(chan error, len(s.servers))
	for _, server := range s.servers {
		server := server
		go func() {
			if err := server.ListenAndServe(); err != nil {
				errCh <- fmt.Errorf("listen %s/%s: %w", server.Addr, server.Net, err)
			}
		}()
	}

	select {
	case <-ctx.Done():
		return s.shutdown()
	case err := <-errCh:
		_ = s.shutdown()
		return err
	}
}

func (s *Server) shutdown() error {
	var wg sync.WaitGroup
	errCh := make(chan error, len(s.servers))
	for _, server := range s.servers {
		wg.Add(1)
		go func(server *dns.Server) {
			defer wg.Done()
			if err := server.Shutdown(); err != nil {
				errCh <- err
			}
		}(server)
	}
	wg.Wait()
	close(errCh)
	var errs []error
	for err := range errCh {
		errs = append(errs, err)
	}
	return errors.Join(errs...)
}
