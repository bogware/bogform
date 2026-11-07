// Copyright (c) The Bogform Authors
// SPDX-License-Identifier: MPL-2.0

package command

import (
	"encoding/json"
	"fmt"
	"io"
	"os"
	"strings"

	"github.com/bogware/bogform/internal/doctor"
)

// DoctorCommander builds the "doctor" command, which checks a configuration's repository setup.
func DoctorCommander() Command {
	cmd := Command{
		Name:  "doctor",
		Short: "Check this configuration's setup",
		Long: `Checks the configuration in the current directory, and the repository around it: that it loads, that its backend can be reached, that state is encrypted and the passphrase is available, that nothing has held the state lock for too long, that versions are pinned, that a pipeline runs a pinned Bogform, and that no secrets or email addresses are committed.

It reads files and asks the backend whether it can be reached, but it changes nothing, and it never shows secret values. It exits with 1 if it finds a problem, and 0 otherwise, including when there are only warnings.`,
	}
	var jsonOutput bool
	cmd.CommandLine.BoolVar(&jsonOutput, "json", false, "Print the results as JSON.")

	cmd.Run = func(meta Meta) int {
		ctx := meta.CommandContext()
		env, err := doctor.NewEnv(ctx, ".")
		if err != nil {
			fmt.Fprintf(os.Stderr, "Error: %s\n", err)
			return 1
		}
		results := doctor.Run(ctx, env)
		if jsonOutput {
			raw, err := json.MarshalIndent(results, "", "  ")
			if err != nil {
				fmt.Fprintf(os.Stderr, "Error: %s\n", err)
				return 1
			}
			fmt.Println(string(raw))
		} else {
			printDoctorResults(os.Stdout, env.Dir, results)
		}
		if doctor.HasProblems(results) {
			return 1
		}
		return 0
	}
	return cmd
}

func printDoctorResults(w io.Writer, dir string, results []doctor.Result) {
	fmt.Fprintf(w, "Checked %s\n\n", dir)
	counts := map[doctor.Status]int{}
	for _, r := range results {
		counts[r.Status]++
		fmt.Fprintf(w, "  %-8s %s\n", r.Status, r.Summary)
		if r.Fix != "" {
			fmt.Fprintf(w, "  %-8s Fix: %s\n", "", r.Fix)
		}
	}
	var summary []string
	for _, status := range []doctor.Status{doctor.StatusProblem, doctor.StatusWarning, doctor.StatusOK} {
		if n := counts[status]; n > 0 {
			label := string(status)
			if n > 1 && status != doctor.StatusOK {
				label += "s"
			}
			summary = append(summary, fmt.Sprintf("%d %s", n, label))
		}
	}
	fmt.Fprintf(w, "\n%s.\n", strings.Join(summary, ", "))
}
