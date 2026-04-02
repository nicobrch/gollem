package main

import (
	"log"
	"net/http"
	"time"

	"go-llm/internal/appconfig"
	"go-llm/internal/gateway"
	"go-llm/internal/httpclient"
	"go-llm/internal/providers"
)

func main() {
	cfg, err := appconfig.Load()
	if err != nil {
		log.Fatalf("config error: %v", err)
	}

	provider, err := providers.New(cfg)
	if err != nil {
		log.Fatalf("provider error: %v", err)
	}

	g := gateway.New(httpclient.New(cfg.RequestTimeout), provider, gateway.Config{
		GatewayAPIKey: cfg.GatewayAPIKey,
		DefaultModel:  cfg.DefaultModel,
		MaxBodyBytes:  cfg.MaxBodyBytes,
	})

	server := &http.Server{
		Addr:              cfg.ListenAddr,
		Handler:           g.Handler(),
		ReadHeaderTimeout: 5 * time.Second,
		ReadTimeout:       15 * time.Second,
		IdleTimeout:       120 * time.Second,
		MaxHeaderBytes:    1 << 20,
	}

	log.Printf("go-llm listening on %s", cfg.ListenAddr)
	log.Printf("provider: %s", provider.Name())
	if cfg.ProviderName == "azure" {
		log.Printf("azure upstream: %s", cfg.Azure.UpstreamURL)
	}

	if err := server.ListenAndServe(); err != nil && err != http.ErrServerClosed {
		log.Fatalf("server error: %v", err)
	}
}
