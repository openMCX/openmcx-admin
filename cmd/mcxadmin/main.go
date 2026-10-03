// Command mcxadmin works on MCX Admin configuration documents (docs/SLICE_A.md):
//
//	mcxadmin import <site.json>                      document on stdout, report on stderr
//	mcxadmin validate <config.json>                  exit 1 with the problems
//	mcxadmin diff <before.json> <after.json>         the change preview
//	mcxadmin to-site <config.json> <base site.json>  users and groups as the servers read them
//	mcxadmin template <config.json> <out.xlsx|dir>   the spreadsheet to fill in (XLSX, or users.csv and groups.csv)
//	mcxadmin sheet <config.json> <upload.xlsx | users.csv [groups.csv]> [report dir]
//	                                                 the new document; with row errors, the sheets back with an error column
//
// openMCX administration (no 3GPP specification).
package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/openMCX/openmcx-admin/internal/closer"
	"github.com/openMCX/openmcx-admin/internal/config"
	"github.com/openMCX/openmcx-admin/internal/sheet"
)

// ------------------------------------------------------------------------------
// main runs the subcommand named on the command line and exits 1 with its error.
func main() {
	if err := run(os.Args[1:]); err != nil {
		fmt.Fprintln(os.Stderr, "mcxadmin:", err)
		os.Exit(1)
	}
}

// command is one subcommand: its usage line, the number of arguments it takes (minimum and maximum, -1 for no
// maximum) and what it does. Documents go to stdout, reports and problems to stderr.
type command struct {
	usage    string
	min, max int
	run      func(args []string, out *json.Encoder) error
}

// commands are the subcommands, in the order the usage lists them.
var commands = []struct {
	name string
	command
}{
	{"import", command{"<site.json>", 1, 1, importCommand}},
	{"validate", command{"<config.json>", 1, 1, validateCommand}},
	{"diff", command{"<before.json> <after.json>", 2, 2, diffCommand}},
	{"to-site", command{"<config.json> <base site.json>", 2, 2, toSiteCommand}},
	{"template", command{"<config.json> <out.xlsx|dir>", 2, 2, templateCommand}},
	{"sheet", command{"<config.json> <upload.xlsx | users.csv [groups.csv]> [report dir]", 2, -1, runSheet}},
}

// ------------------------------------------------------------------------------
// run dispatches a subcommand (see commands); without one, or with a wrong number of arguments, it returns the
// usage.
func run(args []string) error {
	out := json.NewEncoder(os.Stdout)
	out.SetIndent("", "  ")
	if len(args) == 0 {
		lines := make([]string, 0, len(commands))
		for _, entry := range commands {
			lines = append(lines, "  mcxadmin "+entry.name+" "+entry.usage)
		}
		return fmt.Errorf("usage:\n%s", strings.Join(lines, "\n"))
	}
	for _, entry := range commands {
		if entry.name != args[0] {
			continue
		}
		if count := len(args) - 1; count < entry.min || entry.max >= 0 && count > entry.max {
			return fmt.Errorf("usage: mcxadmin %s %s", entry.name, entry.usage)
		}
		return entry.run(args[1:], out)
	}
	return fmt.Errorf("unknown command %q", args[0])
}

// ------------------------------------------------------------------------------
// importCommand turns a site.json into a configuration document: the document on stdout, the import report on
// stderr.
func importCommand(args []string, out *json.Encoder) error {
	raw, err := config.ReadFile(args[0])
	if err != nil {
		return err
	}
	doc, report, err := config.ImportSite(raw)
	if err != nil {
		return err
	}
	if problems := doc.Validate(); len(problems) > 0 {
		return problemsError(problems)
	}
	summary, err := json.MarshalIndent(report, "", "  ")
	if err != nil {
		return err
	}
	fmt.Fprintln(os.Stderr, string(summary))
	return out.Encode(doc)
}

// ------------------------------------------------------------------------------
// validateCommand checks a configuration document and prints its size.
func validateCommand(args []string, _ *json.Encoder) error {
	doc, err := config.Load(args[0])
	if err != nil {
		return err
	}
	if problems := doc.Validate(); len(problems) > 0 {
		return problemsError(problems)
	}
	fmt.Printf("valid: %d users, %d groups, %d templates\n", len(doc.Users), len(doc.Groups), len(doc.Templates))
	return nil
}

// ------------------------------------------------------------------------------
// diffCommand prints the changes from one document to another.
func diffCommand(args []string, _ *json.Encoder) error {
	before, err := config.Load(args[0])
	if err != nil {
		return err
	}
	after, err := config.Load(args[1])
	if err != nil {
		return err
	}
	for _, change := range config.Diff(before, after) {
		fmt.Println(change)
	}
	return nil
}

