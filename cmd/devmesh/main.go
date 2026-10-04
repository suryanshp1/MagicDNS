package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"log/slog"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"github.com/devmesh-dns/devmesh/internal/config"
	"github.com/devmesh-dns/devmesh/internal/dnsserver"
	"github.com/devmesh-dns/devmesh/internal/registry"
	"github.com/devmesh-dns/devmesh/internal/resolver"
	"github.com/miekg/dns"
)

var version = "0.0.1-dev"

func main() {
	if err := run(os.Args[1:]); err != nil {
		slog.Error("devmesh failed", "error", err)
		os.Exit(1)
	}
}

func run(args []string) error {
	if len(args) == 0 {
		return usageError()
	}
	switch args[0] {
	case "serve":
		return serve(args[1:])
	case "config":
		return configCommand(args[1:])
	case "query":
		return query(args[1:])
	case "version", "--version", "-version":
		fmt.Println(version)
		return nil
	case "help", "--help", "-h":
		printUsage()
		return nil
	default:
		return fmt.Errorf("unknown command %q\n\n%s", args[0], usageText())
	}
}

func serve(args []string) error {
	set := flag.NewFlagSet("serve", flag.ContinueOnError)
	path := set.String("config", "devmesh.yaml", "configuration file")
	if err := set.Parse(args); err != nil {
		return err
	}
	runtime, err := loadRuntime(*path)
	if err != nil {
		return err
	}
	snapshot, err := registry.New(runtime.Zones)
	if err != nil {
		return err
	}
	engine := resolver.New(snapshot, runtime.Upstreams, runtime.Timeout, runtime.CacheTTL, runtime.CacheSize)
	server := dnsserver.New(runtime.Listen, engine)

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	slog.Info("DevMesh DNS starting", "listeners", runtime.Listen, "zones", len(runtime.Zones))
	if err := server.Run(ctx); err != nil {
		return err
	}
	slog.Info("DevMesh DNS stopped")
	return nil
}

func configCommand(args []string) error {
	if len(args) == 0 || args[0] != "validate" {
		return errors.New("usage: devmesh config validate [--config path]")
	}
	set := flag.NewFlagSet("config validate", flag.ContinueOnError)
	path := set.String("config", "devmesh.yaml", "configuration file")
	if err := set.Parse(args[1:]); err != nil {
		return err
	}
	runtime, err := loadRuntime(*path)
	if err != nil {
		return err
	}
	fmt.Printf("valid: %d zone(s), %d listener(s), %d upstream(s)\n", len(runtime.Zones), len(runtime.Listen), len(runtime.Upstreams))
	return nil
}

func query(args []string) error {
	set := flag.NewFlagSet("query", flag.ContinueOnError)
	server := set.String("server", "127.0.0.1:5354", "DNS server address")
	typeName := set.String("type", "A", "record type")
	timeout := set.Duration("timeout", 2*time.Second, "query timeout")
	if err := set.Parse(args); err != nil {
		return err
	}
	if set.NArg() != 1 {
		return errors.New("usage: devmesh query [flags] <name>")
	}
	qtype, ok := dns.StringToType[strings.ToUpper(*typeName)]
	if !ok {
		return fmt.Errorf("unknown DNS type %q", *typeName)
	}
	request := new(dns.Msg)
	request.SetQuestion(dns.Fqdn(set.Arg(0)), qtype)
	response, elapsed, err := (&dns.Client{Timeout: *timeout}).Exchange(request, *server)
	if err != nil {
		return err
	}
	fmt.Printf("status=%s authoritative=%t elapsed=%s\n", dns.RcodeToString[response.Rcode], response.Authoritative, elapsed.Round(time.Microsecond))
	for _, answer := range response.Answer {
		fmt.Println(answer)
	}
	return nil
}

func loadRuntime(path string) (*config.Runtime, error) {
	cfg, err := config.LoadFile(path)
	if err != nil {
		return nil, err
	}
	return cfg.Build()
}

func usageError() error {
	return errors.New(usageText())
}

func printUsage() {
	fmt.Print(usageText())
}

func usageText() string {
	return `DevMesh DNS

Usage:
  devmesh serve [--config path]
  devmesh config validate [--config path]
  devmesh query [--server host:port] [--type A] <name>
  devmesh version
`
}
