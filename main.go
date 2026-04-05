package main

import (
	"context"
	"fmt"
	"log"
	"net/http"
	"net/url"
	"os"
	"os/signal"
	"syscall"
	"time"

	"gollem/internal/appconfig"
	"gollem/internal/gateway"
	"gollem/internal/gatewaykeys"
	"gollem/internal/httpclient"
	"gollem/internal/providers"
	"gollem/internal/semanticcache"
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

	keyStore, keyStoreClose, err := newKeyStoreFromConfig(cfg)
	if err != nil {
		log.Fatalf("key store error: %v", err)
	}
	if keyStoreClose != nil {
		defer keyStoreClose()
	}

	keyManager := gatewaykeys.NewManager(keyStore)
	httpClient := httpclient.New(cfg.RequestTimeout)

	semCache, err := semanticcache.New(cfg.SemanticCache, cfg.Azure, httpClient)
	if err != nil {
		log.Fatalf("semantic cache error: %v", err)
	}
	if semCache != nil {
		defer func() {
			if closeErr := semCache.Close(); closeErr != nil {
				log.Printf("failed closing semantic cache: %v", closeErr)
			}
		}()
	}

	g := gateway.New(httpClient, provider, keyManager, gateway.Config{
		AdminAPIKey:          cfg.GatewayAdminKey,
		DefaultModel:         cfg.DefaultModel,
		AzureDeployment:      cfg.Azure.DeploymentName,
		MaxBodyBytes:         cfg.MaxBodyBytes,
		MaxInFlight:          cfg.MaxInFlight,
		LogPromptSummaries:   cfg.LogPromptSummaries,
		LogResponseSummaries: cfg.LogResponseSummaries,
		SemanticCache:        semCache,
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
	log.Printf("gateway keys backend: %s", cfg.GatewayKeysBackend)
	if cfg.GatewayKeysBackend == "file" {
		log.Printf("gateway keys file: %s", cfg.GatewayKeysFile)
	}
	if cfg.ProviderName == "azure" {
		azureHost := cfg.Azure.UpstreamURL
		if parsedURL, parseErr := url.Parse(cfg.Azure.UpstreamURL); parseErr == nil && parsedURL.Host != "" {
			azureHost = parsedURL.Host
		}
		log.Printf("azure upstream host: %s", azureHost)
	}
	if cfg.SemanticCache.Enabled {
		log.Printf("semantic cache: enabled addr=%s", cfg.SemanticCache.RedisAddr)
	} else {
		log.Printf("semantic cache: disabled")
	}

	shutdownCtx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	serverErr := make(chan error, 1)
	go func() {
		if err := server.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			serverErr <- err
			return
		}
		serverErr <- nil
	}()

	select {
	case err := <-serverErr:
		if err != nil {
			log.Fatalf("server error: %v", err)
		}
	case <-shutdownCtx.Done():
		log.Printf("shutdown signal received")
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		if err := server.Shutdown(ctx); err != nil {
			log.Fatalf("server shutdown error: %v", err)
		}
		if err := <-serverErr; err != nil {
			log.Fatalf("server error: %v", err)
		}
		log.Printf("server stopped gracefully")
	}
}

func newKeyStoreFromConfig(cfg appconfig.Config) (gatewaykeys.Store, func(), error) {
	switch cfg.GatewayKeysBackend {
	case "file":
		return gatewaykeys.NewFileStore(cfg.GatewayKeysFile), nil, nil
	case "postgres":
		store, err := gatewaykeys.NewPostgresStore(cfg.GatewayKeysPostgres.DSN)
		if err != nil {
			return nil, nil, err
		}
		return store, func() {
			if err := store.Close(); err != nil {
				log.Printf("failed closing postgres key store: %v", err)
			}
		}, nil
	default:
		return nil, nil, fmt.Errorf("unsupported key backend: %q", cfg.GatewayKeysBackend)
	}
}
