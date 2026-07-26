package doctor

import (
	"context"
	"fmt"
	"os"
	"strings"

	"github.com/amio/aria2s/internal/jobs"
	"github.com/amio/aria2s/internal/paths"
	"github.com/amio/aria2s/internal/state"
)

type Issue struct {
	Code        string
	Severity    string
	Summary     string
	Explanation string
	Evidence    string
	Recovery    []string
	Message     string // compatibility rendering alias for Summary
}

type Report struct {
	Healthy bool
	Issues  []Issue
}

type Options struct {
	Paths           paths.Paths
	IsPortAvailable func(int) bool
	Service         SupervisorStatus
	RPCReachable    func(context.Context, state.State) bool
}

func Check(ctx context.Context, options Options) Report {
	var issues []Issue
	current, err := state.Load(options.Paths.StateFile)
	if err != nil {
		return Report{Healthy: false, Issues: []Issue{problem("InstallIncomplete", "state file missing or unreadable", err.Error(), "Run `aria2s install`.")}}
	}
	if current.RuntimeSchemaVersion != 2 {
		issues = append(issues, problem("UpgradeRequired", "managed runtime upgrade required", fmt.Sprintf("schema=%d", current.RuntimeSchemaVersion), "Run `aria2s dashboard` for v1 reinstall instructions, or `aria2s install --discard-legacy-tasks`."))
	}
	if !isExecutable(current.Aria2cPath) {
		issues = append(issues, problem("ControllerUnavailable", "missing aria2c binary", current.Aria2cPath, "Install aria2 and rerun `aria2s install`."))
	}
	if !fileExists(options.Paths.ServiceFile) {
		issues = append(issues, problem("InstallIncomplete", "missing service file", options.Paths.ServiceFile, "Run `aria2s install`."))
	}
	if options.Service != nil && !options.Service.IsLoaded(ctx) {
		issues = append(issues, problem("ControllerUnavailable", "supervisor unloaded", current.ServiceName, "Run `aria2s start`."))
	}
	if options.Service != nil && options.Service.IsLoaded(ctx) && !options.Service.IsRunning(ctx) {
		issues = append(issues, problem("ControllerUnavailable", "supervisor not running", current.ServiceName, "Inspect logs, then run `aria2s start`."))
	}
	if options.RPCReachable != nil && !options.RPCReachable(ctx, current) {
		issues = append(issues, problem("RPCUnavailable", "RPC unreachable", fmt.Sprintf("127.0.0.1:%d", current.RPCPort), "Inspect logs and run `aria2s doctor`."))
	}
	if options.IsPortAvailable != nil && !options.IsPortAvailable(current.RPCPort) && !managedServiceOwnsPort(ctx, options, current) {
		issues = append(issues, problem("RPCUnavailable", "port conflict", fmt.Sprintf("127.0.0.1:%d", current.RPCPort), "Stop the conflicting process or rerun install."))
	}
	scanned, scanErr := jobs.New(options.Paths.StateDir).Scan()
	if scanErr != nil {
		issues = append(issues, problem("InstallIncomplete", "managed job store is unreadable", scanErr.Error(), "Repair permissions for the aria2s state directory, then rerun doctor."))
	} else {
		for _, item := range scanned {
			if item.Err != nil {
				issues = append(issues, lifecycleProblem("CorruptManifest", item.ID, item.Err.Error()))
				continue
			}
			if item.Job.ProblemCode != "" {
				issues = append(issues, lifecycleProblem(item.Job.ProblemCode, item.ID, "managed manifest"))
			}
		}
	}
	return Report{Healthy: len(issues) == 0, Issues: issues}
}

func problem(code, summary, evidence, recovery string) Issue {
	return Issue{Code: code, Severity: "error", Summary: summary, Message: summary, Explanation: summary, Evidence: evidence, Recovery: []string{recovery}}
}

