// Package cli implements the sctl command-line interface.
package cli

import (
	"time"

	"github.com/spf13/cobra"
)

// rootFlags holds the persistent flags shared across all subcommands.
type rootFlags struct {
	Addr      string
	TLSCert   string
	TLSKey    string
	TLSCA     string
	TLSServer string
	JSON      bool
	Timeout   time.Duration
}

// CLI encapsulates the sctl command-line interface and its shared state.
type CLI struct {
	rf      *rootFlags
	rootCmd *cobra.Command
}

// New creates a new CLI instance and registers all subcommands.
func New() *CLI {
	c := &CLI{rf: &rootFlags{}}
	c.setupCommands()
	return c
}

func (c *CLI) setupCommands() {
	c.rootCmd = &cobra.Command{
		Use:           "sctl",
		Short:         "SLTV command-line client",
		SilenceUsage:  true,
		SilenceErrors: true,
		Long: `sctl is the command-line client for SLTV (Shared Logical Thin Volume).
It connects to sltvd over gRPC (Unix socket by default) and drives
all disk and attachment operations.`,
	}

	f := c.rootCmd.PersistentFlags()
	f.StringVar(&c.rf.Addr, "addr", "unix:///run/sltv/sltvd.sock", "sltvd endpoint (unix:///path or host:port)")
	f.StringVar(&c.rf.TLSCert, "tls-cert", "", "client TLS certificate (required for mTLS TCP endpoints)")
	f.StringVar(&c.rf.TLSKey, "tls-key", "", "client TLS private key")
	f.StringVar(&c.rf.TLSCA, "tls-ca", "", "TLS CA bundle to verify the server")
	f.StringVar(&c.rf.TLSServer, "tls-server-name", "", "server name override for TLS verification")
	f.BoolVar(&c.rf.JSON, "json", false, "emit JSON instead of human-readable tables")
	f.DurationVar(&c.rf.Timeout, "timeout", 30*time.Second, "request timeout")

	c.rootCmd.AddCommand(
		c.newVersionCmd(),
		c.newCreateDiskCmd(),
		c.newDeleteDiskCmd(),
		c.newGetDiskCmd(),
		c.newListDisksCmd(),
		c.newExtendDiskCmd(),
		c.newAttachDiskCmd(),
		c.newDetachDiskCmd(),
		c.newListAttachmentsCmd(),
		c.newStatusCmd(),
	)
}

// Run executes the CLI and returns any error.
func (c *CLI) Run() error {
	return c.rootCmd.Execute()
}

// RootCmd returns the root cobra command, used by tests.
func (c *CLI) RootCmd() *cobra.Command {
	return c.rootCmd
}
