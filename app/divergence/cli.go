package divergence

import (
	"context"
	"fmt"
	"net/http"
	"time"

	"github.com/spf13/cobra"
)

const (
	flagBundle    = "bundle"
	flagReference = "reference"
	flagHeight    = "height"
	flagTimeout   = "timeout"
	flagTop       = "top"
)

// DiffCmd is `thornode debug divergence-diff`: the crash hook's reference
// diff, run offline against a bundle.
func DiffCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "divergence-diff",
		Short: "Diff an app-hash divergence bundle against a reference node",
		Long: `Reads a bundle written by the divergence crash hook
(<home>/divergence/bundle-<height>-<unix>/), fetches the reference node's
commit info and write set for the divergent height from its
/thorchain/debug endpoints, and writes diff.json into the bundle: the stores
whose commit hash differs and every key written differently. An existing
diff.json is replaced only by a diff with more evidence (or the same evidence
from the same reference); any other run, such as one after the reference
pruned the height, goes to diff-<height>-<unix>.json. A --height other than
the divergent one writes diff-<height>.json.`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			bundle, _ := cmd.Flags().GetString(flagBundle)
			reference, _ := cmd.Flags().GetString(flagReference)
			height, _ := cmd.Flags().GetInt64(flagHeight)
			timeout, _ := cmd.Flags().GetDuration(flagTimeout)
			top, _ := cmd.Flags().GetInt(flagTop)

			if reference == "" {
				s, err := ReadSummary(bundle)
				if err != nil {
					return err
				}
				reference = s.ReferenceAPI
			}
			if reference == "" {
				return fmt.Errorf("--%s is required (the bundle has no reference_api)", flagReference)
			}

			ctx, cancel := context.WithTimeout(cmd.Context(), timeout)
			defer cancel()
			d, path, err := RunReferenceDiff(ctx, &http.Client{Timeout: timeout}, bundle, reference, height)
			if err != nil {
				return err
			}
			printDiff(cmd, d, top)
			cmd.Printf("wrote %s\n", path)
			return nil
		},
	}
	cmd.Flags().String(flagBundle, "", "bundle directory")
	cmd.Flags().String(flagReference, "", "reference THORNode API base URL, e.g. http://host:1317 (default: the bundle's reference_api)")
	cmd.Flags().Int64(flagHeight, 0, "height to diff (default: the bundle's divergent height)")
	cmd.Flags().Duration(flagTimeout, 60*time.Second, "overall timeout")
	cmd.Flags().Int(flagTop, 20, "key diffs to print")
	_ = cmd.MarkFlagRequired(flagBundle)
	return cmd
}

func printDiff(cmd *cobra.Command, d *Diff, top int) {
	cmd.Printf("height %d: local app hash %s, reference %s (agree: %t)\n", d.Height, d.AppHash.Local, d.AppHash.Reference, d.AppHash.Agrees)
	if d.Previous != nil {
		cmd.Printf("height %d: agree: %t\n", d.Previous.Height, d.Previous.Agrees)
	}
	for _, s := range d.Stores {
		cmd.Printf("store %s differs: local %s reference %s\n", s.Name, s.Local, s.Reference)
	}
	if d.LocalResultsHash != "" || d.ReferenceResultsHash != "" {
		cmd.Printf("results hash: local %s reference %s\n", d.LocalResultsHash, d.ReferenceResultsHash)
	}
	for _, t := range d.TxResultDiffs {
		cmd.Printf("tx %d result differs: local %+v reference %+v\n", t.Index, t.Local, t.Reference)
	}
	cmd.Printf("key diffs: %d\n", d.TotalKeyDiffs)
	for i, k := range d.KeyDiffs {
		if i >= top {
			break
		}
		name := k.Key
		if k.Text != "" {
			name = k.Text
		}
		cmd.Printf("  %s %s %s [%s] local=%s reference=%s\n", k.Kind, k.Store, name, k.PrefixName, valueSummary(k.Local), valueSummary(k.Reference))
	}
	for _, n := range d.Notes {
		cmd.Printf("note: %s\n", n)
	}
}

func valueSummary(v ValueInfo) string {
	switch {
	case !v.Written:
		return "-"
	case v.Deleted:
		return "deleted"
	default:
		return fmt.Sprintf("%dB:%s", v.Len, v.SHA256)
	}
}
