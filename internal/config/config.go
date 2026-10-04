// Package config loads and validates DevMesh configuration.
package config

import (
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/miekg/dns"
	"gopkg.in/yaml.v3"
)

const CurrentVersion = 1

type Config struct {
	Version          int          `yaml:"version"`
	AllowUnsafeZones bool         `yaml:"allow_unsafe_zones,omitempty"`
	Server           ServerConfig `yaml:"server"`
	Zones            []ZoneConfig `yaml:"zones"`
}

type ServerConfig struct {
	Listen    ListenConfig `yaml:"listen"`
	Upstreams []string     `yaml:"upstreams"`
	Timeout   string       `yaml:"timeout,omitempty"`
	CacheTTL  string       `yaml:"cache_ttl,omitempty"`
	CacheSize int          `yaml:"cache_size,omitempty"`
}

type ListenConfig struct {
	DNS []string `yaml:"dns"`
}

type ZoneConfig struct {
	Name       string         `yaml:"name"`
	DefaultTTL string         `yaml:"default_ttl,omitempty"`
	Records    []RecordConfig `yaml:"records"`
}

type RecordConfig struct {
	Name   string   `yaml:"name"`
	Type   string   `yaml:"type"`
	TTL    string   `yaml:"ttl,omitempty"`
	Values []string `yaml:"values"`
}

type Runtime struct {
	Listen    []string
	Upstreams []string
	Timeout   time.Duration
	CacheTTL  time.Duration
	CacheSize int
	Zones     []Zone
}

type Zone struct {
	Name    string
	TTL     uint32
	Records []Record
}

type Record struct {
	Name   string
	Type   uint16
	TTL    uint32
	Values []string
}

func LoadFile(path string) (*Config, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, fmt.Errorf("open config: %w", err)
	}
	defer f.Close()
	return Decode(f)
}

func Decode(r io.Reader) (*Config, error) {
	dec := yaml.NewDecoder(r)
	dec.KnownFields(true)

	var cfg Config
	if err := dec.Decode(&cfg); err != nil {
		return nil, fmt.Errorf("decode config: %w", err)
	}
	if err := ensureSingleDocument(dec); err != nil {
		return nil, err
	}
	return &cfg, nil
}

func ensureSingleDocument(dec *yaml.Decoder) error {
	var extra any
	err := dec.Decode(&extra)
	if errors.Is(err, io.EOF) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("decode trailing config: %w", err)
	}
	return errors.New("configuration must contain exactly one YAML document")
}

func (c *Config) Build() (*Runtime, error) {
	var problems []string
	if c.Version != CurrentVersion {
		problems = append(problems, fmt.Sprintf("version must be %d", CurrentVersion))
	}

	listen := append([]string(nil), c.Server.Listen.DNS...)
	if len(listen) == 0 {
		listen = []string{"127.0.0.1:5354"}
	}
	for _, addr := range listen {
		if _, _, err := net.SplitHostPort(addr); err != nil {
			problems = append(problems, fmt.Sprintf("server.listen.dns %q: %v", addr, err))
		}
	}

	timeout, err := durationOrDefault(c.Server.Timeout, 2*time.Second)
	if err != nil || timeout <= 0 {
		problems = append(problems, "server.timeout must be a positive duration")
	}
	cacheTTL, err := durationOrDefault(c.Server.CacheTTL, 30*time.Second)
	if err != nil || cacheTTL < 0 {
		problems = append(problems, "server.cache_ttl must be a non-negative duration")
	}
	cacheSize := c.Server.CacheSize
	if cacheSize == 0 {
		cacheSize = 4096
	} else if cacheSize < 0 {
		problems = append(problems, "server.cache_size must be a positive integer")
	}

	upstreams, err := expandUpstreams(c.Server.Upstreams)
	if err != nil {
		problems = append(problems, err.Error())
	}

	seenZones := make(map[string]struct{})
	zones := make([]Zone, 0, len(c.Zones))
	for i, rawZone := range c.Zones {
		zone, errs := buildZone(rawZone, c.AllowUnsafeZones)
		for _, problem := range errs {
			problems = append(problems, fmt.Sprintf("zones[%d]: %s", i, problem))
		}
		if len(errs) != 0 {
			continue
		}
		if _, exists := seenZones[zone.Name]; exists {
			problems = append(problems, fmt.Sprintf("zones[%d]: duplicate zone %q", i, zone.Name))
			continue
		}
		seenZones[zone.Name] = struct{}{}
		zones = append(zones, zone)
	}
	if len(zones) == 0 {
		problems = append(problems, "at least one valid zone is required")
	}

	if len(problems) != 0 {
		sort.Strings(problems)
		return nil, fmt.Errorf("invalid configuration:\n- %s", strings.Join(problems, "\n- "))
	}
	return &Runtime{
		Listen:    listen,
		Upstreams: upstreams,
		Timeout:   timeout,
		CacheTTL:  cacheTTL,
		CacheSize: cacheSize,
		Zones:     zones,
	}, nil
}

