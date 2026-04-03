package main

import (
	"log"
	"net/http"
	"net/url"
	"time"

	"gollem/internal/appconfig"
	"gollem/internal/gateway"
	"gollem/internal/gatewaykeys"
	"gollem/internal/httpclient"
	"gollem/internal/providers"
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

	keyStore := gatewaykeys.NewFileStore(cfg.GatewayKeysFile)
	keyManager := gatewaykeys.NewManager(keyStore)

	g := gateway.New(httpclient.New(cfg.RequestTimeout), provider, keyManager, gateway.Config{
		GatewayAPIKey: cfg.GatewayAPIKey,
		AdminAPIKey:   cfg.GatewayAdminKey,
		DefaultModel:  cfg.DefaultModel,
		MaxBodyBytes:  cfg.MaxBodyBytes,
		MaxInFlight:   cfg.MaxInFlight,
	})

	server := &http.Server{
		Addr:              cfg.ListenAddr,
		Handler:           g.Handler(),
		ReadHeaderTimeout: 5 * time.Second,
		ReadTimeout:       15 * time.Second,
		IdleTimeout:       120 * time.Second,
		MaxHeaderBytes:    1 << 20,
	}

	log.Printf("gollem listening on %s", cfg.ListenAddr)
	log.Printf("provider: %s", provider.Name())
	log.Printf("gateway keys file: %s", cfg.GatewayKeysFile)
	if cfg.ProviderName == "azure" {
		azureHost := cfg.Azure.UpstreamURL
		if parsedURL, parseErr := url.Parse(cfg.Azure.UpstreamURL); parseErr == nil && parsedURL.Host != "" {
			azureHost = parsedURL.Host
		}
		log.Printf("azure upstream host: %s", azureHost)
	}

	if err := server.ListenAndServe(); err != nil && err != http.ErrServerClosed {
		log.Fatalf("server error: %v", err)
	}
}
