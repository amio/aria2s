package doctor

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"regexp"
	"strings"

	"github.com/charmbracelet/x/ansi"
)

type RenderOptions struct {
	JSON    bool
	Summary bool
	All     bool
	ASCII   bool
	Color   bool
	Width   int
	HomeDir string
}

func Render(out io.Writer, report Report, options RenderOptions) error {
	report = report.Sanitized()
	counts := report.Counts()
	if options.JSON {
		encoder := json.NewEncoder(out)
		encoder.SetIndent("", "  ")
		return encoder.Encode(struct {
			SchemaVersion int    `json:"schemaVersion"`
			Healthy       bool   `json:"healthy"`
			Complete      bool   `json:"complete"`
			Counts        Counts `json:"counts"`
			Report
		}{1, report.Healthy(), report.Complete(), counts, report})
	}
	width := options.Width
	if width <= 0 {
		width = 100
	}
	writer := reportWriter{options: options, width: min(width, 100)}
	writer.line("", options.style("aria2s doctor", "1"))
	result := "No issues found."
	switch {
	case counts.Error > 0:
		result = "Action required."
	case counts.Skipped > 0:
		result = "Checks incomplete; health could not be fully verified."
	case counts.Warning > 0:
		result = "No errors found; review the warnings below."
	}
	resultSeverity := OK
	if counts.Error > 0 {
		resultSeverity = Error
	} else if counts.Skipped > 0 || counts.Warning > 0 {
		resultSeverity = Warning
	}
	writer.line("", options.style(result, severityColor(resultSeverity)))
	for _, group := range []string{Installation, Runtime, Downloads} {
		writer.heading(group)
		shownTasks, omittedTasks := 0, 0
		// Errors and warnings precede healthy task rows; regular checks retain
		// dependency order. A list limit never changes the report's counts.
		ordered := make([]DiagnosticCheck, 0, len(report.Checks))
		for _, check := range report.Checks {
			if check.Group == group && check.TaskID == "" {
				ordered = append(ordered, check)
			}
		}
		for _, severity := range []string{Error, Warning, Skipped, OK} {
			for _, check := range report.Checks {
				if check.Group == group && check.TaskID != "" && check.Severity == severity {
					ordered = append(ordered, check)
				}
			}
		}
		for _, check := range ordered {
			if check.TaskID != "" {
				if check.Severity == OK && !options.All {
					continue
				}
				if !options.All && shownTasks >= 10 {
					omittedTasks++
					continue
				}
				shownTasks++
			}
			name := check.Name
			if !options.All {
				name = ansi.Truncate(name, max(12, writer.width/2), "...")
			}
			name = options.style(name, "1")
			if check.TaskID != "" {
				name += options.style(" ["+check.TaskID+"]", "90")
			}
			writer.line("  "+statusMark(check.Severity, options)+" ", name+": "+options.style(check.Summary, summaryColor(check.Severity)))
			if !options.Summary {
				if check.Explanation != "" && check.Explanation != check.Summary {
					writer.detail("Why", check.Explanation)
				}
				if check.Evidence != "" {
					writer.detail("Evidence", check.Evidence)
				}
			}
		}
		if omittedTasks > 0 {
			writer.line("  ", options.inline(fmt.Sprintf("... %d more task findings; use `--all` or `--json`.", omittedTasks), "90"))
		}
	}
	if !options.Summary {
		if report.Repair != nil {
			writer.heading("Recommended repair:")
			writer.line("  ", options.style(report.Repair.Command, "36"))
			writer.line("  ", options.inline(report.Repair.Summary, ""))
		}
		var steps []string
		seen := make(map[string]bool)
		for _, check := range report.Checks {
			if report.Repair != nil && (check.Name == "RPC" || check.Name == "Startup") {
				continue
			}
			for _, step := range check.Recovery {
				if !seen[step] {
					steps = append(steps, step)
					seen[step] = true
				}
			}
		}
		if len(steps) > 0 {
			writer.heading("Next steps")
			limit := len(steps)
			if !options.All && limit > 5 {
				limit = 5
			}
			for i, step := range steps[:limit] {
				writer.line(options.style(fmt.Sprintf("  %d. ", i+1), "90"), options.inline(step, ""))
			}
			if limit < len(steps) {
				writer.line("  ", options.inline(fmt.Sprintf("... %d more suggestions; use `--all` or `--json`.", len(steps)-limit), "90"))
			}
		}
	}

	writer.WriteByte('\n')
	separator := "─"
	if options.ASCII {
		separator = "-"
	}
	writer.line("", options.style(strings.Repeat(separator, writer.width), "90"))
	var totals []string
	for _, total := range []struct {
		n               int
		label, severity string
	}{
		{counts.OK, "passed", OK}, {counts.Warning, "warnings", Warning},
		{counts.Error, "errors", Error}, {counts.Skipped, "unchecked", Skipped},
	} {
		color := severityColor(total.severity)
		if total.n == 0 {
			color = "90"
		}
		totals = append(totals, options.style(fmt.Sprintf("%d %s", total.n, total.label), color))
	}
	writer.line("", strings.Join(totals, options.style("  |  ", "90")))
	writer.WriteByte('\n')
	writer.line("", options.style("--summary", "36")+options.style(" compact checks   ", "90")+options.style("--all", "36")+options.style(" expand details", "90"))
	writer.line("", options.style("--json", "36")+options.style(" redacted report", "90"))
	_, err := out.Write(writer.Bytes())
	return err
}

