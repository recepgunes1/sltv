package cli

import (
	"errors"
	"fmt"
	"strings"
	"text/tabwriter"

	pb "github.com/sltv/sltv/api/proto/v1"
	"github.com/sltv/sltv/internal/config"
	"github.com/spf13/cobra"
)

func (c *CLI) newCreateDiskCmd() *cobra.Command {
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
			ctx, cancel := withTimeout(cmd.Context(), c.rf.Timeout)
			defer cancel()
			client, err := dial(ctx, c.rf)
			if err != nil {
				return err
			}
			defer func() { _ = client.Close() }()
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
			d, err := client.CreateDisk(ctx, req)
			if err != nil {
				return err
			}
			return printDisk(cmd.OutOrStdout(), d, c.rf.JSON)
		},
	}
	cmd.Flags().StringVar(&size, "size", "", "physical size for thick disks, or initial LV size for thin (e.g. 50G)")
	cmd.Flags().BoolVar(&thin, "thin-prov", false, "create as thin-provisioned")
	cmd.Flags().StringVar(&virtualSize, "virtual-size", "", "guest-visible size for thin disks (e.g. 100G)")
	cmd.Flags().StringVar(&vg, "vg", "", "volume group (defaults to daemon-configured default)")
	return cmd
}

func (c *CLI) newDeleteDiskCmd() *cobra.Command {
	var force bool
	cmd := &cobra.Command{
		Use:   "delete-disk NAME",
		Short: "Delete a disk",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx, cancel := withTimeout(cmd.Context(), c.rf.Timeout)
			defer cancel()
			client, err := dial(ctx, c.rf)
			if err != nil {
				return err
			}
			defer func() { _ = client.Close() }()
			_, err = client.DeleteDisk(ctx, &pb.DeleteDiskRequest{Name: args[0], Force: force})
			return err
		},
	}
	cmd.Flags().BoolVar(&force, "force", false, "detach from any VMs first")
	return cmd
}

func (c *CLI) newGetDiskCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "get-disk NAME",
		Short: "Show disk details",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx, cancel := withTimeout(cmd.Context(), c.rf.Timeout)
			defer cancel()
			client, err := dial(ctx, c.rf)
			if err != nil {
				return err
			}
			defer func() { _ = client.Close() }()
			d, err := client.GetDisk(ctx, &pb.GetDiskRequest{Name: args[0]})
			if err != nil {
				return err
			}
			return printDisk(cmd.OutOrStdout(), d, c.rf.JSON)
		},
	}
}

func (c *CLI) newListDisksCmd() *cobra.Command {
	var vg string
	cmd := &cobra.Command{
		Use:   "list-disks",
		Short: "List disks",
		RunE: func(cmd *cobra.Command, _ []string) error {
			ctx, cancel := withTimeout(cmd.Context(), c.rf.Timeout)
			defer cancel()
			client, err := dial(ctx, c.rf)
			if err != nil {
				return err
			}
			defer func() { _ = client.Close() }()
			r, err := client.ListDisks(ctx, &pb.ListDisksRequest{Vg: vg})
			if err != nil {
				return err
			}
			if c.rf.JSON {
				return printJSON(cmd.OutOrStdout(), r)
			}
			tw := tabwriter.NewWriter(cmd.OutOrStdout(), 0, 4, 2, ' ', 0)
			_, _ = fmt.Fprintln(tw, "NAME\tVG\tMODE\tSIZE\tVIRT\tDEVICE")
			for _, d := range r.Disks {
				_, _ = fmt.Fprintf(tw, "%s\t%s\t%s\t%s\t%s\t%s\n",
					d.Name, d.Vg, modeString(d.Mode),
					humanSize(d.SizeBytes), humanSize(d.VirtualSizeBytes), d.DevicePath)
			}
			return tw.Flush()
		},
	}
	cmd.Flags().StringVar(&vg, "vg", "", "filter by volume group")
	return cmd
}

func (c *CLI) newExtendDiskCmd() *cobra.Command {
	var (
		size  string
		delta string
	)
	cmd := &cobra.Command{
		Use:   "extend-disk NAME",
		Short: "Manually grow a disk",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx, cancel := withTimeout(cmd.Context(), c.rf.Timeout)
			defer cancel()
			client, err := dial(ctx, c.rf)
			if err != nil {
				return err
			}
			defer func() { _ = client.Close() }()
			req := &pb.ExtendDiskRequest{Name: args[0]}
			if size != "" {
				n, err := config.ParseSize(size)
				if err != nil {
					return err
				}
				req.SizeBytes = n
			}
			if delta != "" {
				n, err := config.ParseSize(strings.TrimPrefix(delta, "+"))
				if err != nil {
					return err
				}
				req.DeltaBytes = n
			}
			if req.SizeBytes == 0 && req.DeltaBytes == 0 {
				return errors.New("--size or --delta is required")
			}
			d, err := client.ExtendDisk(ctx, req)
			if err != nil {
				return err
			}
			return printDisk(cmd.OutOrStdout(), d, c.rf.JSON)
		},
	}
	cmd.Flags().StringVar(&size, "size", "", "absolute new size (e.g. 100G)")
	cmd.Flags().StringVar(&delta, "delta", "", "relative growth (e.g. +10G)")
	return cmd
}
