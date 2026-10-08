package main

import (
	"context"
	"fmt"
	"log"
	"net/http"
	"os/signal"
	"syscall"

	codefly "github.com/codefly-dev/sdk-go"
	"github.com/codefly-dev/core/standards"
)

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	mux := http.NewServeMux()
	mux.HandleFunc("/health", func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusOK) })

	// The address is the COMPOSITION's answer, never this file's. A hardcoded
	// port is wrong the moment two services want it, and it ignores what the
	// composition assigned this endpoint -- the service then listens somewhere
	// nothing is looking.
	//
	// Resolve, and refuse to start when it cannot be resolved: NetworkInstance()
	// is nil on failure, so reading .Port off it would panic with nothing to
	// read and the only symptom would be a port that never accepts.
	instance, err := codefly.For(ctx).API(standards.HTTP).ResolveNetworkInstance()
	if err != nil {
		panic(fmt.Errorf("cannot resolve this service's %s address: %w", standards.HTTP, err))
	}

	srv := &http.Server{Addr: fmt.Sprintf(":%d", instance.Port), Handler: mux}
	go func() {
		if err := srv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			log.Fatal(err)
		}
	}()

	<-ctx.Done()
	_ = srv.Shutdown(context.Background())
}
