package adminaccess

import "testing"

func TestUsageExportRoutesRequireUsageRead(t *testing.T) {
	for _, r := range []struct{ method, path string }{{"POST", "/usage/exports"}, {"GET", "/usage/exports"}, {"GET", "/usage/exports/:id"}, {"POST", "/usage/exports/:id/cancel"}, {"POST", "/usage/exports/:id/download-ticket"}, {"DELETE", "/usage/exports/:id"}} {
		if RouteAllowed(Policy{}, r.method, "/api/v1/admin"+r.path) {
			t.Fatal("ungranted export route allowed")
		}
		if !RouteAllowed(Policy{GlobalPermissions: []string{"usage.read"}}, r.method, "/api/v1/admin"+r.path) {
			t.Fatalf("missing export permission mapping: %+v", r)
		}
	}
}
