package main

import (
	"errors"
	"flag"
	"io"
)

// newFlagSet returns a flag.FlagSet with ContinueOnError so the caller can detect
// flag.ErrHelp and return 0 instead of letting the stdlib call os.Exit. The FlagSet
// writes its own error/usage messages to errOut.
func newFlagSet(name string, errOut io.Writer) *flag.FlagSet {
	fset := flag.NewFlagSet(name, flag.ContinueOnError)
	fset.SetOutput(errOut)
	return fset
}

// isHelpErr reports whether err is the sentinel returned by flag when --help/-h
// is passed to a FlagSet with ContinueOnError mode.
func isHelpErr(err error) bool {
	return errors.Is(err, flag.ErrHelp)
}
