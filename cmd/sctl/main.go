// Command sctl is the SLTV CLI. It talks to sltvd over gRPC and
// drives all disk and attachment operations from the shell.
package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"
	"text/tabwriter"
	"time"

	"github.com/spf13/cobra"

	pb "github.com/sltv/sltv/api/proto/v1"
	"github.com/sltv/sltv/internal/config"
	"github.com/sltv/sltv/internal/version"
	"github.com/sltv/sltv/pkg/sltvclient"
)

// rootFlags is the set of global CLI flags shared by all subcommands.
type rootFlags struct {
	Addr         string
	TLSCert      string
	TLSKey       string
	TLSCA        string
	TLSServer    string
	JSON         bool
	Timeout      time.Duration
}

func main() {
	if err := newRootCmd().Execute(); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

func newRootCmd() *cobra.Command {
	rf := &rootFlags{}
	root := &cobra.Command{
		Use:           "sctl",
		Short:         "SLTV command-line client",
		SilenceUsage:  true,
		SilenceErrors: true,
		Long: `sctl is the command-line client for SLTV (Shared Logical Thin Volume).
It connects to sltvd over gRPC (Unix socket by default) and drives
all disk and attachment operations.`,
	}
	root.PersistentFlags().StringVar(&rf.Addr, "addr", "unix:///run/sltv/sltvd.sock", "sltvd endpoint (unix:///path or host:port)")
	root.PersistentFlags().StringVar(&rf.TLSCert, "tls-cert", "", "client TLS certificate (required for mTLS TCP endpoints)")
	root.PersistentFlags().StringVar(&rf.TLSKey, "tls-key", "", "client TLS private key")
	root.PersistentFlags().StringVar(&rf.TLSCA, "tls-ca", "", "TLS CA bundle to verify the server")
	root.PersistentFlags().StringVar(&rf.TLSServer, "tls-server-name", "", "server name override for TLS verification")
	root.PersistentFlags().BoolVar(&rf.JSON, "json", false, "emit JSON instead of human-readable tables")
	root.PersistentFlags().DurationVar(&rf.Timeout, "timeout", 30*time.Second, "request timeout")

	root.AddCommand(
		newVersionCmd(),
		newCreateDiskCmd(rf),
		newDeleteDiskCmd(rf),
		newGetDiskCmd(rf),
		newListDisksCmd(rf),
		newExtendDiskCmd(rf),
		newAttachDiskCmd(rf),
		newDetachDiskCmd(rf),
		newListAttachmentsCmd(rf),
		newStatusCmd(rf),
	)
	return root
}

func newVersionCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "version",
		Short: "Print the sctl version",
		RunE: func(cmd *cobra.Command, _ []string) error {
			fmt.Fprintln(cmd.OutOrStdout(), version.String())
			return nil
		},
	}
}

func newCreateDiskCmd(rf *rootFlags) *cobra.Command {
	var (
		size        string
		thin        bool
		virtualSize string
		vg          string
	)
	cmd := &cobra.Command{
		Use:   "create-disk NAME",
		Short: "Provision a new disk",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx, cancel := withTimeout(cmd.Context(), rf.Timeout)
			defer cancel()
			c, err := dial(ctx, rf)
			if err != nil {
				return err
			}
			defer c.Close()
			req := &pb.CreateDiskRequest{Name: args[0], Vg: vg}
			if thin {
				req.Mode = pb.ProvisionMode_PROVISION_MODE_THIN
			} else {
				req.Mode = pb.ProvisionMode_PROVISION_MODE_THICK
			}
			if size != "" {
				n, err := config.ParseSize(size)
				if err != nil {
					return err
				}
				req.SizeBytes = n
			}
			if virtualSize != "" {
				n, err := config.ParseSize(virtualSize)
				if err != nil {
					return err
				}
				req.VirtualSizeBytes = n
			}
			d, err := c.CreateDisk(ctx, req)
			if err != nil {
				return err
			}
			return printDisk(cmd.OutOrStdout(), d, rf.JSON)
		},
	}
	cmd.Flags().StringVar(&size, "size", "", "physical size for thick disks, or initial LV size for thin (e.g. 50G)")
	cmd.Flags().BoolVar(&thin, "thin-prov", false, "create as thin-provisioned")
	cmd.Flags().StringVar(&virtualSize, "virtual-size", "", "guest-visible size for thin disks (e.g. 100G)")
	cmd.Flags().StringVar(&vg, "vg", "", "volume group (defaults to daemon-configured default)")
	return cmd
}

