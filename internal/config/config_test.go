package config

import (
	"strings"
	"testing"

	"github.com/miekg/dns"
)

func TestDecodeAndBuild(t *testing.T) {
	t.Parallel()
	cfg, err := Decode(strings.NewReader(`
version: 1
server:
  listen:
    dns: ["127.0.0.1:5354"]
  upstreams: ["192.0.2.53"]
zones:
  - name: project.internal
    default_ttl: 45s
    records:
      - name: api
        type: A
        values: ["127.0.0.1"]
`))
	if err != nil {
		t.Fatalf("Decode() error = %v", err)
	}
	runtime, err := cfg.Build()
	if err != nil {
		t.Fatalf("Build() error = %v", err)
	}
	if got, want := runtime.Upstreams[0], "192.0.2.53:53"; got != want {
		t.Fatalf("upstream = %q, want %q", got, want)
	}
	if got, want := runtime.Zones[0].Records[0].Type, uint16(dns.TypeA); got != want {
		t.Fatalf("record type = %d, want %d", got, want)
	}
	if got, want := runtime.Zones[0].TTL, uint32(45); got != want {
		t.Fatalf("TTL = %d, want %d", got, want)
	}
}

func TestDecodeRejectsUnknownField(t *testing.T) {
	t.Parallel()
	_, err := Decode(strings.NewReader("version: 1\nunknown: true\n"))
	if err == nil || !strings.Contains(err.Error(), "field unknown not found") {
		t.Fatalf("Decode() error = %v, want unknown-field error", err)
	}
}

func TestDecodeRejectsMultipleDocuments(t *testing.T) {
	t.Parallel()
	_, err := Decode(strings.NewReader("version: 1\n---\nversion: 1\n"))
	if err == nil || !strings.Contains(err.Error(), "exactly one YAML document") {
		t.Fatalf("Decode() error = %v, want multiple-document error", err)
	}
}

func TestBuildReportsUnsafeZoneAndRecordErrorsTogether(t *testing.T) {
	t.Parallel()
	cfg := &Config{
		Version: 1,
		Server: ServerConfig{
			Upstreams: []string{"192.0.2.53"},
		},
		Zones: []ZoneConfig{{
			Name: "dev",
			Records: []RecordConfig{
				{Name: "api", Type: "A", Values: []string{"not-an-ip"}},
				{Name: "outside.example.", Type: "A", Values: []string{"127.0.0.1"}},
			},
		}},
	}
	_, err := cfg.Build()
	if err == nil {
		t.Fatal("Build() error = nil, want validation failure")
	}
	for _, expected := range []string{"is unsafe", "not-an-ip", "outside zone"} {
		if !strings.Contains(err.Error(), expected) {
			t.Errorf("Build() error = %q, want %q", err, expected)
		}
	}
}

func TestBuildRejectsCNAMECoexistence(t *testing.T) {
	t.Parallel()
	cfg := &Config{
		Version: 1,
		Server:  ServerConfig{Upstreams: []string{"192.0.2.53"}},
		Zones: []ZoneConfig{{
			Name: "project.internal",
			Records: []RecordConfig{
				{Name: "api", Type: "CNAME", Values: []string{"target.project.internal."}},
				{Name: "api", Type: "A", Values: []string{"127.0.0.1"}},
			},
		}},
	}
	_, err := cfg.Build()
	if err == nil || !strings.Contains(err.Error(), "CNAME and other record types") {
		t.Fatalf("Build() error = %v, want CNAME coexistence error", err)
	}
}

func TestBuildRejectsInvalidWildcards(t *testing.T) {
	t.Parallel()
	for _, name := range []string{"api-*", "foo.*", "*.*.api"} {
		name := name
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			cfg := &Config{
				Version: 1,
				Server:  ServerConfig{Upstreams: []string{"192.0.2.53"}},
				Zones: []ZoneConfig{{
					Name:    "project.internal",
					Records: []RecordConfig{{Name: name, Type: "A", Values: []string{"127.0.0.1"}}},
				}},
			}
			_, err := cfg.Build()
			if err == nil || !strings.Contains(err.Error(), "wildcard") {
				t.Fatalf("Build() error = %v, want wildcard validation error", err)
			}
		})
	}
}

func FuzzDecodeAndBuild(f *testing.F) {
	f.Add([]byte("version: 1\nserver:\n  upstreams: [192.0.2.53]\nzones:\n  - name: project.internal\n    records: []\n"))
	f.Add([]byte("not: [valid"))
	f.Fuzz(func(t *testing.T, data []byte) {
		cfg, err := Decode(strings.NewReader(string(data)))
		if err == nil {
			_, _ = cfg.Build()
		}
	})
}
