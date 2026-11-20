package main

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"github.com/marshal-org/factorymeter/pkg/upgrade"
)

func upgradeCmd() *cobra.Command {
	var check, force bool
	var to string
	cmd := &cobra.Command{
		Use:   "upgrade",
		Short: "Replace this factorymeter with the newest release",
		Long: `upgrade downloads the newest release of factorymeter for this system, checks
it against the checksums of that release, and puts it in place of the binary
you ran. A failure leaves the old binary as it was.

It asks github.com which release is the newest and downloads two files from
it: the binary and checksums.txt. It needs no token and sends nothing else.
--check only says whether there is a newer release. --version picks a release,
also an older one.

A binary that "go install" built is replaced as well. To keep building from
source instead, run "go install github.com/marshal-org/factorymeter/cmd/factorymeter@latest".`,
		Example: `  factorymeter upgrade
  factorymeter upgrade --check
  factorymeter upgrade --version v0.4.0`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			w := cmd.OutOrStdout()
			base := upgrade.DefaultBase
			if v := strings.TrimSpace(os.Getenv("FACTORYMETER_RELEASES_URL")); v != "" {
				base = v
			}
			src := upgrade.New(base)
			src.UserAgent = "factorymeter/" + version
			ctx, cancel := context.WithTimeout(cmd.Context(), 10*time.Minute)
			defer cancel()

			target := strings.TrimSpace(to)
			if target != "" && !upgrade.Valid(target) {
				if upgrade.Valid("v" + target) {
					target = "v" + target
				} else {
					return fmt.Errorf("--version takes a release, such as v0.5.0, not %q", to)
				}
			}
			latest := target == ""
			if latest {
				t, err := src.Latest(ctx)
				if err != nil {
					return err
				}
				target = t
			}
			notes := base + "/releases/tag/" + target
			switch {
			case target == version && !force:
				fmt.Fprintf(w, "factorymeter %s is the %s. Nothing to do.\n", version, map[bool]string{true: "newest release", false: "release asked for"}[latest])
				return nil
			case check:
				if upgrade.Newer(target, version) {
					fmt.Fprintf(w, "factorymeter %s is out. This is %s. Run \"factorymeter upgrade\".\nWhat changed: %s\n", target, version, notes)
				} else {
					fmt.Fprintf(w, "factorymeter %s is the newest release. This is %s.\n", target, version)
				}
				return nil
			case latest && !upgrade.Valid(version) && !force:
				return fmt.Errorf("this factorymeter is %s, built from source, not a release. \"factorymeter upgrade --force\" replaces it with %s", version, target)
			case latest && upgrade.Newer(version, target) && !force:
				fmt.Fprintf(w, "factorymeter %s is newer than the newest release, %s. Nothing to do.\n", version, target)
				return nil
			}

			self, err := os.Executable()
			if err != nil {
				return fmt.Errorf("cannot find this binary: %w", err)
			}
			if r, err := filepath.EvalSymlinks(self); err == nil {
				self = r
			}
			asset, err := upgrade.Asset(runtime.GOOS, runtime.GOARCH)
			if err != nil {
				return err
			}
			data, err := src.Fetch(ctx, target, asset)
			if err != nil {
				return err
			}
			fmt.Fprintf(w, "Downloaded %s of %s (%.1f MB). It matches the checksum of the release.\n", asset, target, float64(len(data))/(1<<20))

			// The new binary runs once before it is put in place, so a file
			// that cannot run on this system never replaces one that can.
			tmp, err := upgrade.TempCopy(data, runtime.GOOS == "windows")
			if err != nil {
				return err
			}
			defer os.Remove(tmp)
			out, err := exec.CommandContext(ctx, tmp, "--version").CombinedOutput()
			if err != nil || !strings.Contains(string(out), target) {
				return fmt.Errorf("the downloaded binary did not run as %s on this system (%v: %s). Nothing was changed", target, err, strings.TrimSpace(string(out)))
			}
			upgrade.CleanOld(self)
			if err := upgrade.Replace(self, data, runtime.GOOS == "windows"); err != nil {
				return err
			}
			fmt.Fprintf(w, "Replaced %s: %s, was %s.\nWhat changed: %s\n", self, target, version, notes)
			return nil
		},
	}
	f := cmd.Flags()
	f.BoolVar(&check, "check", false, "only say whether there is a newer release")
	f.StringVar(&to, "version", "", "upgrade to this release, such as v0.5.0, also an older one")
	f.BoolVar(&force, "force", false, "replace the binary also when it is the same release or built from source")
	return cmd
}