func newDeleteDiskCmd(rf *rootFlags) *cobra.Command {
	var force bool
	cmd := &cobra.Command{
		Use:   "delete-disk NAME",
		Short: "Delete a disk",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx, cancel := withTimeout(cmd.Context(), rf.Timeout)
			defer cancel()
			c, err := dial(ctx, rf)
			if err != nil {
				return err
			}
			defer c.Close()
			_, err = c.DeleteDisk(ctx, &pb.DeleteDiskRequest{Name: args[0], Force: force})
			return err
		},
	}
	cmd.Flags().BoolVar(&force, "force", false, "detach from any VMs first")
	return cmd
}

func newGetDiskCmd(rf *rootFlags) *cobra.Command {
	return &cobra.Command{
		Use:   "get-disk NAME",
		Short: "Show disk details",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx, cancel := withTimeout(cmd.Context(), rf.Timeout)
			defer cancel()
			c, err := dial(ctx, rf)
			if err != nil {
				return err
			}
			defer c.Close()
			d, err := c.GetDisk(ctx, &pb.GetDiskRequest{Name: args[0]})
			if err != nil {
				return err
			}
			return printDisk(cmd.OutOrStdout(), d, rf.JSON)
		},
	}
}

func newListDisksCmd(rf *rootFlags) *cobra.Command {
	var vg string
	cmd := &cobra.Command{
		Use:   "list-disks",
		Short: "List disks",
		RunE: func(cmd *cobra.Command, _ []string) error {
			ctx, cancel := withTimeout(cmd.Context(), rf.Timeout)
			defer cancel()
			c, err := dial(ctx, rf)
			if err != nil {
				return err
			}
			defer c.Close()
			r, err := c.ListDisks(ctx, &pb.ListDisksRequest{Vg: vg})
			if err != nil {
				return err
			}
			if rf.JSON {
				return printJSON(cmd.OutOrStdout(), r)
			}
			tw := tabwriter.NewWriter(cmd.OutOrStdout(), 0, 4, 2, ' ', 0)
			fmt.Fprintln(tw, "NAME\tVG\tMODE\tSIZE\tVIRT\tDEVICE")
			for _, d := range r.Disks {
				fmt.Fprintf(tw, "%s\t%s\t%s\t%s\t%s\t%s\n",
					d.Name, d.Vg, modeString(d.Mode),
					humanSize(d.SizeBytes), humanSize(d.VirtualSizeBytes), d.DevicePath)
			}
			return tw.Flush()
		},
	}
	cmd.Flags().StringVar(&vg, "vg", "", "filter by volume group")
	return cmd
}

func newExtendDiskCmd(rf *rootFlags) *cobra.Command {
	var (
		size  string
		delta string
	)
	cmd := &cobra.Command{
		Use:   "extend-disk NAME",
		Short: "Manually grow a disk",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx, cancel := withTimeout(cmd.Context(), rf.Timeout)
			defer cancel()
			c, err := dial(ctx, rf)
			if err != nil {
				return err
			}
			defer c.Close()
			req := &pb.ExtendDiskRequest{Name: args[0]}
			if size != "" {
				n, err := config.ParseSize(size)
				if err != nil {
					return err
				}
				req.SizeBytes = n
			}
			if delta != "" {
				trimmed := strings.TrimPrefix(delta, "+")
				n, err := config.ParseSize(trimmed)
				if err != nil {
					return err
				}
				req.DeltaBytes = n
			}
			if req.SizeBytes == 0 && req.DeltaBytes == 0 {
				return errors.New("--size or --delta is required")
			}
			d, err := c.ExtendDisk(ctx, req)
			if err != nil {
				return err
			}
			return printDisk(cmd.OutOrStdout(), d, rf.JSON)
		},
	}
	cmd.Flags().StringVar(&size, "size", "", "absolute new size (e.g. 100G)")
	cmd.Flags().StringVar(&delta, "delta", "", "relative growth (e.g. +10G)")
	return cmd
}

func newAttachDiskCmd(rf *rootFlags) *cobra.Command {
	var target string
	cmd := &cobra.Command{
		Use:   "attach-disk DISK VM",
		Short: "Attach a disk to a libvirt domain",
		Args:  cobra.ExactArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx, cancel := withTimeout(cmd.Context(), rf.Timeout)
			defer cancel()
			c, err := dial(ctx, rf)
			if err != nil {
				return err
			}
			defer c.Close()
			a, err := c.AttachDisk(ctx, &pb.AttachDiskRequest{
				DiskName:  args[0],
				VmName:    args[1],
				TargetDev: target,
			})
			if err != nil {
				return err
			}
			return printAttachment(cmd.OutOrStdout(), a, rf.JSON)
		},
	}
	cmd.Flags().StringVar(&target, "target", "", "guest target device (e.g. vdb)")
	return cmd
}