// Layout measures terminal cells, not bytes or runes, and keeps ANSI styling
// out of the width budget. Expanded reports retain the same readable measure.
type reportWriter struct {
	bytes.Buffer
	options RenderOptions
	width   int
}

func (writer *reportWriter) line(prefix, text string) {
	indent := ansi.StringWidth(prefix)
	if indent >= writer.width-1 {
		text, prefix, indent = prefix+text, "", 0
	}
	for i, line := range strings.Split(ansi.Wrap(text, max(1, writer.width-indent), ""), "\n") {
		if i == 0 {
			writer.WriteString(prefix)
		} else {
			writer.WriteString(strings.Repeat(" ", indent))
		}
		writer.WriteString(strings.TrimRight(line, " "))
		writer.WriteByte('\n')
	}
}

func (writer *reportWriter) heading(title string) {
	writer.WriteByte('\n')
	writer.line("", writer.options.style(title, "1"))
}

func (writer *reportWriter) detail(label, value string) {
	writer.line("      "+writer.options.style(fmt.Sprintf("%-10s", label+":"), "90"), writer.options.inline(value, ""))
}

func (options RenderOptions) style(value, code string) string {
	if !options.Color || code == "" || value == "" {
		return value
	}
	return "\x1b[" + code + "m" + value + "\x1b[0m"
}

// Backtick commands and filesystem paths are navigation cues, distinct from
// diagnostic severity. Spaces inside macOS paths stay part of the highlight.
var reportReference = regexp.MustCompile("`[^`]+`|(?:~/|/)[^:;()\x60]+")

func (options RenderOptions) inline(value, baseColor string) string {
	if options.HomeDir != "" && options.HomeDir != "/" {
		value = strings.ReplaceAll(value, strings.TrimRight(options.HomeDir, "/")+"/", "~/")
	}
	var text strings.Builder
	last := 0
	for _, span := range reportReference.FindAllStringIndex(value, -1) {
		start, end := span[0], span[1]
		if value[start] != '`' && start > 0 && !strings.ContainsRune(" (=", rune(value[start-1])) {
			continue
		}
		text.WriteString(options.style(value[last:start], baseColor))
		text.WriteString(options.style(value[start:end], "36"))
		last = end
	}
	text.WriteString(options.style(value[last:], baseColor))
	return text.String()
}

func severityColor(severity string) string {
	switch severity {
	case OK:
		return "32"
	case Warning:
		return "33"
	case Error:
		return "31"
	default:
		return "90"
	}
}

func summaryColor(severity string) string {
	if severity == OK {
		return "90"
	}
	return severityColor(severity)
}

func statusMark(severity string, options RenderOptions) string {
	mark := "-"
	switch severity {
	case OK:
		mark = "✓"
	case Warning:
		mark = "!"
	case Error:
		mark = "✗"
	}
	if options.ASCII {
		switch severity {
		case OK:
			mark = "[OK]"
		case Warning:
			mark = "[WARN]"
		case Error:
			mark = "[FAIL]"
		default:
			mark = "[SKIP]"
		}
	}
	return options.style(mark, severityColor(severity))
}
