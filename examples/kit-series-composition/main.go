package main

import (
	"context"
	"errors"
	"fmt"
	"log"
	"net/http"
	"os/signal"
	"syscall"
	"time"

	configkit "github.com/jaredjakacky/configkit"
	dependkit "github.com/jaredjakacky/dependkit"
	opskit "github.com/jaredjakacky/opskit"
	servekit "github.com/jaredjakacky/servekit"
	workerkit "github.com/jaredjakacky/workerkit"
)

type appConfig struct {
	ServiceName string `json:"service_name"`
	Message     string `json:"message"`
}

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	manager, err := loadConfig(ctx)
	if err != nil {
		log.Fatalf("load config: %v", err)
	}
	dependencies, err := newDependencies()
	if err != nil {
		log.Fatalf("register dependencies: %v", err)
	}
	runtime, err := newRuntime(dependencies)
	if err != nil {
		log.Fatalf("create worker runtime: %v", err)
	}

	// The application is the composition root. Each domain kit exposes Opskit
	// contracts, and Servekit receives only their shared registry.
	operations := opskit.NewRegistry()
	operations.MustRegister(manager, opskit.Required())
	operations.MustRegister(dependencies, opskit.Required())
	operations.MustRegister(runtime, opskit.Required())

	server := servekit.New(
		servekit.WithAddr(":8080"),
		servekit.WithOps(
			operations,
			servekit.WithOpsAdmin(),
			servekit.WithOpsAdminAuthGate(requireOpsToken),
		),
	)
	server.Handle(http.MethodGet, "/message", func(*http.Request) (any, error) {
		cfg, ok := manager.Value()
		if !ok {
			return nil, servekit.Error(http.StatusServiceUnavailable, "configuration unavailable", nil)
		}
		return map[string]string{"service": cfg.ServiceName, "message": cfg.Message}, nil
	})

	if err := runtime.StartAll(ctx); err != nil {
		log.Fatalf("start workers: %v", err)
	}
	printUsage()
	runErr := server.Run(ctx)

	shutdownCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 10*time.Second)
	defer cancel()
	shutdownErr := runtime.Shutdown(shutdownCtx)
	if err := errors.Join(runErr, shutdownErr); err != nil && !errors.Is(err, context.Canceled) {
		log.Fatal(err)
	}
}

func loadConfig(ctx context.Context) (*configkit.Manager[appConfig], error) {
	manager := configkit.NewManager[appConfig](configkit.WithIdentity("config"))
	source := configkit.NewBytesSource(
		[]byte(`{"service_name":"kit-series-demo","message":"hello from composed kits"}`),
		configkit.SourceMetadata{Name: "embedded", Kind: "memory"},
		"v1",
	)
	pipeline := configkit.Pipeline[appConfig]{
		Decode: configkit.JSONDecoder[appConfig](),
		ValidateConfig: func(_ context.Context, cfg appConfig) error {
			if cfg.ServiceName == "" || cfg.Message == "" {
				return errors.New("service_name and message are required")
			}
			return nil
		},
		Redact: func(_ context.Context, cfg appConfig) (configkit.RedactedView, error) {
			return configkit.RedactedView{
				"service_name": cfg.ServiceName,
				"message":      cfg.Message,
			}, nil
		},
		Checksum: configkit.SHA256JSONChecksum[appConfig](),
	}

	if _, err := manager.LoadFromSource(ctx, configkit.AttemptKindInitialLoad, source, pipeline); err != nil {
		return nil, err
	}
	return manager, nil
}

func newDependencies() (*dependkit.Registry, error) {
	registry := dependkit.NewRegistry()
	err := registry.Register(dependkit.DependencySpec{
		Name:        "catalog-api",
		Kind:        "http",
		Description: "application-owned catalog dependency",
		Readiness:   dependkit.ReadinessRequired,
		StaleAfter:  2 * time.Minute,
		Check: dependkit.CheckResultFunc(func(context.Context) dependkit.CheckResult {
			return dependkit.Healthy("catalog API is reachable")
		}),
	})
	if err != nil {
		return nil, err
	}
	return registry, nil
}

func newRuntime(dependencies *dependkit.Registry) (*workerkit.Runtime, error) {
	runtime, err := workerkit.New(workerkit.Identity{Name: "workers"})
	if err != nil {
		return nil, err
	}
	err = runtime.Register(workerkit.WorkerSpec{
		Name:        "dependencies",
		Description: "Keeps cached dependency state fresh.",
		Worker: workerkit.NewCheckGroupLoop(
			dependencies,
			workerkit.WithCheckInterval(30*time.Second),
			workerkit.WithCheckTimeout(5*time.Second),
		),
	}, workerkit.WithWorkerReadinessContribution(false))
	if err != nil {
		return nil, err
	}
	return runtime, nil
}

func requireOpsToken(r *http.Request) error {
	if r.Header.Get("X-Ops-Token") == "local-dev" {
		return nil
	}
	return servekit.Error(http.StatusUnauthorized, "ops token required", nil)
}

func printUsage() {
	fmt.Println("Kit Series composition example listening on :8080")
	fmt.Println("  curl -i http://localhost:8080/message")
	fmt.Println("  curl -i http://localhost:8080/readyz")
	fmt.Println("  curl -s -H 'X-Ops-Token: local-dev' http://localhost:8080/admin/components")
	fmt.Println("  curl -s -H 'X-Ops-Token: local-dev' http://localhost:8080/admin/components/config")
	fmt.Println("  curl -s -H 'X-Ops-Token: local-dev' http://localhost:8080/admin/components/dependencies")
	fmt.Println("  curl -s -H 'X-Ops-Token: local-dev' http://localhost:8080/admin/components/workers")
}
