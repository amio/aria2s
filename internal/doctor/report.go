// Package doctor owns diagnostic reports and runtime evidence. Checks are the
// sole source of health and counts; an unobserved dependency is never success.
package doctor

import (
	"regexp"
	"strings"
	"unicode"
)

const (
	OK           = "ok"
	Warning      = "warning"
	Error        = "error"
	Skipped      = "skipped"
	Installation = "Installation"
	Runtime      = "Service & RPC"
	Downloads    = "Downloads"
)

type Issue struct {
	Code        string   `json:"code,omitempty"`
	Severity    string   `json:"severity"`
	Summary     string   `json:"summary"`
	Explanation string   `json:"explanation,omitempty"`
	Evidence    string   `json:"evidence,omitempty"`
	Recovery    []string `json:"recovery,omitempty"`
}

type DiagnosticCheck struct {
	Group string `json:"group"`
	Name  string `json:"name"`
	Issue
	TaskID     string `json:"taskId,omitempty"`
	TaskStatus string `json:"taskStatus,omitempty"`
	Ownership  string `json:"ownership,omitempty"`
}

type Repair struct {
	Code    string `json:"code"`
	Command string `json:"command"`
	Summary string `json:"summary"`
}

type Report struct {
	Checks []DiagnosticCheck `json:"checks"`
	Repair *Repair           `json:"repair,omitempty"`
}

type Counts struct {
	OK      int `json:"ok"`
	Warning int `json:"warning"`
	Error   int `json:"error"`
	Skipped int `json:"skipped"`
}

func (report Report) Counts() (counts Counts) {
	for _, check := range report.Checks {
		switch check.Severity {
		case OK:
			counts.OK++
		case Warning:
			counts.Warning++
		case Error:
			counts.Error++
		default:
			counts.Skipped++
		}
	}
	return counts
}

func (report Report) Healthy() bool  { return report.Counts().Error == 0 }
func (report Report) Complete() bool { return report.Counts().Skipped == 0 }

func (report Report) Issues() []DiagnosticCheck {
	var result []DiagnosticCheck
	for _, check := range report.Checks {
		if check.Severity == Warning || check.Severity == Error {
			result = append(result, check)
		}
	}
	return result
}

func checkGroup(name string) string {
	switch name {
	case "Supervisor", "RPC", "Startup":
		return Runtime
	default:
		return Installation
	}
}

// Never publish raw source URLs (including signed queries), RPC credentials,
// or terminal control characters, even in expanded or machine-readable output.
var diagnosticURL = regexp.MustCompile(`(?i)(?:[a-z][a-z0-9+.-]*://|magnet:\?)[^\s<>"\x60]+`)
var diagnosticToken = regexp.MustCompile(`(?i)(?:token:|rpc-secret\s*[=:]\s*)[^\s,;"']+`)

func CleanText(value string, secrets ...string) string {
	for _, secret := range secrets {
		if secret != "" {
			value = strings.ReplaceAll(value, secret, "[redacted]")
		}
	}
	value = diagnosticURL.ReplaceAllString(value, "[redacted URL]")
	value = diagnosticToken.ReplaceAllString(value, "[redacted credential]")
	value = strings.Map(func(r rune) rune {
		if unicode.IsControl(r) || unicode.In(r, unicode.Cf) {
			return ' '
		}
		return r
	}, value)
	return strings.Join(strings.Fields(value), " ")
}

func (report Report) Sanitized(secrets ...string) Report {
	checks := make([]DiagnosticCheck, len(report.Checks))
	for i, check := range report.Checks {
		// Classification fields are app-owned enums. Redacting a short secret
		// such as "error" must not change counts, exit status, or grouping.
		check.Group = CleanText(check.Group)
		check.Name = CleanText(check.Name, secrets...)
		check.Code = CleanText(check.Code, secrets...)
		check.Severity = CleanText(check.Severity)
		check.Summary = CleanText(check.Summary, secrets...)
		check.Explanation = CleanText(check.Explanation, secrets...)
		check.Evidence = CleanText(check.Evidence, secrets...)
		check.TaskID = CleanText(check.TaskID, secrets...)
		check.TaskStatus = CleanText(check.TaskStatus)
		check.Ownership = CleanText(check.Ownership)
		check.Recovery = append([]string(nil), check.Recovery...)
		for j := range check.Recovery {
			check.Recovery[j] = CleanText(check.Recovery[j], secrets...)
		}
		checks[i] = check
	}
	report.Checks = checks
	if report.Repair != nil {
		repair := *report.Repair
		repair.Code = CleanText(repair.Code, secrets...)
		repair.Command = CleanText(repair.Command, secrets...)
		repair.Summary = CleanText(repair.Summary, secrets...)
		report.Repair = &repair
	}
	return report
}