// ------------------------------------------------------------------------------
// toSiteCommand gives a document's users and groups in the lab's site.json form, against a base site.json.
func toSiteCommand(args []string, out *json.Encoder) error {
	doc, err := config.Load(args[0])
	if err != nil {
		return err
	}
	base, err := config.ReadFile(args[1])
	if err != nil {
		return err
	}
	users, groups, err := doc.ToSiteUsers(base)
	if err != nil {
		return err
	}
	return out.Encode(map[string]any{"users": users, "groups": groups})
}

// ------------------------------------------------------------------------------
// templateCommand writes the bulk-upload template: a workbook (.xlsx) or a directory with users.csv and
// groups.csv.
func templateCommand(args []string, _ *json.Encoder) error {
	doc, err := config.Load(args[0])
	if err != nil {
		return err
	}
	target := args[1]
	if strings.HasSuffix(strings.ToLower(target), ".xlsx") {
		raw, err := sheet.TemplateXLSX(doc)
		if err != nil {
			return err
		}
		return os.WriteFile(target, raw, 0o644)
	}
	if err := os.MkdirAll(target, 0o755); err != nil {
		return err
	}
	for name, columns := range map[string][]sheet.Column{"users.csv": sheet.UserColumns, "groups.csv": sheet.GroupColumns} {
		raw, err := sheet.TemplateCSV(columns)
		if err == nil {
			err = os.WriteFile(filepath.Join(target, name), raw, 0o644)
		}
		if err != nil {
			return err
		}
	}
	return nil
}

// ------------------------------------------------------------------------------
// problemsError prints each validation problem on stderr and returns an error that counts them.
func problemsError(problems []config.Problem) error {
	for _, problem := range problems {
		fmt.Fprintln(os.Stderr, problem)
	}
	return fmt.Errorf("%d problems", len(problems))
}

// ------------------------------------------------------------------------------
// runSheet applies an uploaded spreadsheet to a document as the API does (sheet.Prepare): the new document on
// stdout and the change preview on stderr, or, with problems, the sheets back with an error column in the report
// directory and exit 1. Nothing is applied with problems.
func runSheet(args []string, out *json.Encoder) error {
	doc, err := config.Load(args[0])
	if err != nil {
		return err
	}
	users, groups, rest, err := readSheets(args[1], args[2:])
	if err != nil {
		return err
	}
	res := sheet.Prepare(doc, users, groups)
	if len(res.Problems) > 0 {
		for _, problem := range res.Problems {
			fmt.Fprintln(os.Stderr, problem)
		}
		if len(rest) > 0 {
			if err := writeReports(rest[0], users, groups, res.Problems); err != nil {
				return err
			}
		}
		return fmt.Errorf("%d problems in %d user and %d group rows; nothing applied", len(res.Problems), res.Users, res.Groups)
	}
	changes := config.Diff(doc, res.Document)
	fmt.Fprintf(os.Stderr, "%d user and %d group rows, %d changes:\n", res.Users, res.Groups, len(changes))
	for _, change := range changes {
		fmt.Fprintln(os.Stderr, change)
	}
	return out.Encode(res.Document)
}

// ------------------------------------------------------------------------------
// readSheets reads the rows of an upload: a workbook, or a users CSV followed, in rest, by an optional groups
// CSV; it returns the arguments left (the report directory).
func readSheets(upload string, rest []string) (users, groups []sheet.Row, left []string, err error) {
	if strings.HasSuffix(strings.ToLower(upload), ".xlsx") {
		raw, err := os.ReadFile(upload)
		if err != nil {
			return nil, nil, nil, err
		}
		users, groups, err = sheet.ReadXLSX(bytes.NewReader(raw))
		return users, groups, rest, err
	}
	read := func(path string, columns []sheet.Column) ([]sheet.Row, error) {
		file, err := os.Open(path)
		if err != nil {
			return nil, err
		}
		defer closer.Quietly(path, file)
		return sheet.ReadCSV(file, columns)
	}
	if users, err = read(upload, sheet.UserColumns); err != nil {
		return nil, nil, nil, err
	}
	if len(rest) > 0 && strings.HasSuffix(strings.ToLower(rest[0]), ".csv") {
		if groups, err = read(rest[0], sheet.GroupColumns); err != nil {
			return nil, nil, nil, err
		}
		rest = rest[1:]
	}
	return users, groups, rest, nil
}

// ------------------------------------------------------------------------------
// writeReports writes users-report.csv and groups-report.csv (sheet.ReportCSV) into a directory.
func writeReports(directory string, users, groups []sheet.Row, problems []sheet.Problem) error {
	if err := os.MkdirAll(directory, 0o755); err != nil {
		return err
	}
	for _, report := range []struct {
		name    string
		columns []sheet.Column
		rows    []sheet.Row
	}{{sheet.UsersSheet, sheet.UserColumns, users}, {sheet.GroupsSheet, sheet.GroupColumns, groups}} {
		raw, err := sheet.ReportCSV(report.columns, report.rows, report.name, problems)
		if err == nil {
			err = os.WriteFile(filepath.Join(directory, report.name+"-report.csv"), raw, 0o644)
		}
		if err != nil {
			return err
		}
	}
	return nil
}
