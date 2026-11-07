package managementmcp

import (
	"slices"
	"testing"
)

func TestCatalogTotalQuotaGrantAndCreationOnlyMode(t *testing.T) {
	for _, spec := range ToolCatalog() {
		if spec.Name != "admin_create_group" && spec.Name != "admin_update_group" && spec.Name != "admin_assign_subscription" {
			continue
		}
		body := spec.InputSchema["properties"].(map[string]any)["body"].(map[string]any)
		fields := body["properties"].(map[string]any)
		switch spec.Name {
		case "admin_create_group":
			if _, ok := fields["total_quota_no_expiry"]; !ok {
				t.Fatal("missing creation-only mode")
			}
		case "admin_update_group":
			if _, ok := fields["total_quota_no_expiry"]; ok {
				t.Fatal("immutable mode is exposed for updates")
			}
		case "admin_assign_subscription":
			if required, _ := spec.InputSchema["required"].([]string); !slices.Contains(required, "idempotency_key") {
				t.Fatal("quota grants must expose an idempotent operation ID")
			}
			if _, ok := fields["quota_amount"]; !ok {
				t.Fatal("missing per-grant amount")
			}
			if required, _ := body["required"].([]string); slices.Contains(required, "validity_days") {
				t.Fatal("permanent grants must be allowed to omit days")
			}
		}
	}
}
