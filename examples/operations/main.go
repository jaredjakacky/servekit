package main

import (
	"context"
	"log"
	"net/http"
	"sync/atomic"

	opskit "github.com/jaredjakacky/opskit"
	servekit "github.com/jaredjakacky/servekit"
)

// The operations example is the primary Kit Series composition path. The
// application registers components in one Opskit registry, and Servekit
// presents that registry without knowing which packages produced them.
func main() {
	database := &databaseComponent{}
	database.ready.Store(true)

	ops := opskit.NewRegistry()
	ops.MustRegister(database, opskit.Required())
	ops.MustRegister(opskit.ComponentFunc{
		Info: opskit.ComponentInfo{
			Name:        "build",
			Kind:        "runtime",
			Description: "example build information",
		},
		Fn: func(context.Context) opskit.Status {
			return opskit.ReadyStatus("development build")
		},
	}, opskit.Informational())

	s := servekit.New(
		servekit.WithAddr(":8082"),
		servekit.WithOps(
			ops,
			servekit.WithOpsAdmin(),
			servekit.WithOpsAdminAuthGate(func(r *http.Request) error {
				if r.Header.Get("X-Admin-Token") == "local-dev" {
					return nil
				}
				return servekit.Error(http.StatusForbidden, "admin token required", nil)
			}),
		),
	)

	s.Handle(http.MethodPost, "/demo/database/{state}", func(r *http.Request) (any, error) {
		switch r.PathValue("state") {
		case "ready":
			database.ready.Store(true)
		case "not-ready":
			database.ready.Store(false)
		default:
			return nil, servekit.Error(http.StatusBadRequest, "state must be ready or not-ready", nil)
		}
		return map[string]any{"database_ready": database.ready.Load()}, nil
	})

	log.Println("operations example listening on :8082")
	log.Println("try:")
	log.Println(`  curl -i http://127.0.0.1:8082/readyz`)
	log.Println(`  curl -i -H 'X-Admin-Token: local-dev' http://127.0.0.1:8082/admin/components`)
	log.Println(`  curl -i -H 'X-Admin-Token: local-dev' http://127.0.0.1:8082/admin/components/database`)
	log.Println(`  curl -i -X POST http://127.0.0.1:8082/demo/database/not-ready`)
	log.Println(`  curl -i -X POST http://127.0.0.1:8082/demo/database/ready`)
	if err := s.Run(context.Background()); err != nil {
		log.Fatal(err)
	}
}

type databaseComponent struct {
	ready atomic.Bool
}

func (*databaseComponent) ComponentInfo() opskit.ComponentInfo {
	return opskit.ComponentInfo{
		Name:        "database",
		Kind:        "state",
		Description: "synthetic database state for the operations example",
	}
}

func (c *databaseComponent) Status(context.Context) opskit.Status {
	if c.ready.Load() {
		return opskit.ReadyStatus("database connection state is ready")
	}
	return opskit.NotReadyStatus("database connection state is not ready")
}

func (c *databaseComponent) Inspect(context.Context) (opskit.Inspection, error) {
	return opskit.Inspection{
		Summary: "cached database state",
		Details: map[string]any{
			"ready":  c.ready.Load(),
			"source": "local demo state",
		},
	}, nil
}
