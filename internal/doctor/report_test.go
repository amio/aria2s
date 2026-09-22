package doctor_test

import (
	"bytes"
	"encoding/json"
	"fmt"
	"strings"
	"testing"

	"github.com/amio/aria2s/internal/doctor"
	"github.com/charmbracelet/x/ansi"
)

func TestDoctorOutputModesPreserveFindingsAndRedactEvidence(t *testing.T) {
	report := doctor.Report{Checks: []doctor.DiagnosticCheck{{Group: doctor.Runtime, Name: "RPC", Issue: doctor.Issue{Severity: doctor.OK, Summary: "responding"}}}}
	for i := 0; i < 15; i++ {
		report.Checks = append(report.Checks, doctor.DiagnosticCheck{
			Group: doctor.Downloads, Name: "private https://user:password@example.test/file?signed=private-query\x1b[31m\nFAKE",
			TaskID: fmt.Sprintf("%016x", i), TaskStatus: "error", Ownership: "managed",
			Issue: doctor.Issue{Code: "NativeTaskError", Severity: doctor.Error, Summary: "download failed",
				Evidence: "rpc-secret=hidden-secret token:rpc-secret-value exact-credential magnet:?xt=secret-hash",
				Recovery: []string{"Open Dashboard and inspect this task."}},
		})
	}
	report = report.Sanitized("exact-credential")
	for _, mode := range []struct {
		name    string
		options doctor.RenderOptions
		shown   int
	}{
		{"default", doctor.RenderOptions{}, 10},
		{"summary", doctor.RenderOptions{Summary: true, ASCII: true}, 10},
		{"expanded", doctor.RenderOptions{All: true, ASCII: true}, 15},
		{"json", doctor.RenderOptions{JSON: true}, 15},
	} {
		t.Run(mode.name, func(t *testing.T) {
			var buffer bytes.Buffer
			if err := doctor.Render(&buffer, report, mode.options); err != nil {
				t.Fatal(err)
			}
			output := buffer.String()
			for _, private := range []string{"password", "private-query", "hidden-secret", "rpc-secret-value", "exact-credential", "secret-hash", "\x1b"} {
				if strings.Contains(output, private) {
					t.Fatalf("output leaked %q: %s", private, output)
				}
			}
			if got := strings.Count(output, "download failed"); got != mode.shown {
				t.Fatalf("shown findings = %d, want %d", got, mode.shown)
			}
			if mode.options.JSON {
				var decoded struct {
					SchemaVersion     int
					Healthy, Complete bool
					Counts            doctor.Counts
					Checks            []doctor.DiagnosticCheck
				}
				if err := json.Unmarshal(buffer.Bytes(), &decoded); err != nil {
					t.Fatal(err)
				}
				if decoded.SchemaVersion != 1 || decoded.Healthy || !decoded.Complete || decoded.Counts.Error != 15 || len(decoded.Checks) != 16 {
					t.Fatalf("inconsistent JSON: %+v", decoded)
				}
			} else {
				if !strings.Contains(output, "15 errors") {
					t.Fatalf("truncation changed totals: %s", output)
				}
				if mode.options.Summary && (strings.Contains(output, "Evidence:") || strings.Contains(output, "Next steps")) {
					t.Fatalf("summary expanded details: %s", output)
				}
				if !mode.options.Summary && strings.Count(output, "Open Dashboard") != 1 {
					t.Fatalf("suggestions not deduplicated: %s", output)
				}
			}
		})
	}
}

func TestDoctorUnknownCoverageIsNotHealthySuccess(t *testing.T) {
	report := doctor.Report{Checks: []doctor.DiagnosticCheck{{Group: doctor.Downloads, Name: "Live coverage", Issue: doctor.Issue{Severity: doctor.Skipped, Summary: "RPC unavailable"}}}}
	var buffer bytes.Buffer
	if err := doctor.Render(&buffer, report, doctor.RenderOptions{}); err != nil {
		t.Fatal(err)
	}
	if report.Complete() || !strings.Contains(buffer.String(), "Checks incomplete") || strings.Contains(buffer.String(), "No issues found") {
		t.Fatalf("unknown coverage was reported as success: %s", buffer.String())
	}
}

func TestDoctorRedactionCannotChangeHealthClassification(t *testing.T) {
	report := doctor.Report{Checks: []doctor.DiagnosticCheck{{Group: doctor.Downloads, TaskStatus: "error", Issue: doctor.Issue{Severity: doctor.Error, Evidence: "credential error"}}}}
	clean := report.Sanitized("error")
	if clean.Healthy() || clean.Counts().Error != 1 || clean.Checks[0].TaskStatus != "error" || strings.Contains(clean.Checks[0].Evidence, "error") {
		t.Fatalf("redaction changed trusted classification or leaked evidence: %+v", clean)
	}
}

func TestDoctorWrapsTerminalCellsWithoutLosingExpandedEvidence(t *testing.T) {
	longName := strings.Repeat("下载🌊e\u0301", 25) + ".torrent"
	longPath := "/Users/test/Library/Application Support/aria2s/" + strings.Repeat("payload", 30) + ".session"
	report := doctor.Report{Checks: []doctor.DiagnosticCheck{{Group: doctor.Downloads, Name: longName, TaskID: "0123456789abcdef", Issue: doctor.Issue{
		Severity: doctor.Error, Summary: "restore the published payload before retrying this download",
		Evidence: longPath, Recovery: []string{"Run `aria2s doctor --all` after restoring the files."},
	}}}}
	for _, width := range []int{24, 40, 80, 100, 160} {
		for _, all := range []bool{false, true} {
			for _, ascii := range []bool{false, true} {
				options := doctor.RenderOptions{Width: width, All: all, ASCII: ascii, HomeDir: "/Users/test"}
				var plain, color bytes.Buffer
				if err := doctor.Render(&plain, report, options); err != nil {
					t.Fatal(err)
				}
				options.Color = true
				if err := doctor.Render(&color, report, options); err != nil {
					t.Fatal(err)
				}
				if ansi.Strip(color.String()) != plain.String() {
					t.Fatalf("styling changed layout at width %d", width)
				}
				for _, line := range strings.Split(color.String(), "\n") {
					if ansi.StringWidth(line) > min(width, 100) {
						t.Fatalf("line exceeds %d cells: %q", min(width, 100), line)
					}
				}
				joined := strings.Join(strings.Fields(plain.String()), "")
				if !strings.Contains(joined, strings.ReplaceAll(strings.Replace(longPath, "/Users/test", "~", 1), " ", "")) {
					t.Fatal("wrapping lost evidence")
				}
				if all && !strings.Contains(joined, longName) {
					t.Fatal("expanded report lost task name")
				}
			}
		}
	}
	var jsonOutput bytes.Buffer
	if err := doctor.Render(&jsonOutput, report, doctor.RenderOptions{JSON: true, Width: 24, Color: true, HomeDir: "/Users/test"}); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(jsonOutput.String(), longPath) || strings.Contains(jsonOutput.String(), "\x1b") {
		t.Fatal("terminal formatting changed JSON evidence")
	}
}