func newDetachDiskCmd(rf *rootFlags) *cobra.Command {
	return &cobra.Command{
		Use:   "detach-disk DISK VM",
		Short: "Detach a disk from a libvirt domain",
		Args:  cobra.ExactArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx, cancel := withTimeout(cmd.Context(), rf.Timeout)
			defer cancel()
			c, err := dial(ctx, rf)
			if err != nil {
				return err
			}
			defer c.Close()
			_, err = c.DetachDisk(ctx, &pb.DetachDiskRequest{DiskName: args[0], VmName: args[1]})
			return err
		},
	}
}

func newListAttachmentsCmd(rf *rootFlags) *cobra.Command {
	var disk, vm string
	cmd := &cobra.Command{
		Use:   "list-attachments",
		Short: "List disk attachments",
		RunE: func(cmd *cobra.Command, _ []string) error {
			ctx, cancel := withTimeout(cmd.Context(), rf.Timeout)
			defer cancel()
			c, err := dial(ctx, rf)
			if err != nil {
				return err
			}
			defer c.Close()
			r, err := c.ListAttachments(ctx, &pb.ListAttachmentsRequest{DiskName: disk, VmName: vm})
			if err != nil {
				return err
			}
			if rf.JSON {
				return printJSON(cmd.OutOrStdout(), r)
			}
			tw := tabwriter.NewWriter(cmd.OutOrStdout(), 0, 4, 2, ' ', 0)
			fmt.Fprintln(tw, "DISK\tVM\tHOST\tTARGET\tATTACHED_AT")
			for _, a := range r.Attachments {
				ts := ""
				if a.AttachedAt != nil {
					ts = a.AttachedAt.AsTime().UTC().Format(time.RFC3339)
				}
				fmt.Fprintf(tw, "%s\t%s\t%s\t%s\t%s\n", a.DiskName, a.VmName, a.Host, a.TargetDev, ts)
			}
			return tw.Flush()
		},
	}
	cmd.Flags().StringVar(&disk, "disk", "", "filter by disk")
	cmd.Flags().StringVar(&vm, "vm", "", "filter by VM")
	return cmd
}

func newStatusCmd(rf *rootFlags) *cobra.Command {
	var clusterFlag bool
	cmd := &cobra.Command{
		Use:   "status",
		Short: "Show node (and optionally cluster) status",
		RunE: func(cmd *cobra.Command, _ []string) error {
			ctx, cancel := withTimeout(cmd.Context(), rf.Timeout)
			defer cancel()
			c, err := dial(ctx, rf)
			if err != nil {
				return err
			}
			defer c.Close()
			ns, err := c.GetNodeStatus(ctx, nil)
			if err != nil {
				return err
			}
			if rf.JSON {
				out := map[string]any{"node": ns}
				if clusterFlag {
					cs, err := c.GetClusterStatus(ctx, nil)
					if err != nil {
						return err
					}
					out["cluster"] = cs
				}
				return printJSON(cmd.OutOrStdout(), out)
			}
			fmt.Fprintf(cmd.OutOrStdout(), "Node:    %s\n", ns.NodeId)
			fmt.Fprintf(cmd.OutOrStdout(), "Version: %s\n", ns.Version)
			fmt.Fprintf(cmd.OutOrStdout(), "Cluster: %v\n", ns.ClusterEnabled)
			tw := tabwriter.NewWriter(cmd.OutOrStdout(), 0, 4, 2, ' ', 0)
			fmt.Fprintln(tw, "VG\tSIZE\tFREE")
			for _, vg := range ns.Vgs {
				fmt.Fprintf(tw, "%s\t%s\t%s\n", vg.Name, humanSize(vg.SizeBytes), humanSize(vg.FreeBytes))
			}
			_ = tw.Flush()
			if clusterFlag {
				cs, err := c.GetClusterStatus(ctx, nil)
				if err != nil {
					return err
				}
				fmt.Fprintf(cmd.OutOrStdout(), "\nCluster status\n")
				fmt.Fprintf(cmd.OutOrStdout(), "  Enabled:        %v\n", cs.Enabled)
				fmt.Fprintf(cmd.OutOrStdout(), "  Leader:         %s\n", cs.Leader)
				fmt.Fprintf(cmd.OutOrStdout(), "  Disks:          %d\n", cs.DiskCount)
				fmt.Fprintf(cmd.OutOrStdout(), "  Attachments:    %d\n", cs.AttachmentCount)
				fmt.Fprintf(cmd.OutOrStdout(), "  Nodes:\n")
				for _, n := range cs.Nodes {
					fmt.Fprintf(cmd.OutOrStdout(), "    - %s (%s)\n", n.NodeId, n.Version)
				}
			}
			return nil
		},
	}
	cmd.Flags().BoolVar(&clusterFlag, "cluster", false, "include cluster status")
	return cmd
}

