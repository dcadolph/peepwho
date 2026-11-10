package cmd

import (
	"encoding/xml"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"

	"github.com/spf13/cobra"

	"github.com/dcadolph/whotoask/internal/util"
)

// scheduleLabel is the launchd label for the weekly refresh agent.
const scheduleLabel = "app.whotoask.refresh"

// launchdPlist renders the launchd agent that runs `whotoask refresh` weekly. It
// is a pure function so its output can be tested without touching launchctl.
// Every interpolated value is XML-escaped: a path holding '&' is a normal
// path, and unescaped it produces an invalid plist whose only symptom is a
// cryptic launchctl failure.
func launchdPlist(label, exe, dataDir, logPath string) string {
	return fmt.Sprintf(`<?xml version="1.0" encoding="UTF-8"?>
<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd">
<plist version="1.0">
<dict>
	<key>Label</key>
	<string>%s</string>
	<key>ProgramArguments</key>
	<array>
		<string>%s</string>
		<string>refresh</string>
		<string>--data-dir</string>
		<string>%s</string>
	</array>
	<key>StartCalendarInterval</key>
	<dict>
		<key>Weekday</key>
		<integer>0</integer>
		<key>Hour</key>
		<integer>3</integer>
		<key>Minute</key>
		<integer>0</integer>
	</dict>
	<key>StandardErrorPath</key>
	<string>%s</string>
	<key>StandardOutPath</key>
	<string>%s</string>
</dict>
</plist>
`, xmlEscape(label), xmlEscape(exe), xmlEscape(dataDir), xmlEscape(logPath), xmlEscape(logPath))
}

// xmlEscape returns s with XML-special characters escaped for a plist string.
func xmlEscape(s string) string {
	var b strings.Builder
	// EscapeText on a strings.Builder cannot fail.
	_ = xml.EscapeText(&b, []byte(s))
	return b.String()
}

// schedulePlistPath returns the launchd agent path in the user's home.
func schedulePlistPath() (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("schedule: home dir: %w", err)
	}
	return filepath.Join(home, "Library", "LaunchAgents", scheduleLabel+".plist"), nil
}

// newScheduleCmd builds the schedule command, which installs, removes, or checks
// a launchd agent that runs a weekly refresh. It is macOS only.
func newScheduleCmd(opts *options) *cobra.Command {
	var install, remove, status bool
	cmd := &cobra.Command{
		Use:   "schedule",
		Short: "Install, remove, or check a weekly launchd refresh (macOS)",
		Long: `Install a launchd agent that runs "whotoask refresh" every Sunday at 3am, so the
graph stays current without remembering to. macOS only; on other systems run
"whotoask refresh" from cron.

  whotoask schedule --install
  whotoask schedule --status
  whotoask schedule --remove`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			if runtime.GOOS != "darwin" {
				return fmt.Errorf("%w: schedule is macOS only; run `whotoask refresh` from cron elsewhere", ErrBadArgs)
			}
			if countTrue(install, remove, status) != 1 {
				return fmt.Errorf("%w: pass exactly one of --install, --remove, or --status", ErrBadArgs)
			}
			plistPath, err := schedulePlistPath()
			if err != nil {
				return err
			}
			switch {
			case install:
				return scheduleInstall(cmd, opts, plistPath)
			case remove:
				return scheduleRemove(cmd, plistPath)
			default:
				return scheduleStatus(cmd, plistPath)
			}
		},
	}
	f := cmd.Flags()
	f.BoolVar(&install, "install", false, "Install the weekly refresh agent.")
	f.BoolVar(&remove, "remove", false, "Remove the refresh agent.")
	f.BoolVar(&status, "status", false, "Report whether the agent is installed.")
	return cmd
}

// countTrue returns how many of the booleans are true.
func countTrue(bs ...bool) int {
	n := 0
	for _, b := range bs {
		if b {
			n++
		}
	}
	return n
}

// scheduleInstall writes the agent and loads it, replacing any prior copy.
func scheduleInstall(cmd *cobra.Command, opts *options, plistPath string) error {
	exe, err := os.Executable()
	if err != nil {
		return fmt.Errorf("schedule: locate binary: %w", err)
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return fmt.Errorf("schedule: home dir: %w", err)
	}
	// launchd agents run with cwd "/", so a relative --data-dir written as
	// given would point the weekly refresh at a directory that does not exist
	// while --status keeps reporting the agent installed.
	dataDir, err := filepath.Abs(opts.dataDir)
	if err != nil {
		return fmt.Errorf("schedule: resolve data dir: %w", err)
	}
	logPath := filepath.Join(home, "Library", "Logs", "whotoask-refresh.log")
	if err := os.MkdirAll(filepath.Dir(plistPath), 0o755); err != nil {
		return fmt.Errorf("schedule: create LaunchAgents dir: %w", err)
	}
	plist := []byte(launchdPlist(scheduleLabel, exe, dataDir, logPath))
	if err := util.WriteFileAtomic(plistPath, plist, 0o644); err != nil {
		return fmt.Errorf("schedule: write agent: %w", err)
	}
	// Unload any prior copy so load does not fail on a duplicate label.
	_ = exec.CommandContext(cmd.Context(), "launchctl", "unload", plistPath).Run()
	if out, err := exec.CommandContext(cmd.Context(), "launchctl", "load", "-w", plistPath).CombinedOutput(); err != nil {
		return fmt.Errorf("schedule: launchctl load: %w: %s", err, out)
	}
	fmt.Fprintf(cmd.ErrOrStderr(), "installed weekly refresh (Sunday 3am): %s\n", plistPath)
	return nil
}

// scheduleRemove unloads and deletes the agent.
func scheduleRemove(cmd *cobra.Command, plistPath string) error {
	if _, err := os.Stat(plistPath); errors.Is(err, os.ErrNotExist) {
		fmt.Fprintln(cmd.ErrOrStderr(), "no refresh agent installed")
		return nil
	}
	_ = exec.CommandContext(cmd.Context(), "launchctl", "unload", plistPath).Run()
	if err := os.Remove(plistPath); err != nil {
		return fmt.Errorf("schedule: remove agent: %w", err)
	}
	fmt.Fprintln(cmd.ErrOrStderr(), "removed refresh agent")
	return nil
}

// scheduleStatus reports whether the agent is installed.
func scheduleStatus(cmd *cobra.Command, plistPath string) error {
	if _, err := os.Stat(plistPath); errors.Is(err, os.ErrNotExist) {
		fmt.Fprintln(cmd.OutOrStdout(), "not installed")
		return nil
	}
	fmt.Fprintf(cmd.OutOrStdout(), "installed: %s\n", plistPath)
	return nil
}
