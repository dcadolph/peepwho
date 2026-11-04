package cmd

import (
	"fmt"
	"runtime/debug"
	"strings"

	"github.com/spf13/cobra"
)

// version is the build version, overridden via -ldflags at release time.
var version = "dev"

// init fills the version in for a binary that got no ldflags, which is what
// `go install` produces: the toolchain records the module version it built,
// and reporting "dev" there makes a perfectly good install look unfinished.
// The linker's value always wins, so a release binary is unaffected: this only
// ever replaces the default.
func init() { version = resolveVersion(version, moduleVersion()) }

// resolveVersion picks the version to report: the linker's value when there is
// one, otherwise the module version, otherwise the default.
func resolveVersion(stamped, module string) string {
	if stamped != "dev" || module == "" {
		return stamped
	}
	return module
}

// moduleVersion returns the module version this binary was built from, or the
// empty string when there is none, which is the case for a build from a local
// working directory.
func moduleVersion() string {
	info, ok := debug.ReadBuildInfo()
	if !ok {
		return ""
	}
	v := strings.TrimPrefix(info.Main.Version, "v")
	if v == "" || strings.Contains(v, "devel") {
		return ""
	}
	return v
}

// newVersionCmd builds the version command.
func newVersionCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "version",
		Short: "Print the peepwho version",
		RunE: func(cmd *cobra.Command, _ []string) error {
			_, err := fmt.Fprintln(cmd.OutOrStdout(), "peepwho", version)
			return err
		},
	}
}
