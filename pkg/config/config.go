package config

import (
    "fmt"

    "github.com/spf13/viper"
)

// GatewayConfig captures runtime settings for the gateway service.
type GatewayConfig struct {
    ListenAddr      string `mapstructure:"listen_addr"`
    QueueBase       string `mapstructure:"queue_base_url"`
    OrchestratorURL string `mapstructure:"orchestrator_url"`
}

// LoadGateway loads gateway configuration from defaults, files, and env vars.
func LoadGateway() (GatewayConfig, error) {
    v := viper.New()
    v.SetConfigName("config")
    v.AddConfigPath("./configs")
    v.SetEnvPrefix("GATEWAY")
    v.AutomaticEnv()

    v.SetDefault("listen_addr", ":8080")
    v.SetDefault("queue_base_url", "https://queue.vyvo.local")
    v.SetDefault("orchestrator_url", "http://localhost:8081")

    if err := v.ReadInConfig(); err != nil {
        if _, ok := err.(viper.ConfigFileNotFoundError); !ok {
            return GatewayConfig{}, fmt.Errorf("load config: %w", err)
        }
    }

    var cfg GatewayConfig
    if err := v.Unmarshal(&cfg); err != nil {
        return GatewayConfig{}, fmt.Errorf("unmarshal config: %w", err)
    }

    return cfg, nil
}
