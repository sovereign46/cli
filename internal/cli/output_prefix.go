package cli

import (
	"github.com/sovereign46/cli/internal/airplane"
	"github.com/sovereign46/cli/internal/config"
	"github.com/sovereign46/cli/internal/output"
)

func OutputPrefix(env map[string]string, configPath string) string {
	return activeOutputPrefix(config.NewStore(env, configPath))
}

func activeOutputPrefix(store *config.Store) string {
	cfg, err := store.LoadConfig()
	return activeOutputPrefixForConfig(cfg, err)
}

func activeOutputPrefixForConfig(cfg config.Config, err error) string {
	if err != nil {
		return output.DefaultPrefix
	}
	if cfg.ActiveMode() == config.ModeAirplane {
		return airplane.Prefix
	}
	return output.DefaultPrefix
}