func buildZone(raw ZoneConfig, allowUnsafe bool) (Zone, []string) {
	var problems []string
	name, err := canonicalName(raw.Name)
	if err != nil {
		return Zone{}, []string{"name: " + err.Error()}
	}
	label := strings.TrimSuffix(name, ".")
	if !allowUnsafe && isUnsafeZone(label) {
		problems = append(problems, fmt.Sprintf("zone %q is unsafe; choose .internal/home.arpa or set allow_unsafe_zones", label))
	}

	ttlDuration, err := durationOrDefault(raw.DefaultTTL, 30*time.Second)
	if err != nil {
		problems = append(problems, "default_ttl must be a duration")
	}
	ttl, err := durationToTTL(ttlDuration)
	if err != nil {
		problems = append(problems, "default_ttl: "+err.Error())
	}

	records := make([]Record, 0, len(raw.Records))
	ownerTypes := make(map[string]map[uint16]struct{})
	for i, rawRecord := range raw.Records {
		record, errs := buildRecord(name, ttl, rawRecord)
		for _, problem := range errs {
			problems = append(problems, fmt.Sprintf("records[%d]: %s", i, problem))
		}
		if len(errs) != 0 {
			continue
		}
		records = append(records, record)
		if ownerTypes[record.Name] == nil {
			ownerTypes[record.Name] = make(map[uint16]struct{})
		}
		ownerTypes[record.Name][record.Type] = struct{}{}
	}
	for owner, types := range ownerTypes {
		if _, hasCNAME := types[dns.TypeCNAME]; hasCNAME && len(types) > 1 {
			problems = append(problems, fmt.Sprintf("owner %q has CNAME and other record types", owner))
		}
	}
	return Zone{Name: name, TTL: ttl, Records: records}, problems
}

func buildRecord(zone string, defaultTTL uint32, raw RecordConfig) (Record, []string) {
	var problems []string
	owner, err := recordName(zone, raw.Name)
	if err != nil {
		problems = append(problems, "name: "+err.Error())
	}

	typeName := strings.ToUpper(strings.TrimSpace(raw.Type))
	rrType, ok := map[string]uint16{
		"A": dns.TypeA, "AAAA": dns.TypeAAAA, "CNAME": dns.TypeCNAME,
		"TXT": dns.TypeTXT, "SRV": dns.TypeSRV,
	}[typeName]
	if !ok {
		problems = append(problems, "type must be one of A, AAAA, CNAME, TXT, or SRV")
	}
	if len(raw.Values) == 0 {
		problems = append(problems, "values must not be empty")
	}

	ttl := defaultTTL
	if raw.TTL != "" {
		d, err := time.ParseDuration(raw.TTL)
		if err != nil {
			problems = append(problems, "ttl must be a duration")
		} else if ttl, err = durationToTTL(d); err != nil {
			problems = append(problems, "ttl: "+err.Error())
		}
	}

	if len(problems) == 0 {
		for _, value := range raw.Values {
			if _, err := dns.NewRR(fmt.Sprintf("%s %d IN %s %s", owner, ttl, typeName, value)); err != nil {
				problems = append(problems, fmt.Sprintf("invalid %s value %q: %v", typeName, value, err))
			}
		}
	}
	return Record{Name: owner, Type: rrType, TTL: ttl, Values: append([]string(nil), raw.Values...)}, problems
}

