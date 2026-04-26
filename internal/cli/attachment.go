package cli

import (
	"fmt"
	"text/tabwriter"
	"time"

	pb "github.com/sltv/sltv/api/proto/v1"
	"github.com/spf13/cobra"
)

func (c *CLI) newAttachDiskCmd() *cobra.Command {
	var target string
	cmd := &cobra.Command{
		Use:   "attach-disk DISK VM",
		Short: "Attach a disk to a libvirt domain",
		Args:  cobra.ExactArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx, cancel := withTimeout(cmd.Context(), c.rf.Timeout)
			defer cancel()
			client, err := dial(ctx, c.rf)
			if err != nil {
				return err
			}
			defer func() { _ = client.Close() }()
			a, err := client.AttachDisk(ctx, &pb.AttachDiskRequest{
				DiskName:  args[0],
				VmName:    args[1],
				TargetDev: target,
			})
			if err != nil {
				return err
			}
			return printAttachment(cmd.OutOrStdout(), a, c.rf.JSON)
		},
	}
	cmd.Flags().StringVar(&target, "target", "", "guest target device (e.g. vdb)")
	return cmd
}

func (c *CLI) newDetachDiskCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "detach-disk DISK VM",
		Short: "Detach a disk from a libvirt domain",
		Args:  cobra.ExactArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx, cancel := withTimeout(cmd.Context(), c.rf.Timeout)
			defer cancel()
			client, err := dial(ctx, c.rf)
			if err != nil {
				return err
			}
			defer func() { _ = client.Close() }()
			_, err = client.DetachDisk(ctx, &pb.DetachDiskRequest{DiskName: args[0], VmName: args[1]})
			return err
		},
	}
}

func (c *CLI) newListAttachmentsCmd() *cobra.Command {
	var disk, vm string
	cmd := &cobra.Command{
		Use:   "list-attachments",
		Short: "List disk attachments",
		RunE: func(cmd *cobra.Command, _ []string) error {
			ctx, cancel := withTimeout(cmd.Context(), c.rf.Timeout)
			defer cancel()
			client, err := dial(ctx, c.rf)
			if err != nil {
				return err
			}
			defer func() { _ = client.Close() }()
			r, err := client.ListAttachments(ctx, &pb.ListAttachmentsRequest{DiskName: disk, VmName: vm})
			if err != nil {
				return err
			}
			if c.rf.JSON {
				return printJSON(cmd.OutOrStdout(), r)
			}
			tw := tabwriter.NewWriter(cmd.OutOrStdout(), 0, 4, 2, ' ', 0)
			_, _ = fmt.Fprintln(tw, "DISK\tVM\tHOST\tTARGET\tATTACHED_AT")
			for _, a := range r.Attachments {
				ts := ""
				if a.AttachedAt != nil {
					ts = a.AttachedAt.AsTime().UTC().Format(time.RFC3339)
				}
				_, _ = fmt.Fprintf(tw, "%s\t%s\t%s\t%s\t%s\n", a.DiskName, a.VmName, a.Host, a.TargetDev, ts)
			}
			return tw.Flush()
		},
	}
	cmd.Flags().StringVar(&disk, "disk", "", "filter by disk")
	cmd.Flags().StringVar(&vm, "vm", "", "filter by VM")
	return cmd
}
