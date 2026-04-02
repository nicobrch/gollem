package appconfig

import (
	"bufio"
	"fmt"
	"net/url"
	"os"
	"strconv"
	"strings"
	"time"
)

const (
	defaultPort                 = "8000"
	defaultProvider             = "azure"
	defaultAzureAPIVersion      = "2024-10-21"
	defaultRequestTimeout       = 60 * time.Second
	defaultMaxBodyBytes         = int64(1 << 20) // 1 MiB
	defaultGatewayKeysFile      = "./data/gateway_keys.json"
	defaultAzureCompletionsPath = "/openai/deployments/%s/chat/completions"
)

type Config struct {
	ListenAddr      string
	GatewayAPIKey   string
	GatewayAdminKey string
	GatewayKeysFile string
	ProviderName    string
	DefaultModel    string
	RequestTimeout  time.Duration
	MaxBodyBytes    int64
	Azure           AzureConfig
}

type AzureConfig struct {
	APIKey         string
	UpstreamURL    string
	DeploymentName string
}

func Load() (Config, error) {
	if err := loadDotEnvFile(".env"); err != nil {
		return Config{}, err
	}

	port := envOrDefault("PORT", defaultPort)
	listenAddr := envOrDefault("LISTEN_ADDR", ":"+port)

	gatewayAPIKey := strings.TrimSpace(os.Getenv("GATEWAY_API_KEY"))
	gatewayAdminKey := strings.TrimSpace(os.Getenv("GATEWAY_ADMIN_API_KEY"))
	if gatewayAdminKey == "" {
		gatewayAdminKey = gatewayAPIKey
	}
	if gatewayAdminKey == "" {
		return Config{}, fmt.Errorf("GATEWAY_ADMIN_API_KEY is required (or GATEWAY_API_KEY fallback)")
	}

	gatewayKeysFile := strings.TrimSpace(envOrDefault("GATEWAY_KEYS_FILE", defaultGatewayKeysFile))
	if gatewayKeysFile == "" {
		return Config{}, fmt.Errorf("GATEWAY_KEYS_FILE cannot be empty")
	}

	providerName := strings.ToLower(envOrDefault("LLM_PROVIDER", defaultProvider))
	if providerName != defaultProvider {
		return Config{}, fmt.Errorf("unsupported LLM_PROVIDER: %q", providerName)
	}

	requestTimeout := defaultRequestTimeout
	if v := strings.TrimSpace(os.Getenv("REQUEST_TIMEOUT_SECONDS")); v != "" {
		seconds, err := strconv.Atoi(v)
		if err != nil || seconds <= 0 {
			return Config{}, fmt.Errorf("REQUEST_TIMEOUT_SECONDS must be a positive integer")
		}
		requestTimeout = time.Duration(seconds) * time.Second
	}

	maxBodyBytes := defaultMaxBodyBytes
	if v := strings.TrimSpace(os.Getenv("MAX_BODY_BYTES")); v != "" {
		size, err := strconv.ParseInt(v, 10, 64)
		if err != nil || size <= 0 {
			return Config{}, fmt.Errorf("MAX_BODY_BYTES must be a positive integer")
		}
		maxBodyBytes = size
	}

	azureCfg, err := loadAzureConfig()
	if err != nil {
		return Config{}, err
	}

	defaultModel := strings.TrimSpace(os.Getenv("DEFAULT_MODEL"))
	if defaultModel == "" {
		defaultModel = azureCfg.DeploymentName
	}

	return Config{
		ListenAddr:      listenAddr,
		GatewayAPIKey:   gatewayAPIKey,
		GatewayAdminKey: gatewayAdminKey,
		GatewayKeysFile: gatewayKeysFile,
		ProviderName:    providerName,
		DefaultModel:    defaultModel,
		RequestTimeout:  requestTimeout,
		MaxBodyBytes:    maxBodyBytes,
		Azure:           azureCfg,
	}, nil
}

