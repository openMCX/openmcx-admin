// The XLSX workbook (read and template) and the CSV template and error report of the bulk upload.
//
// openMCX administration (no 3GPP specification).
package sheet

import (
	"bytes"
	"encoding/csv"
	"encoding/json"
	"fmt"
	"io"
	"sort"
	"strings"

	"github.com/xuri/excelize/v2"

	"github.com/openMCX/openmcx-admin/internal/closer"
	"github.com/openMCX/openmcx-admin/internal/config"
)

// Sheet names of the XLSX workbook.
const (
	UsersSheet  = "users"
	GroupsSheet = "groups"
	HelpSheet   = "help"
	// DocumentSheet names the problems of the whole document (Validate after Apply), which have no row.
	DocumentSheet = "document"
	// ReportColumn is the column ReportCSV adds; an upload ignores it.
	ReportColumn = "error"
)

// ------------------------------------------------------------------------------
// ReadXLSX reads the users and groups sheets of a workbook. A missing groups
// sheet means no group rows.
func ReadXLSX(reader io.Reader) (users, groups []Row, err error) {
	book, err := excelize.OpenReader(reader)
	if err != nil {
		return nil, nil, fmt.Errorf("workbook: %w", err)
	}
	defer closer.Quietly("workbook", book)
	read := func(name string, columns []Column, required bool) ([]Row, error) {
		if idx, _ := book.GetSheetIndex(name); idx < 0 {
			if required {
				return nil, fmt.Errorf("the workbook has no %q sheet", name)
			}
			return nil, nil
		}
		records, err := book.GetRows(name)
		if err != nil {
			return nil, err
		}
		rows, err := rowsOf(records, columns)
		if err != nil {
			return nil, fmt.Errorf("sheet %q: %w", name, err)
		}
		return rows, nil
	}
	if users, err = read(UsersSheet, UserColumns, true); err != nil {
		return nil, nil, err
	}
	if groups, err = read(GroupsSheet, GroupColumns, false); err != nil {
		return nil, nil, err
	}
	return users, groups, nil
}

// ------------------------------------------------------------------------------
// TemplateXLSX is the workbook an administrator downloads: the users and groups
// sheets with their header rows, and a help sheet naming each column and the
// templates of the system.
func TemplateXLSX(doc *config.Document) ([]byte, error) {
	book := excelize.NewFile()
	defer closer.Quietly("workbook", book)
	if err := book.SetSheetName("Sheet1", UsersSheet); err != nil {
		return nil, err
	}
	for _, name := range []string{GroupsSheet, HelpSheet} {
		if _, err := book.NewSheet(name); err != nil {
			return nil, err
		}
	}
	sheets := map[string][][]string{UsersSheet: {headerNames(UserColumns)}, GroupsSheet: {headerNames(GroupColumns)},
		HelpSheet: helpRows(doc)}
	for _, name := range []string{UsersSheet, GroupsSheet, HelpSheet} {
		if err := writeRows(book, name, sheets[name]); err != nil {
			return nil, fmt.Errorf("sheet %q: %w", name, err)
		}
	}
	var buf bytes.Buffer
	if err := book.Write(&buf); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}

// ------------------------------------------------------------------------------
// headerNames is a sheet's header row: the column names, a required one marked *.
func headerNames(columns []Column) []string {
	names := make([]string, len(columns))
	for index, column := range columns {
		names[index] = column.Name
		if column.Required {
			names[index] += "*"
		}
	}
	return names
}

// ------------------------------------------------------------------------------
// helpRows is the help sheet: the rules, each column of both sheets with its help, and the system's templates.
func helpRows(doc *config.Document) [][]string {
	rows := [][]string{{"openMCX Admin bulk upload", "", "A column marked * is required. Lists are separated by ;."}, nil}
	for _, sheet := range []struct {
		name    string
		columns []Column
	}{{UsersSheet, UserColumns}, {GroupsSheet, GroupColumns}} {
		for _, column := range sheet.columns {
			rows = append(rows, []string{sheet.name, column.Name, column.Help})
		}
	}
	rows = append(rows, nil, []string{"templates of " + doc.System.Domain})
	names := make([]string, 0, len(doc.Templates))
	for name := range doc.Templates {
		names = append(names, name)
	}
	sort.Strings(names)
	for _, name := range names {
		rows = append(rows, []string{"", name})
	}
	return rows
}

// ------------------------------------------------------------------------------
// writeRows writes rows into a sheet from cell A1; a nil row is left empty.
func writeRows(book *excelize.File, sheet string, rows [][]string) error {
	for rowIndex, values := range rows {
		for columnIndex, value := range values {
			cell, err := excelize.CoordinatesToCellName(columnIndex+1, rowIndex+1)
			if err != nil {
				return err
			}
			if err := book.SetCellValue(sheet, cell, value); err != nil {
				return err
			}
		}
	}
	return nil
}

// ------------------------------------------------------------------------------
// TemplateCSV is the header row of one CSV sheet.
func TemplateCSV(columns []Column) ([]byte, error) {
	var buf bytes.Buffer
	out := csv.NewWriter(&buf)
	if err := out.Write(headerNames(columns)); err != nil {
		return nil, err
	}
	out.Flush()
	return buf.Bytes(), out.Error()
}

// ------------------------------------------------------------------------------
// ReportCSV gives a sheet back with an "error" column, empty on good rows, so that it can be fixed and uploaded
// again (the error column is ignored on upload). Problems of the whole document (sheet "document", no line) are
// given on the users report, in rows of their own after the data, with only the error column filled.
func ReportCSV(columns []Column, rows []Row, sheetName string, problems []Problem) ([]byte, error) {
	byLine := map[int][]string{}
	var documentProblems []string
	for _, problem := range problems {
		switch {
		case problem.Sheet == sheetName:
			byLine[problem.Line] = append(byLine[problem.Line], problem.Column+": "+problem.Reason)
		case problem.Sheet == DocumentSheet && sheetName == UsersSheet:
			documentProblems = append(documentProblems, "document: "+problem.Column+": "+problem.Reason)
		}
	}
	var buf bytes.Buffer
	out := csv.NewWriter(&buf)
	names := make([]string, 0, len(columns)+1)
	for _, column := range columns {
		names = append(names, column.Name)
	}
	records := [][]string{append(names, ReportColumn)}
	for _, row := range rows {
		values := make([]string, 0, len(columns)+1)
		for _, column := range columns {
			values = append(values, row.Cells[column.Name])
		}
		records = append(records, append(values, strings.Join(byLine[row.Line], "; ")))
	}
	for _, problem := range documentProblems {
		records = append(records, append(make([]string, len(columns)), problem))
	}
	if err := out.WriteAll(records); err != nil {
		return nil, fmt.Errorf("report %s: %w", sheetName, err)
	}
	return buf.Bytes(), nil
}

// ------------------------------------------------------------------------------
// mustJSON is a value as JSON; it panics on a value that has none.
func mustJSON(value any) []byte {
	raw, err := json.Marshal(value)
	if err != nil {
		panic(err)
	}
	return raw
}
