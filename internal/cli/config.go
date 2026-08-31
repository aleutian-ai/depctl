package cli

import (
	"errors"
	"fmt"

	"github.com/spf13/cobra"

	"aleutian-ai/ragctl/internal/config"
)

func newConfigCmd() *cobra.Command {
	configCmd := &cobra.Command{
		Use:   "config",
		Short: "Manage ragctl configuration",
	}

	var configPath string
	validateCmd := &cobra.Command{
		Use:   "validate",
		Short: "Validate the ragctl configuration file",
		RunE: func(cmd *cobra.Command, args []string) error {
			return runConfigValidate(cmd, configPath)
		},
	}
	validateCmd.Flags().StringVar(&configPath, "config", "", "path to config.yaml (default: platform-specific location, see `ragctl init`)")
	configCmd.AddCommand(validateCmd)

	return configCmd
}

func runConfigValidate(cmd *cobra.Command, configPath string) error {
	if configPath == "" {
		var err error
		configPath, err = config.DefaultConfigPath()
		if err != nil {
			return fmt.Errorf("resolve config path: %w", err)
		}
	}

	_, err := config.Load(configPath)
	if err != nil {
		if errors.Is(err, config.ErrConfigNotFound) {
			return fmt.Errorf("%w (run `ragctl init` to create one)", err)
		}
		return err
	}

	fmt.Fprintf(cmd.OutOrStdout(), "config OK: %s\n", configPath)
	return nil
}
