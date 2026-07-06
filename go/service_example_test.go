// SPDX-License-Identifier: EUPL-1.2

package ws_test

import (
	"context"

	core "dappco.re/go"
	"dappco.re/go/ws"
)

// ExampleNewService constructs the ws service factory through
// `NewService` for go-ws Core service registration. The factory
// produces a *ws.Service ready for c.Service() — OnStartup wires the
// ws.* action handlers and runs the Hub event loop in the background.
//
// Usage example: `c.Service("ws", ws.NewService(ws.DefaultHubConfig()))`
func ExampleNewService() {
	factory := ws.NewService(ws.DefaultHubConfig())
	core.Println(factory != nil)
	// Output: true
}

// ExampleService_OnStartup registers the ws.* action handlers and
// spawns the Hub event loop through `Service.OnStartup` for go-ws
// Core service registration. Idempotent — multiple startups won't
// double-register.
//
// Usage example: `r := svc.OnStartup(ctx)`
func ExampleService_OnStartup() {
	c := core.New()
	r := ws.NewService(ws.DefaultHubConfig())(c)
	if !r.OK {
		core.Println("startup-init-failed")
		return
	}
	svc := r.Value.(*ws.Service)
	startup := svc.OnStartup(context.Background())
	defer svc.OnShutdown(context.Background())
	core.Println(startup.OK)
	// Output: true
}

// ExampleService_OnShutdown stops the Hub event loop through
// `Service.OnShutdown` for go-ws Core service registration.
//
// Usage example: `r := svc.OnShutdown(ctx)`
func ExampleService_OnShutdown() {
	c := core.New()
	r := ws.NewService(ws.DefaultHubConfig())(c)
	if !r.OK {
		core.Println("startup-init-failed")
		return
	}
	svc := r.Value.(*ws.Service)
	svc.OnStartup(context.Background())
	shutdown := svc.OnShutdown(context.Background())
	core.Println(shutdown.OK)
	// Output: true
}
