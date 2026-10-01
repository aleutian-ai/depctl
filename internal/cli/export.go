package cli

import (
	"fmt"

	"github.com/spf13/cobra"

	"aleutian-ai/ragctl/internal/daemon/api"
)

func newExportCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "export",
		Short: "Push synced dependency knowledge into a cross-agent memory system",
	}
	cmd.AddCommand(newExportMem0Cmd())
	cmd.AddCommand(newExportGraphitiCmd())
	cmd.AddCommand(newExportCogneeCmd())
	return cmd
}

func newExportMem0Cmd() *cobra.Command {
	var projectID string
	var dependencies []string
	var endpoint, apiKeyEnv string

	cmd := &cobra.Command{
		Use:   "mem0",
		Short: "Push synced dependency knowledge into a user's own Mem0 instance",
		Long: `Push a project's (or one dependency's) already-synced dependency knowledge into a user's own Mem0 instance as tagged memories.

A real, disclosed fact about Mem0: self-hosted Mem0 has telemetry on by default, and multiple open upstream issues (mem0ai/mem0 #3762, #3729, #2683) report it still spawns telemetry threads even with MEM0_TELEMETRY=false set. Running this command sends your dependency knowledge, and whatever metadata Mem0's own SDK collects, to the Mem0 instance you point it at — review that instance's own telemetry posture first.`,
		RunE: func(cmd *cobra.Command, args []string) error {
			return runExportMem0(cmd, projectID, dependencies, endpoint, apiKeyEnv)
		},
	}
	cmd.Flags().StringVar(&projectID, "project", "", "project ID to export (required)")
	cmd.Flags().StringArrayVar(&dependencies, "dependency", nil, "limit to a dependency name (repeatable); default is every dependency with an active generation")
	cmd.Flags().StringVar(&endpoint, "endpoint", "", "Mem0 REST API base URL (overrides export.mem0.endpoint in config.yaml)")
	cmd.Flags().StringVar(&apiKeyEnv, "api-key-env", "", "environment variable holding the Mem0 API key (overrides export.mem0.api_key_env in config.yaml)")
	_ = cmd.MarkFlagRequired("project")
	return cmd
}

func runExportMem0(cmd *cobra.Command, projectID string, dependencies []string, endpoint, apiKeyEnv string) error {
	c, err := ensureDaemon(cmd.Context())
	if err != nil {
		return err
	}

	req := api.ExportMem0Request{
		ProjectID:    projectID,
		Dependencies: dependencies,
		Endpoint:     endpoint,
		APIKeyEnv:    apiKeyEnv,
	}
	resp, err := c.ExportMem0(cmd.Context(), req, cmd.OutOrStdout())
	if err != nil {
		return err
	}

	var failed int
	for _, r := range resp.Results {
		failed += r.Failed
	}
	if failed > 0 {
		return fmt.Errorf("%d chunk(s) failed to export", failed)
	}
	return nil
}

func newExportGraphitiCmd() *cobra.Command {
	var projectID string
	var dependencies []string
	var endpoint, authTokenEnv string

	cmd := &cobra.Command{
		Use:   "graphiti",
		Short: "Push synced dependency knowledge into a user's own Graphiti instance",
		Long: `Push a project's (or one dependency's) already-synced dependency knowledge into a user's own Graphiti instance as structured episodes (one per dependency).

A real, disclosed fact about Graphiti: the self-hosted REST server has no authentication of its own by default (getzep/graphiti#1716) — every endpoint, including destructive ones, is reachable by anyone who can reach the port. Never expose it beyond localhost or a private network without your own auth in front of it (see --auth-token-env for a bearer token your own reverse proxy can check).`,
		RunE: func(cmd *cobra.Command, args []string) error {
			return runExportGraphiti(cmd, projectID, dependencies, endpoint, authTokenEnv)
		},
	}
	cmd.Flags().StringVar(&projectID, "project", "", "project ID to export (required)")
	cmd.Flags().StringArrayVar(&dependencies, "dependency", nil, "limit to a dependency name (repeatable); default is every dependency with an active generation")
	cmd.Flags().StringVar(&endpoint, "endpoint", "", "Graphiti REST API base URL (overrides export.graphiti.endpoint in config.yaml)")
	cmd.Flags().StringVar(&authTokenEnv, "auth-token-env", "", "environment variable holding a bearer token for your own reverse-proxy auth (overrides export.graphiti.auth_token_env in config.yaml) — Graphiti has none of its own")
	_ = cmd.MarkFlagRequired("project")
	return cmd
}

