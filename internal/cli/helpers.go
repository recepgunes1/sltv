package cli

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"strings"
	"time"

	pb "github.com/sltv/sltv/api/proto/v1"
	"github.com/sltv/sltv/pkg/sltvclient"
)

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

// errWriter collects the first write error so callers can return it once.
type errWriter struct {
	w   io.Writer
	err error
}

func (ew *errWriter) printf(format string, args ...any) {
	if ew.err == nil {
		_, ew.err = fmt.Fprintf(ew.w, format, args...)
	}
}

func printDisk(w io.Writer, d *pb.Disk, asJSON bool) error {
	if asJSON {
		return printJSON(w, d)
	}
	ew := &errWriter{w: w}
	ew.printf("Name:    %s\n", d.Name)
	ew.printf("VG:      %s\n", d.Vg)
	ew.printf("Mode:    %s\n", modeString(d.Mode))
	ew.printf("Size:    %s (%d bytes)\n", humanSize(d.SizeBytes), d.SizeBytes)
	ew.printf("Virtual: %s (%d bytes)\n", humanSize(d.VirtualSizeBytes), d.VirtualSizeBytes)
	ew.printf("Device:  %s\n", d.DevicePath)
	ew.printf("Node:    %s\n", d.CreatedOnNode)
	if d.CreatedAt != nil {
		ew.printf("Created: %s\n", d.CreatedAt.AsTime().UTC().Format(time.RFC3339))
	}
	if d.LastExtendedAt != nil {
		ew.printf("Extend:  %s\n", d.LastExtendedAt.AsTime().UTC().Format(time.RFC3339))
	}
	return ew.err
}

func printAttachment(w io.Writer, a *pb.Attachment, asJSON bool) error {
	if asJSON {
		return printJSON(w, a)
	}
	ew := &errWriter{w: w}
	ew.printf("Disk:   %s\n", a.DiskName)
	ew.printf("VM:     %s\n", a.VmName)
	ew.printf("Host:   %s\n", a.Host)
	ew.printf("Target: %s\n", a.TargetDev)
	if a.AttachedAt != nil {
		ew.printf("When:   %s\n", a.AttachedAt.AsTime().UTC().Format(time.RFC3339))
	}
	return ew.err
}

func printJSON(w io.Writer, v any) error {
	enc := json.NewEncoder(w)
	enc.SetIndent("", "  ")
	return enc.Encode(v)
}
