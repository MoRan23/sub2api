package adminaccess

import "testing"

func TestSubscriptionTopupPermissionsKeepSecretsSeparate(t *testing.T) {
	for _, route := range []struct {
		permission string
		method     string
		path       string
	}{
		{"payment.topups.read", "GET", "/payment/topup-products"},
		{"payment.topups.read", "GET", "/payment/topup-skus/:id/cards"},
		{"payment.topups.write", "POST", "/payment/topup-products"},
		{"payment.topups.write", "PUT", "/payment/topup-products/:id"},
		{"payment.topups.write", "DELETE", "/payment/topup-products/:id"},
		{"payment.topups.write", "POST", "/payment/topup-products/:id/skus"},
		{"payment.topups.write", "PUT", "/payment/topup-skus/:id"},
		{"payment.topups.write", "DELETE", "/payment/topup-skus/:id"},
		{"payment.topups.write", "POST", "/payment/topup-skus/:id/cards/import"},
		{"payment.topups.write", "PUT", "/payment/topup-cards/:id"},
		{"payment.topups.secrets", "POST", "/payment/topup-cards/:id/reveal"},
		{"payment.topups.resend", "POST", "/payment/orders/:id/resend-cards"},
	} {
		t.Run(route.method+route.path, func(t *testing.T) {
			path := "/api/v1/admin" + route.path
			if !RouteAllowed(Policy{GlobalPermissions: []string{route.permission}}, route.method, path) {
				t.Fatal("explicit permission must allow route")
			}
			if RouteAllowed(Policy{}, route.method, path) || RouteAllowed(Policy{GlobalPermissions: []string{"payment.read", "payment.fulfill"}}, route.method, path) {
				t.Fatal("existing order access must not grant inventory or card access")
			}
		})
	}
	for _, permission := range []string{"payment.topups.read", "payment.topups.write", "payment.topups.resend"} {
		if RouteAllowed(Policy{GlobalPermissions: []string{permission}}, "POST", "/api/v1/admin/payment/topup-cards/:id/reveal") {
			t.Fatalf("%s must not reveal card secrets", permission)
		}
	}
}
