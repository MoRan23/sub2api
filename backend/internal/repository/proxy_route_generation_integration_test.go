//go:build integration

package repository

import (
	"time"

	"github.com/Wei-Shaw/sub2api/internal/service"
)

func (s *ProxyRepoSuite) TestRouteGenerationTracksEndpointAndCredentials() {
	proxy := s.mustCreateProxy(&service.Proxy{Name: "generation", Protocol: "http", Host: "127.0.0.1", Port: 8080,
		Status: service.StatusActive, RouteGeneration: 999})
	s.Require().Equal(int64(1), proxy.RouteGeneration, "create uses the database default")
	steps := []struct {
		name   string
		change func(*service.Proxy)
		bump   bool
	}{
		{name: "unchanged", change: func(*service.Proxy) {}},
		{name: "name", change: func(p *service.Proxy) { p.Name = "renamed" }},
		{name: "status", change: func(p *service.Proxy) { p.Status = service.StatusDisabled }},
		{name: "expiry", change: func(p *service.Proxy) { expiry := time.Now().Add(time.Hour); p.ExpiresAt = &expiry }},
		{name: "fallback", change: func(p *service.Proxy) { p.FallbackMode = service.FallbackModeDirect }},
		{name: "expiry warning", change: func(p *service.Proxy) { p.ExpiryWarnDays = 30 }},
		{name: "protocol", change: func(p *service.Proxy) { p.Protocol = "socks5" }, bump: true},
		{name: "host changes away", change: func(p *service.Proxy) { p.Host = "127.0.0.2" }, bump: true},
		{name: "host changes back", change: func(p *service.Proxy) { p.Host = "127.0.0.1" }, bump: true},
		{name: "port", change: func(p *service.Proxy) { p.Port++ }, bump: true},
		{name: "username", change: func(p *service.Proxy) { p.Username = "user" }, bump: true},
		{name: "password", change: func(p *service.Proxy) { p.Password = "pass" }, bump: true},
		{name: "clear credentials", change: func(p *service.Proxy) { p.Username, p.Password = "", "" }, bump: true},
	}
	generation := int64(1)
	for _, step := range steps {
		step.change(proxy)
		proxy.RouteGeneration = 999 // Stale or forged caller state must be ignored.
		s.Require().NoError(s.repo.Update(s.ctx, proxy), step.name)
		if step.bump {
			generation++
		}
		s.Require().Equal(generation, proxy.RouteGeneration, step.name)
		stored, err := s.repo.GetByID(s.ctx, proxy.ID)
		s.Require().NoError(err, step.name)
		s.Require().Equal(generation, stored.RouteGeneration, step.name)
	}
}
