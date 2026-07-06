// SPDX-License-Identifier: EUPL-1.2

package ws

import (
	"context"

	core "dappco.re/go"
)

// --- AX-7 compliance triplets ---

func TestService_NewService_Good(t *core.T) {
	cfg := HubConfig{}
	factory := NewService(cfg)
	core.AssertNotNil(t, factory)
}

func TestService_NewService_Bad(t *core.T) {
	cfg := DefaultHubConfig()
	factory := NewService(cfg)
	core.AssertNotNil(t, factory)
}

func TestService_NewService_Ugly(t *core.T) {
	a := NewService(DefaultHubConfig())
	b := NewService(DefaultHubConfig())
	core.AssertNotNil(t, a)
	core.AssertNotNil(t, b)
}

func serviceForTest(t *core.T) *Service {
	t.Helper()
	c := core.New()
	r := NewService(DefaultHubConfig())(c)
	core.RequireTrue(t, r.OK)
	return r.Value.(*Service)
}

func TestService_Service_OnStartup_Good(t *core.T) {
	svc := serviceForTest(t)
	startup := svc.OnStartup(context.Background())
	defer svc.OnShutdown(context.Background())
	core.AssertTrue(t, startup.OK)
}

func TestService_Service_OnStartup_Bad(t *core.T) {
	var s *Service
	r := s.OnStartup(context.Background())
	core.AssertTrue(t, r.OK)
}

func TestService_Service_OnStartup_Ugly(t *core.T) {
	svc := serviceForTest(t)
	svc.OnStartup(context.Background())
	defer svc.OnShutdown(context.Background())
	again := svc.OnStartup(context.Background())
	core.AssertTrue(t, again.OK)
}

func TestService_Service_OnShutdown_Good(t *core.T) {
	svc := serviceForTest(t)
	svc.OnStartup(context.Background())
	shutdown := svc.OnShutdown(context.Background())
	core.AssertTrue(t, shutdown.OK)
}

func TestService_Service_OnShutdown_Bad(t *core.T) {
	var s *Service
	r := s.OnShutdown(context.Background())
	core.AssertTrue(t, r.OK)
}

func TestService_Service_OnShutdown_Ugly(t *core.T) {
	svc := serviceForTest(t)
	svc.OnStartup(context.Background())
	svc.OnShutdown(context.Background())
	again := svc.OnShutdown(context.Background())
	core.AssertTrue(t, again.OK)
}

// --- end AX-7 compliance triplets ---
