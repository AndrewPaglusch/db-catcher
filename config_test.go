package main

import (
	"strings"
	"testing"
)

const validConfig = `
s3:
  region: us-west-004
  bucket: test-bucket
notify_webhook: https://example.com/global
backups:
  - id: 3f6c2a9e-1111-4000-8000-000000000001
    name: orders
    type: postgres
    keep: 3
    alert_after_minutes: 60
  - id: 9b1d2c3e-2222-4000-9000-000000000002
    name: billing
    type: mysql
    keep: 2
    alert_after_minutes: 90
    notify_webhook: https://example.com/billing
    alert_on_invalid: true
`

func TestParseConfigAppliesDefaults(t *testing.T) {
	cfg, err := parseConfig([]byte(validConfig))
	if err != nil {
		t.Fatal(err)
	}

	if cfg.Listen != ":8080" {
		t.Errorf("listen = %q, want :8080", cfg.Listen)
	}
	orders, billing := cfg.Backups[0], cfg.Backups[1]
	if orders.NotifyWebhook != "https://example.com/global" {
		t.Errorf("orders webhook = %q, want the global one", orders.NotifyWebhook)
	}
	if billing.NotifyWebhook != "https://example.com/billing" {
		t.Errorf("billing webhook = %q, want its override", billing.NotifyWebhook)
	}
	if orders.AlertOnInvalid || !billing.AlertOnInvalid {
		t.Errorf("alert_on_invalid = %v/%v, want false/true", orders.AlertOnInvalid, billing.AlertOnInvalid)
	}
	if cfg.backupFor("3f6c2a9e-1111-4000-8000-000000000001") != orders {
		t.Error("backupFor did not find orders by its id")
	}
	if cfg.backupFor("") != nil || cfg.backupFor("orders") != nil {
		t.Error("backupFor matched something that is not an id")
	}
}

func TestParseConfigRejects(t *testing.T) {
	tests := []struct {
		name    string
		change  func(string) string
		wantErr string
	}{
		{"duplicate id", replace("9b1d2c3e-2222-4000-9000-000000000002", "3f6c2a9e-1111-4000-8000-000000000001"), "id is used by another backup"},
		{"duplicate name", replace("name: billing", "name: orders"), "name is used by another backup"},
		{"id that is not a uuid", replace("3f6c2a9e-1111-4000-8000-000000000001", "orders-secret"), "must be a lowercase random (v4) UUID"},
		{"non-v4 uuid", replace("3f6c2a9e-1111-4000", "3f6c2a9e-1111-1000"), "must be a lowercase random (v4) UUID"},
		{"name with a slash", replace("name: orders", "name: prod/orders"), "name may only contain"},
		{"unknown type", replace("type: mysql", "type: oracle"), `unknown type "oracle"`},
		{"keep zero", replace("keep: 3", "keep: 0"), "keep must be at least 1"},
		{"negative alert minutes", replace("alert_after_minutes: 60", "alert_after_minutes: -5"), "alert_after_minutes must be at least 1"},
		{"no webhook anywhere", replace("notify_webhook: https://example.com/global", ""), "no notify_webhook set"},
		{"missing bucket", replace("bucket: test-bucket", ""), "s3.bucket is required"},
		{"misspelled key", replace("keep: 3", "kep: 3"), "field kep not found"},
		{"tls cert without key", func(s string) string { return "tls_cert: cert.pem\n" + s }, "tls_cert and tls_key must be set together"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := parseConfig([]byte(tt.change(validConfig)))
			if err == nil || !strings.Contains(err.Error(), tt.wantErr) {
				t.Fatalf("err = %v, want it to mention %q", err, tt.wantErr)
			}
		})
	}
}

func TestParseConfigReportsEveryProblem(t *testing.T) {
	broken := replace("keep: 3", "keep: 0")(replace("type: mysql", "type: oracle")(validConfig))
	_, err := parseConfig([]byte(broken))
	if err == nil || !strings.Contains(err.Error(), "keep") || !strings.Contains(err.Error(), "oracle") {
		t.Fatalf("err = %v, want both problems listed", err)
	}
}

func replace(old, new string) func(string) string {
	return func(s string) string { return strings.Replace(s, old, new, 1) }
}
