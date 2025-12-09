// Everything you need to develop the Dagger Engine
// https://dagger.io
package main

import (
	"context"
	"errors"
	"fmt"
	"os"

	"github.com/dagger/dagger/util/patchpreview"
)

// A dev environment for the DaggerDev Engine
type DaggerDev struct{}

// Verify that generated code is up to date
// +check
func (dev *DaggerDev) Generated(ctx context.Context) error {
	generators := dag.CurrentModule().Generators().Run()
	if empty, err := generators.IsEmpty(ctx); err != nil {
		return err
	} else if !empty {
		cs := generators.Changes()
		rawPatch, err := cs.AsPatch().Contents(ctx)
		if err != nil {
			return err
		}
		out, err := patchpreview.SummarizeString(ctx, rawPatch, cs)
		if err != nil {
			return err
		}
		fmt.Fprint(os.Stderr, out)
		return errors.New("generated files are not up-to-date")
	}
	return nil
}
