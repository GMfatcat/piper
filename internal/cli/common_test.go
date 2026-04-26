package cli

import (
	"errors"
	"path/filepath"
	"reflect"
	"testing"
)

func TestResolveDataDir_FlagWins(t *testing.T) {
	t.Setenv("PIPER_DATA_DIR", "/env/path")
	got, err := ResolveDataDir("/flag/path")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got != "/flag/path" {
		t.Errorf("want /flag/path, got %s", got)
	}
}

func TestResolveDataDir_EnvWhenNoFlag(t *testing.T) {
	t.Setenv("PIPER_DATA_DIR", "/env/path")
	got, err := ResolveDataDir("")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got != "/env/path" {
		t.Errorf("want /env/path, got %s", got)
	}
}

func TestResolveDataDir_HomeDefault(t *testing.T) {
	t.Setenv("PIPER_DATA_DIR", "")
	got, err := ResolveDataDir("")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !filepath.IsAbs(got) {
		t.Errorf("expected absolute path, got %s", got)
	}
	if filepath.Base(got) != ".piper" {
		t.Errorf("expected suffix .piper, got %s", got)
	}
}

func TestParsePortArgs(t *testing.T) {
	tests := []struct {
		name    string
		args    []string
		want    []int
		wantErr bool
	}{
		{"single port", []string{"8080"}, []int{8080}, false},
		{"multiple ports", []string{"8080", "9000", "9100"}, []int{8080, 9000, 9100}, false},
		{"single range", []string{"8080-8082"}, []int{8080, 8081, 8082}, false},
		{"mixed", []string{"8080", "9000-9002", "9100"}, []int{8080, 9000, 9001, 9002, 9100}, false},
		{"single-port range", []string{"8080-8080"}, []int{8080}, false},
		{"empty args", nil, nil, true},
		{"invalid port", []string{"abc"}, nil, true},
		{"port too high", []string{"99999"}, nil, true},
		{"port zero", []string{"0"}, nil, true},
		{"reverse range", []string{"9000-8000"}, nil, true},
		{"malformed range", []string{"8080-abc"}, nil, true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := ParsePortArgs(tt.args)
			if tt.wantErr {
				if err == nil {
					t.Errorf("expected error, got %v", got)
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if !reflect.DeepEqual(got, tt.want) {
				t.Errorf("want %v, got %v", tt.want, got)
			}
		})
	}
}

func TestInMemorySnapProvider_ReturnsFixed(t *testing.T) {
	p := InMemorySnapProvider{}
	snap, err := p.LatestSnapshot(t.Context())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if snap.UFWActive {
		t.Error("zero-value snap should have UFWActive=false")
	}
	// sanity: errors.Is on nil works
	if errors.Is(err, errors.New("x")) {
		t.Error("nil err should not match")
	}
}
