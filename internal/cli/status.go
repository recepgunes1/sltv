package cli

import (
	"fmt"
	"text/tabwriter"

	"github.com/sltv/sltv/internal/version"
	"github.com/spf13/cobra"
)

func (c *CLI) newVersionCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "version",
		Short: "Print the sctl version",
		RunE: func(cmd *cobra.Command, _ []string) error {
			_, err := fmt.Fprintln(cmd.OutOrStdout(), version.String())
			return err
		},
	}
}

func (c *CLI) newStatusCmd() *cobra.Command {
	var clusterFlag bool
	cmd := &cobra.Command{
		Use:   "status",
		Short: "Show node (and optionally cluster) status",
		RunE: func(cmd *cobra.Command, _ []string) error {
			ctx, cancel := withTimeout(cmd.Context(), c.rf.Timeout)
			defer cancel()
			client, err := dial(ctx, c.rf)
			if err != nil {
				return err
			}
			defer func() { _ = client.Close() }()
			ns, err := client.GetNodeStatus(ctx, nil)
			if err != nil {
				return err
			}
			if c.rf.JSON {
				out := map[string]any{"node": ns}
				if clusterFlag {
					cs, err := client.GetClusterStatus(ctx, nil)
					if err != nil {
						return err
					}
					out["cluster"] = cs
				}
				return printJSON(cmd.OutOrStdout(), out)
			}
			ew := &errWriter{w: cmd.OutOrStdout()}
			ew.printf("Node:    %s\n", ns.NodeId)
			ew.printf("Version: %s\n", ns.Version)
			ew.printf("Cluster: %v\n", ns.ClusterEnabled)
			if ew.err != nil {
				return ew.err
			}
			tw := tabwriter.NewWriter(cmd.OutOrStdout(), 0, 4, 2, ' ', 0)
			_, _ = fmt.Fprintln(tw, "VG\tSIZE\tFREE")
			for _, vg := range ns.Vgs {
				_, _ = fmt.Fprintf(tw, "%s\t%s\t%s\n", vg.Name, humanSize(vg.SizeBytes), humanSize(vg.FreeBytes))
			}
			_ = tw.Flush()
			if clusterFlag {
				cs, err := client.GetClusterStatus(ctx, nil)
				if err != nil {
					return err
				}
				ew2 := &errWriter{w: cmd.OutOrStdout()}
				ew2.printf("\nCluster status\n")
				ew2.printf("  Enabled:        %v\n", cs.Enabled)
				ew2.printf("  Leader:         %s\n", cs.Leader)
				ew2.printf("  Disks:          %d\n", cs.DiskCount)
				ew2.printf("  Attachments:    %d\n", cs.AttachmentCount)
				ew2.printf("  Nodes:\n")
				for _, n := range cs.Nodes {
					ew2.printf("    - %s (%s)\n", n.NodeId, n.Version)
				}
				return ew2.err
			}
			return nil
		},
	}
	cmd.Flags().BoolVar(&clusterFlag, "cluster", false, "include cluster status")
	return cmd
}
