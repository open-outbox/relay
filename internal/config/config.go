package config

import (
	"strings"
	"time"

	"github.com/spf13/viper"
)

type Environment string

const (
	Development Environment = "development"
	Production  Environment = "production"
)

type Config struct {
	// Infrastructure Swtich
	StorageType   string `mapstructure:"STORAGE_TYPE"`   // "postgres", "mysql"
	PublisherType string `mapstructure:"PUBLISHER_TYPE"` // "nats", "kafka", "redis", "stdout"

	// Unified Connection Strings
	StorageURL   string `mapstructure:"STORAGE_URL"`   // e.g. "postgres://user:pass@localhost:5432/db"
	PublisherURL string `mapstructure:"PUBLISHER_URL"` // e.g. "nats://localhost:4222" or "kafka://localhost:9092"

	// Tuning
	PollInterval  time.Duration `mapstructure:"POLL_INTERVAL"`
	BatchSize     int           `mapstructure:"BATCH_SIZE"`
	LeaseTimeout  time.Duration `mapstructure:"LEASE_TIMEOUT"`
	ReapBatchSize int           `mapstructure:"REAP_BATCH_SIZE"`
	ServerPort    string        `mapstructure:"SERVER_PORT"`
	Environment   Environment   `mapstructure:"ENVIRONMENT"`
	RELAY_ID      string        `mapstructure:"RELAY_ID"`

	// Retry Tuning
	RetryMaxAttempts int           `mapstructure:"RETRY_MAX_ATTEMPTS"`
	RetryBaseDelay   time.Duration `mapstructure:"RETRY_BASE_DELAY"`
	RetryMaxDelay    time.Duration `mapstructure:"RETRY_MAX_DELAY"`
	RetryJitter      float64       `mapstructure:"RETRY_JITTER"`
}

func Load() (*Config, error) {
	v := viper.New()

	// 1. Set Defaults
	v.SetDefault("STORAGE_TYPE", "memory")
	v.SetDefault("PUBLISHER_TYPE", "stdout")
	v.SetDefault("POLL_INTERVAL", "500ms")
	v.SetDefault("BATCH_SIZE", 100)
	v.SetDefault("LEASE_TIMEOUT", "3m")
	v.SetDefault("REAP_BATCH_SIZE", 500)
	v.SetDefault("SERVER_PORT", ":8080")
	v.SetDefault("ENVIRONMENT", Production)

	// Retry Defaults
	v.SetDefault("RETRY_MAX_ATTEMPTS", 10)
	v.SetDefault("RETRY_BASE_DELAY", "1s")
	v.SetDefault("RETRY_MAX_DELAY", "24h")
	v.SetDefault("RETRY_JITTER", 0.15)

	// Read from .env or config.yaml (Optional)
	v.SetConfigName("config")
	v.AddConfigPath(".")
	v.AutomaticEnv()
	v.SetEnvKeyReplacer(strings.NewReplacer(".", "_"))

	if err := v.ReadInConfig(); err != nil {
		if _, ok := err.(viper.ConfigFileNotFoundError); !ok {
			return nil, err
		}
	}

	// Try to read .env (The local overrides)
	v.SetConfigFile(".env")
	v.SetConfigType("env")
	if err := v.MergeInConfig(); err != nil {
		println("Failed to read configs from .env")
	}

	// Unmarshal into our struct
	var cfg Config
	if err := v.Unmarshal(&cfg); err != nil {
		return nil, err
	}

	return &cfg, nil
}