func lifecycleProblem(code, gid, evidence string) Issue {
	summary := "managed task requires recovery"
	recovery := "Open Dashboard and use Retry after correcting the reported condition."
	switch code {
	case "StorageOffline", "StorageMismatch":
		summary = "managed storage is unavailable or changed"
		recovery = "Reconnect the original storage, verify the target, then use Retry."
	case "PublicationConflict":
		summary = "publication destination already exists"
		recovery = "Use Retry to publish the retained staging payload under the next available suffixed name."
	case "PublicationRecoveryRequired", "PublicationPayloadMismatch", "PublicationPayloadMissing", "PublicationStateUncertain":
		summary = "publication outcome requires manual reconciliation"
		recovery = "Inspect staging and target, preserve the only payload, then use Retry or explicit Clear."
	case "RestartStateMissing":
		summary = "safe restart state is missing"
		recovery = "Restore the original session/metainfo or inspect retained staging before Retry."
	case "CorruptManifest":
		summary = "managed manifest is corrupt"
		recovery = "Confirm the GID is absent from aria2, then Clear the corrupt Dashboard row."
	case "ManagedIdentityConflict", "FinalSeedPathMismatch":
		summary = "managed GID or payload identity conflicts with observed state"
		recovery = "Stop external RPC changes and inspect the GID/path before retrying."
	case "RestartCheckpointFailed":
		summary = "aria2 session checkpoint failed"
		recovery = "Keep the service running, repair RPC/session access, and retry the operation."
	case "CleanupFailed":
		summary = "published task cleanup is incomplete"
		recovery = "Restore storage access and use Retry; the published payload is retained."
	case "AddFailed", "FinalSeedStartFailed":
		summary = "managed Add outcome needs reconciliation"
		recovery = "Restore RPC availability and use Retry; do not submit a duplicate manually."
	case "PowerLossDurabilityUnavailable":
		summary = "storage does not support directory durability sync"
		recovery = "No action is required for process-crash safety; avoid relying on host power-loss durability."
	}
	issue := problem(code, summary, fmt.Sprintf("gid=%s; %s", gid, evidence), recovery)
	if code == "PowerLossDurabilityUnavailable" {
		issue.Severity = "warning"
	}
	return issue
}

func managedServiceOwnsPort(ctx context.Context, options Options, current state.State) bool {
	if options.Service == nil || options.RPCReachable == nil {
		return false
	}
	return options.Service.IsRunning(ctx) && options.RPCReachable(ctx, current)
}

type SupervisorStatus interface {
	IsLoaded(context.Context) bool
	IsRunning(context.Context) bool
}

type StatusOptions struct {
	Paths      paths.Paths
	Service    SupervisorStatus
	RPCVersion func(context.Context, state.State) (string, error)
}

type StatusReport struct {
	ServiceInstalled  bool
	SupervisorLoaded  bool
	SupervisorRunning bool
	BinaryValid       bool
	RPCReachable      bool
	Version           string
	Endpoint          string
	ConfigPath        string
	LogPath           string
}

func Status(ctx context.Context, options StatusOptions) StatusReport {
	current, err := state.Load(options.Paths.StateFile)
	report := StatusReport{
		ServiceInstalled: fileExists(options.Paths.ServiceFile),
		ConfigPath:       options.Paths.ConfigFile,
		LogPath:          options.Paths.LogFile,
	}
	if err != nil {
		return report
	}
	report.Endpoint = fmt.Sprintf("http://127.0.0.1:%d/jsonrpc", current.RPCPort)
	report.BinaryValid = isExecutable(current.Aria2cPath)
	if options.Service != nil {
		report.SupervisorLoaded = options.Service.IsLoaded(ctx)
		report.SupervisorRunning = options.Service.IsRunning(ctx)
	}
	if options.RPCVersion != nil {
		version, err := options.RPCVersion(ctx, current)
		if err == nil {
			report.RPCReachable = true
			report.Version = version
		}
	}
	return report
}

func (report StatusReport) String() string {
	serviceText := "missing"
	if report.ServiceInstalled {
		serviceText = "installed"
	}
	supervisorText := "stopped"
	if report.SupervisorRunning {
		supervisorText = "running"
	} else if report.SupervisorLoaded {
		supervisorText = "loaded"
	}
	rpcText := "unreachable"
	if report.RPCReachable {
		rpcText = "reachable"
	}
	binaryText := "missing"
	if report.BinaryValid {
		binaryText = "valid"
	}
	var builder strings.Builder
	fmt.Fprintf(&builder, "Service:    %s\n", serviceText)
	fmt.Fprintf(&builder, "Supervisor: %s\n", supervisorText)
	fmt.Fprintf(&builder, "Binary:     %s\n", binaryText)
	fmt.Fprintf(&builder, "RPC:        %s\n", rpcText)
	if report.Version != "" {
		fmt.Fprintf(&builder, "aria2:      %s\n", report.Version)
	}
	if report.Endpoint != "" {
		fmt.Fprintf(&builder, "Endpoint:   %s\n", report.Endpoint)
	}
	fmt.Fprintf(&builder, "Config:     %s\n", report.ConfigPath)
	fmt.Fprintf(&builder, "Logs:       %s\n", report.LogPath)
	return builder.String()
}

func isExecutable(path string) bool {
	info, err := os.Stat(path)
	return err == nil && !info.IsDir() && info.Mode().Perm()&0o111 != 0
}

func fileExists(path string) bool {
	_, err := os.Stat(path)
	return err == nil
}