// --- helpers ---------------------------------------------------------------

func dial(ctx context.Context, rf *rootFlags) (*sltvclient.Client, error) {
	opts := sltvclient.Options{Address: rf.Addr, DialTimeout: rf.Timeout}
	if !strings.HasPrefix(rf.Addr, "unix://") && (rf.TLSCert != "" || rf.TLSCA != "") {
		tlsCfg, err := sltvclient.LoadClientTLS(rf.TLSCert, rf.TLSKey, rf.TLSCA, rf.TLSServer)
		if err != nil {
			return nil, err
		}
		opts.TLS = tlsCfg
	}
	return sltvclient.Dial(ctx, opts)
}

func withTimeout(ctx context.Context, d time.Duration) (context.Context, context.CancelFunc) {
	if ctx == nil {
		ctx = context.Background()
	}
	if d <= 0 {
		return ctx, func() {}
	}
	return context.WithTimeout(ctx, d)
}

func modeString(m pb.ProvisionMode) string {
	switch m {
	case pb.ProvisionMode_PROVISION_MODE_THICK:
		return "thick"
	case pb.ProvisionMode_PROVISION_MODE_THIN:
		return "thin"
	default:
		return "unknown"
	}
}

func humanSize(n uint64) string {
	const (
		k = 1024
		m = k * 1024
		g = m * 1024
		t = g * 1024
	)
	switch {
	case n >= t:
		return fmt.Sprintf("%.1fT", float64(n)/float64(t))
	case n >= g:
		return fmt.Sprintf("%.1fG", float64(n)/float64(g))
	case n >= m:
		return fmt.Sprintf("%.1fM", float64(n)/float64(m))
	case n >= k:
		return fmt.Sprintf("%.1fK", float64(n)/float64(k))
	default:
		return fmt.Sprintf("%dB", n)
	}
}

func printDisk(w io.Writer, d *pb.Disk, asJSON bool) error {
	if asJSON {
		return printJSON(w, d)
	}
	fmt.Fprintf(w, "Name:    %s\n", d.Name)
	fmt.Fprintf(w, "VG:      %s\n", d.Vg)
	fmt.Fprintf(w, "Mode:    %s\n", modeString(d.Mode))
	fmt.Fprintf(w, "Size:    %s (%d bytes)\n", humanSize(d.SizeBytes), d.SizeBytes)
	fmt.Fprintf(w, "Virtual: %s (%d bytes)\n", humanSize(d.VirtualSizeBytes), d.VirtualSizeBytes)
	fmt.Fprintf(w, "Device:  %s\n", d.DevicePath)
	fmt.Fprintf(w, "Node:    %s\n", d.CreatedOnNode)
	if d.CreatedAt != nil {
		fmt.Fprintf(w, "Created: %s\n", d.CreatedAt.AsTime().UTC().Format(time.RFC3339))
	}
	if d.LastExtendedAt != nil {
		fmt.Fprintf(w, "Extend:  %s\n", d.LastExtendedAt.AsTime().UTC().Format(time.RFC3339))
	}
	return nil
}

func printAttachment(w io.Writer, a *pb.Attachment, asJSON bool) error {
	if asJSON {
		return printJSON(w, a)
	}
	fmt.Fprintf(w, "Disk:   %s\n", a.DiskName)
	fmt.Fprintf(w, "VM:     %s\n", a.VmName)
	fmt.Fprintf(w, "Host:   %s\n", a.Host)
	fmt.Fprintf(w, "Target: %s\n", a.TargetDev)
	if a.AttachedAt != nil {
		fmt.Fprintf(w, "When:   %s\n", a.AttachedAt.AsTime().UTC().Format(time.RFC3339))
	}
	return nil
}

func printJSON(w io.Writer, v any) error {
	enc := json.NewEncoder(w)
	enc.SetIndent("", "  ")
	return enc.Encode(v)
}
