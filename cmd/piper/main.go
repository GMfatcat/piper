package main

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/spf13/cobra"

	"github.com/GMfatcat/piper/internal/cli"
	"github.com/GMfatcat/piper/internal/scanner"
	"github.com/GMfatcat/piper/internal/service"
	"github.com/GMfatcat/piper/internal/version"
)

// makeHistoryDepsFactory returns a factory for cli.HistoryDeps.
// The returned factory is called inside RunE (after flags are parsed),
// so it captures cmd by pointer and reads flags lazily.
func makeHistoryDepsFactory(cmd **cobra.Command) func() cli.HistoryDeps {
	return func() cli.HistoryDeps {
		c := *cmd

		dataDir, _ := c.Flags().GetString("data-dir")
		dir, err := cli.ResolveDataDir(dataDir)
		if err != nil {
			// Fatal is appropriate here: we cannot proceed without a data dir.
			fmt.Fprintf(os.Stderr, "history: resolve data dir: %v\n", err)
			os.Exit(1)
		}
		st, err := cli.OpenStore(dir)
		if err != nil {
			fmt.Fprintf(os.Stderr, "history: open store: %v\n", err)
			os.Exit(1)
		}

		format, _ := c.Flags().GetString("format")
		output, _ := c.Flags().GetString("output")
		noColor, _ := c.Flags().GetBool("no-color")
		w := cli.NewWriter(cli.Format(format), output, noColor)

		return cli.HistoryDeps{
			Events: st,
			Out:    w,
		}
	}
}

// makeListDepsFactory returns a factory for cli.ListDeps.
// The context is read from the command at call time.
func makeListDepsFactory(cmd **cobra.Command) func() cli.ListDeps {
	return func() cli.ListDeps {
		c := *cmd
		ctx := c.Context()
		if ctx == nil {
			ctx = context.Background()
		}

		dataDir, _ := c.Flags().GetString("data-dir")
		dir, err := cli.ResolveDataDir(dataDir)
		if err != nil {
			fmt.Fprintf(os.Stderr, "list/scan: resolve data dir: %v\n", err)
			os.Exit(1)
		}
		st, err := cli.OpenStore(dir)
		if err != nil {
			fmt.Fprintf(os.Stderr, "list/scan: open store: %v\n", err)
			os.Exit(1)
		}

		snap, _, err := cli.ScanOnce(ctx)
		if err != nil {
			fmt.Fprintf(os.Stderr, "list/scan: scan: %v\n", err)
			os.Exit(1)
		}
		snapProvider := cli.InMemorySnapProvider{Snap: snap}

		format, _ := c.Flags().GetString("format")
		output, _ := c.Flags().GetString("output")
		noColor, _ := c.Flags().GetBool("no-color")
		w := cli.NewWriter(cli.Format(format), output, noColor)

		return cli.ListDeps{
			Snap:        snapProvider,
			Reserves:    st,
			AllReserves: st,
			Out:         w,
		}
	}
}

// makeServeDepsFactory returns a factory for cli.ServeDeps.
// Called inside RunE after flags are parsed.
func makeServeDepsFactory() func(host string, port int, interval time.Duration, dataDir string) cli.ServeDeps {
	return func(host string, port int, interval time.Duration, dataDir string) cli.ServeDeps {
		dir, err := cli.ResolveDataDir(dataDir)
		if err != nil {
			fmt.Fprintf(os.Stderr, "serve: resolve data dir: %v\n", err)
			os.Exit(1)
		}
		st, err := cli.OpenStore(dir)
		if err != nil {
			fmt.Fprintf(os.Stderr, "serve: open store: %v\n", err)
			os.Exit(1)
		}
		return cli.ServeDeps{
			DataDir:  dir,
			Store:    st,
			Scanner:  scanner.New(),
			Host:     host,
			Port:     port,
			Interval: interval,
			Logger:   slog.Default(),
			Clock:    service.RealClock{},
		}
	}
}

// makeRefreshDepsFactory returns a factory for cli.RefreshDeps.
func makeRefreshDepsFactory() func(host string, port int, dataDir string) cli.RefreshDeps {
	return func(host string, port int, dataDir string) cli.RefreshDeps {
		// Out writer for fallback output.
		w := cli.NewWriter(cli.FormatText, "", false)
		return cli.RefreshDeps{
			Host: host,
			Port: port,
			Out:  w,
			// Fallback is nil — RunRefresh will use the default ScanOnce path.
		}
	}
}

func main() {
	rootCmd := &cobra.Command{
		Use:     "piper",
		Short:   "piper — call once, all ports come marching",
		Long:    `Piper is a port-management tool that fuses ss, docker, and ufw into a single view.`,
		Version: version.Version,
	}

	rootCmd.PersistentFlags().String("data-dir", "", "data directory (default ~/.piper, override with PIPER_DATA_DIR)")
	rootCmd.PersistentFlags().String("format", "text", "output format: text | json")
	rootCmd.PersistentFlags().String("output", "", "tee output to FILE in addition to stdout")
	rootCmd.PersistentFlags().Bool("no-color", false, "disable ANSI color in text output")

	// Commands that need a depsFactory get a double-pointer so the factory
	// captures the concrete *cobra.Command after it is created.
	var historyCmd *cobra.Command
	var listCmd *cobra.Command
	var scanCmd *cobra.Command

	historyCmd = cli.NewHistoryCmd(makeHistoryDepsFactory(&historyCmd))
	listCmd = cli.NewListCmd(makeListDepsFactory(&listCmd))
	scanCmd = cli.NewScanCmd(makeListDepsFactory(&scanCmd))

	serveCmd := cli.NewServeCmd(makeServeDepsFactory())
	refreshCmd := cli.NewRefreshCmd(makeRefreshDepsFactory())

	rootCmd.AddCommand(
		cli.NewCheckCmd(),
		cli.NewSuggestCmd(),
		cli.NewReserveCmd(),
		cli.NewReleaseCmd(),
		historyCmd,
		listCmd,
		scanCmd,
		serveCmd,
		refreshCmd,
	)

	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer cancel()

	if err := rootCmd.ExecuteContext(ctx); err != nil {
		// cobra already prints the error; add a newline and exit non-zero.
		fmt.Fprintln(os.Stderr)
		os.Exit(1)
	}
}