func loadAzureConfig() (AzureConfig, error) {
	azureAPIKey := strings.TrimSpace(os.Getenv("AZURE_OPENAI_API_KEY"))
	if azureAPIKey == "" {
		azureAPIKey = strings.TrimSpace(os.Getenv("OPENAI_API_KEY"))
	}
	if azureAPIKey == "" {
		return AzureConfig{}, fmt.Errorf("AZURE_OPENAI_API_KEY (or OPENAI_API_KEY fallback) is required")
	}

	azureBaseURL := strings.TrimRight(strings.TrimSpace(os.Getenv("AZURE_OPENAI_BASE_URL")), "/")
	if azureBaseURL == "" {
		azureBaseURL = strings.TrimRight(strings.TrimSpace(os.Getenv("OPENAI_BASE_URL")), "/")
	}
	if azureBaseURL == "" {
		return AzureConfig{}, fmt.Errorf("AZURE_OPENAI_BASE_URL is required")
	}

	deploymentName := strings.TrimSpace(os.Getenv("AZURE_OPENAI_DEPLOYMENT"))
	if deploymentName == "" {
		deploymentName = strings.TrimSpace(os.Getenv("DEFAULT_MODEL"))
	}
	if deploymentName == "" {
		return AzureConfig{}, fmt.Errorf("AZURE_OPENAI_DEPLOYMENT is required (or DEFAULT_MODEL fallback)")
	}

	apiVersion := strings.TrimSpace(os.Getenv("AZURE_OPENAI_API_VERSION"))
	if apiVersion == "" {
		apiVersion = defaultAzureAPIVersion
	}

	upstreamURL, err := buildAzureUpstreamURL(azureBaseURL, deploymentName, apiVersion)
	if err != nil {
		return AzureConfig{}, err
	}
	if customURL := strings.TrimSpace(os.Getenv("AZURE_OPENAI_CHAT_COMPLETIONS_URL")); customURL != "" {
		upstreamURL = customURL
	}

	return AzureConfig{
		APIKey:         azureAPIKey,
		UpstreamURL:    upstreamURL,
		DeploymentName: deploymentName,
	}, nil
}

func buildAzureUpstreamURL(azureBaseURL, deploymentName, apiVersion string) (string, error) {
	base, err := url.Parse(azureBaseURL)
	if err != nil || base.Scheme == "" || base.Host == "" {
		return "", fmt.Errorf("AZURE_OPENAI_BASE_URL must be a valid absolute URL")
	}

	base.Path = strings.TrimRight(base.Path, "/") + fmt.Sprintf(defaultAzureCompletionsPath, url.PathEscape(deploymentName))
	query := base.Query()
	query.Set("api-version", apiVersion)
	base.RawQuery = query.Encode()

	return base.String(), nil
}

func envOrDefault(key, fallback string) string {
	value := strings.TrimSpace(os.Getenv(key))
	if value == "" {
		return fallback
	}
	return value
}

func loadDotEnvFile(path string) error {
	file, err := os.Open(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return fmt.Errorf("failed to open .env file: %w", err)
	}
	defer file.Close()

	scanner := bufio.NewScanner(file)
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}

		line = strings.TrimPrefix(line, "export ")
		parts := strings.SplitN(line, "=", 2)
		if len(parts) != 2 {
			continue
		}

		key := strings.TrimSpace(parts[0])
		value := strings.TrimSpace(parts[1])
		if key == "" {
			continue
		}

		if len(value) >= 2 {
			if (value[0] == '\'' && value[len(value)-1] == '\'') || (value[0] == '"' && value[len(value)-1] == '"') {
				value = value[1 : len(value)-1]
			}
		}

		if _, alreadySet := os.LookupEnv(key); !alreadySet {
			if err := os.Setenv(key, value); err != nil {
				return fmt.Errorf("failed to set env var %q from .env: %w", key, err)
			}
		}
	}

	if err := scanner.Err(); err != nil {
		return fmt.Errorf("failed reading .env file: %w", err)
	}

	return nil
}
