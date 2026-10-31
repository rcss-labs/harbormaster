package main

import (
	"fmt"
	"io"
)

func printVersion(w io.Writer, buildVersion, buildCommit string) {
	fmt.Fprintf(w, "harbormaster %s (%s)\n", buildVersion, buildCommit)
}
