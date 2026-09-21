package proxy

import (
	"strings"
	"testing"

	"github.com/alpkeskin/rota/core/internal/models"
)

func TestValidateBodyStrategy(t *testing.T) {
	ipPattern := `^\d{1,3}(\.\d{1,3}){3}$`
	junk := "<!DOCTYPE html><html><head><title>Login</title></head></html>"

	tests := []struct {
		name      string
		strategy  BodyStrategy
		body      string
		wantErr   bool
		wantErrIn string
	}{
		{name: "status strategy skips body validation", strategy: BodyStrategy{Strategy: models.StrategyStatus}, body: junk, wantErr: false},
		{name: "empty strategy normalizes to ip", strategy: BodyStrategy{}, body: "93.184.216.34", wantErr: false},
		{name: "ip body passes ip strategy", strategy: BodyStrategy{Strategy: models.StrategyIP}, body: "93.184.216.34", wantErr: false},
		{name: "ip body with trailing newline passes ip strategy", strategy: BodyStrategy{Strategy: models.StrategyIP}, body: "93.184.216.34\n", wantErr: false},
		{name: "ipv6 body passes ip strategy", strategy: BodyStrategy{Strategy: models.StrategyIP}, body: "2001:db8::1", wantErr: false},
		{name: "html body fails ip strategy", strategy: BodyStrategy{Strategy: models.StrategyIP}, body: junk, wantErr: true, wantErrIn: "body does not contain an IP address"},
		{name: "version string is not an ip", strategy: BodyStrategy{Strategy: models.StrategyIP}, body: "v1.2.3.4.5", wantErr: true},
		{name: "ip embedded in text passes ip strategy", strategy: BodyStrategy{Strategy: models.StrategyIP}, body: "Your IP is 93.184.216.34", wantErr: false},
		{name: "contains strategy matches substring", strategy: BodyStrategy{Strategy: models.StrategyContains, Value: "ok"}, body: "all ok", wantErr: false},
		{name: "contains strategy fails on missing substring", strategy: BodyStrategy{Strategy: models.StrategyContains, Value: "ok"}, body: "all bad", wantErr: true, wantErrIn: "body does not contain"},
		{name: "regex strategy matches", strategy: BodyStrategy{Strategy: models.StrategyRegex, Value: ipPattern}, body: "93.184.216.34", wantErr: false},
		{name: "regex strategy fails on junk", strategy: BodyStrategy{Strategy: models.StrategyRegex, Value: ipPattern}, body: junk, wantErr: true, wantErrIn: "body does not match pattern"},
		{name: "invalid regex reported", strategy: BodyStrategy{Strategy: models.StrategyRegex, Value: "^("}, body: "x", wantErr: true, wantErrIn: "invalid body pattern"},
		{name: "unknown strategy falls back to ip", strategy: BodyStrategy{Strategy: "bogus"}, body: junk, wantErr: true, wantErrIn: "body does not contain an IP address"},
		{name: "unknown strategy falls back to ip (pass)", strategy: BodyStrategy{Strategy: "bogus"}, body: "93.184.216.34", wantErr: false},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			err := tc.strategy.Validate([]byte(tc.body))
			if tc.wantErr {
				if err == nil {
					t.Fatalf("expected error, got nil")
				}
				if tc.wantErrIn != "" && !strings.Contains(err.Error(), tc.wantErrIn) {
					t.Fatalf("error = %q, want it to contain %q", err, tc.wantErrIn)
				}
			} else if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
		})
	}
}

func TestBodyStrategyValid(t *testing.T) {
	for _, s := range []string{models.StrategyStatus, models.StrategyIP, models.StrategyContains, models.StrategyRegex} {
		if !(BodyStrategy{Strategy: s}).Valid() {
			t.Fatalf("strategy %q should be valid", s)
		}
	}
	if (BodyStrategy{Strategy: "bogus"}).Valid() {
		t.Fatal("bogus strategy should not be valid")
	}
}

func TestCompileBodyPatternCaches(t *testing.T) {
	first, err := CompileBodyPattern(`^a$`)
	if err != nil {
		t.Fatalf("first compile: %v", err)
	}
	second, err := CompileBodyPattern(`^a$`)
	if err != nil {
		t.Fatalf("second compile: %v", err)
	}
	if first != second {
		t.Fatal("expected the cached (identical) compiled regex")
	}
}

func TestReadHCBodyTruncates(t *testing.T) {
	long := strings.Repeat("x", hcBodyLimit+1024)
	body, err := ReadHCBody(strings.NewReader(long))
	if err != nil {
		t.Fatalf("ReadHCBody: %v", err)
	}
	if len(body) != hcBodyLimit {
		t.Fatalf("read %d bytes, want the %d-byte cap", len(body), hcBodyLimit)
	}
}