func runExportGraphiti(cmd *cobra.Command, projectID string, dependencies []string, endpoint, authTokenEnv string) error {
	c, err := ensureDaemon(cmd.Context())
	if err != nil {
		return err
	}

	req := api.ExportGraphitiRequest{
		ProjectID:    projectID,
		Dependencies: dependencies,
		Endpoint:     endpoint,
		AuthTokenEnv: authTokenEnv,
	}
	resp, err := c.ExportGraphiti(cmd.Context(), req, cmd.OutOrStdout())
	if err != nil {
		return err
	}

	var failed int
	for _, r := range resp.Results {
		failed += r.Failed
	}
	if failed > 0 {
		return fmt.Errorf("%d dependency episode(s) failed to export", failed)
	}
	return nil
}

func newExportCogneeCmd() *cobra.Command {
	var projectID string
	var dependencies []string
	var endpoint, authTokenEnv string

	cmd := &cobra.Command{
		Use:   "cognee",
		Short: "Push synced dependency knowledge into a user's own Cognee instance",
		Long: `Push a project's (or one dependency's) already-synced dependency knowledge into a user's own Cognee instance, then trigger its own cognify (ECL) pipeline over it.

Two real, disclosed facts about Cognee: it sends anonymous telemetry by default (set TELEMETRY_DISABLED=1 on the Cognee server itself to opt out), and the cognify step's own telemetry call is known to block for 60-120s when that telemetry endpoint is unreachable (topoteretes/cognee#2120) — a long delay after this command reports "cognify triggered" is this, not a ragctl bug. Cognee is also open by default (no auth unless you enable it on the server) — never expose it beyond localhost or a private network without that.`,
		RunE: func(cmd *cobra.Command, args []string) error {
			return runExportCognee(cmd, projectID, dependencies, endpoint, authTokenEnv)
		},
	}
	cmd.Flags().StringVar(&projectID, "project", "", "project ID to export (required)")
	cmd.Flags().StringArrayVar(&dependencies, "dependency", nil, "limit to a dependency name (repeatable); default is every dependency with an active generation")
	cmd.Flags().StringVar(&endpoint, "endpoint", "", "Cognee REST API base URL (overrides export.cognee.endpoint in config.yaml)")
	cmd.Flags().StringVar(&authTokenEnv, "auth-token-env", "", "environment variable holding a bearer token (overrides export.cognee.auth_token_env in config.yaml) — Cognee has none of its own unless enabled server-side")
	_ = cmd.MarkFlagRequired("project")
	return cmd
}

func runExportCognee(cmd *cobra.Command, projectID string, dependencies []string, endpoint, authTokenEnv string) error {
	c, err := ensureDaemon(cmd.Context())
	if err != nil {
		return err
	}

	req := api.ExportCogneeRequest{
		ProjectID:    projectID,
		Dependencies: dependencies,
		Endpoint:     endpoint,
		AuthTokenEnv: authTokenEnv,
	}
	resp, err := c.ExportCognee(cmd.Context(), req, cmd.OutOrStdout())
	if err != nil {
		return err
	}

	var failed int
	for _, r := range resp.Results {
		failed += r.Failed
	}
	if resp.CognifyError != "" {
		return fmt.Errorf("cognify failed: %s", resp.CognifyError)
	}
	if failed > 0 {
		return fmt.Errorf("%d dependency file(s) failed to add", failed)
	}
	return nil
}