func canonicalName(name string) (string, error) {
	name = strings.ToLower(strings.TrimSpace(name))
	name = dns.Fqdn(name)
	if name == "." {
		return "", errors.New("must not be empty or the DNS root")
	}
	if _, ok := dns.IsDomainName(name); !ok {
		return "", fmt.Errorf("%q is not a valid DNS name", name)
	}
	return name, nil
}

func recordName(zone, name string) (string, error) {
	name = strings.ToLower(strings.TrimSpace(name))
	if name == "" || name == "@" {
		return zone, nil
	}
	if strings.Contains(name, "*") && (!strings.HasPrefix(name, "*.") || strings.Count(name, "*") != 1) {
		return "", errors.New("exactly one wildcard is allowed as the complete left-most label")
	}
	var owner string
	if dns.IsFqdn(name) {
		owner = name
	} else {
		owner = name + "." + zone
	}
	owner, err := canonicalName(owner)
	if err != nil {
		return "", err
	}
	if !dns.IsSubDomain(zone, owner) {
		return "", fmt.Errorf("%q is outside zone %q", owner, zone)
	}
	return owner, nil
}

func isUnsafeZone(name string) bool {
	name = strings.TrimSuffix(strings.ToLower(name), ".")
	return name == "dev" || name == "home" || name == "local" || name == "localhost" || strings.HasSuffix(name, ".local") || strings.HasSuffix(name, ".localhost")
}

func durationOrDefault(value string, fallback time.Duration) (time.Duration, error) {
	if value == "" {
		return fallback, nil
	}
	return time.ParseDuration(value)
}

func durationToTTL(d time.Duration) (uint32, error) {
	if d < time.Second {
		return 0, errors.New("must be at least one second")
	}
	seconds := d / time.Second
	if seconds > time.Duration(^uint32(0)) {
		return 0, errors.New("is too large")
	}
	return uint32(seconds), nil
}

func expandUpstreams(values []string) ([]string, error) {
	if len(values) == 0 {
		values = []string{"system"}
	}
	var result []string
	for _, value := range values {
		value = strings.TrimSpace(value)
		if value == "system" {
			cfg, err := dns.ClientConfigFromFile("/etc/resolv.conf")
			if err != nil {
				return nil, fmt.Errorf("read system DNS: %w", err)
			}
			for _, server := range cfg.Servers {
				result = append(result, net.JoinHostPort(server, cfg.Port))
			}
			continue
		}
		if _, _, err := net.SplitHostPort(value); err != nil {
			if net.ParseIP(value) == nil {
				return nil, fmt.Errorf("upstream %q must be an IP address with optional port", value)
			}
			value = net.JoinHostPort(value, "53")
		}
		result = append(result, value)
	}
	if len(result) == 0 {
		return nil, errors.New("at least one upstream DNS server is required")
	}
	return deduplicate(result), nil
}

func deduplicate(values []string) []string {
	seen := make(map[string]struct{}, len(values))
	result := make([]string, 0, len(values))
	for _, value := range values {
		if _, ok := seen[value]; ok {
			continue
		}
		seen[value] = struct{}{}
		result = append(result, value)
	}
	return result
}

func FormatTTL(ttl uint32) string {
	return strconv.FormatUint(uint64(ttl), 10)
}
