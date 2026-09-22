package doctor

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"regexp"
	"strings"
	"time"

	"github.com/amio/aria2s/internal/jobs"
	"github.com/amio/aria2s/internal/paths"
	managedruntime "github.com/amio/aria2s/internal/runtime"
	"github.com/amio/aria2s/internal/state"
)

type Options struct {
	Paths               paths.Paths
	IsPortAvailable     func(int) bool
	Service             SupervisorStatus
	RPCVersion          func(context.Context, state.State) (string, error)
	RPCProbeTimeout     time.Duration
	RPCSlowThreshold    time.Duration
	ReadLogTail         func(string, int64) ([]byte, error)
	InspectInstallation func(state.State) error
	InspectTasks        func(context.Context, state.State, bool, []jobs.ScannedJob, error) []DiagnosticCheck
}

const (
	defaultRPCProbeTimeout  = 30 * time.Second
	defaultRPCSlowThreshold = 2 * time.Second
)

func Check(ctx context.Context, options Options) (report Report) {
	var current state.State
	var rpcReachable bool
	// Task coverage remains useful even when runtime state cannot be loaded.
	defer func() {
		scanned, scanErr := jobs.New(options.Paths.StateDir).Scan()
		if options.InspectTasks != nil {
			report.Checks = append(report.Checks, options.InspectTasks(ctx, current, rpcReachable, scanned, scanErr)...)
		}
		report = report.Sanitized(current.RPCSecret)
	}()
	addSuccess := func(name, summary string) {
		report.Checks = append(report.Checks, DiagnosticCheck{Group: checkGroup(name), Name: name, Issue: Issue{Severity: OK, Summary: summary}})
	}
	addIssue := func(name string, issue Issue) {
		report.Checks = append(report.Checks, DiagnosticCheck{Group: checkGroup(name), Name: name, Issue: issue})
	}

	var err error
	current, err = state.Load(options.Paths.StateFile)
	if err != nil {
		addIssue("Runtime state", problem("InstallIncomplete", "state file is missing or unreadable", err.Error(), "Run `aria2s install`."))
		report.Checks = append(report.Checks, DiagnosticCheck{Group: Runtime, Name: "Runtime inspection", Issue: Issue{Severity: Skipped, Summary: "service and RPC checks require readable managed state"}})
		return report
	}
	addSuccess("Runtime state", "managed state is readable")
	if current.RuntimeSchemaVersion != 2 {
		addIssue("Runtime schema", problem("UpgradeRequired", "managed runtime upgrade is required", fmt.Sprintf("schema=%d", current.RuntimeSchemaVersion), "Run `aria2s dashboard` for v1 reinstall instructions, or `aria2s install --discard-legacy-tasks`."))
	} else {
		addSuccess("Runtime schema", "schema v2")
	}
	if !isExecutable(current.Aria2cPath) {
		addIssue("aria2c", problem("ControllerUnavailable", "missing aria2c binary or binary is not executable", current.Aria2cPath, "Install aria2 and rerun `aria2s install`."))
	} else {
		addSuccess("aria2c", "binary is executable")
	}
	if !fileExists(options.Paths.ServiceFile) {
		addIssue("Service", problem("InstallIncomplete", "missing service file", options.Paths.ServiceFile, "Run `aria2s install`."))
	} else {
		addSuccess("Service", "managed service file is present")
	}
	if options.InspectInstallation != nil && current.RuntimeSchemaVersion == 2 {
		if err := options.InspectInstallation(current); err != nil {
			addIssue("Committed runtime", problem("InstallIncomplete", "installed runtime does not match its committed configuration", err.Error(), "Run `aria2s install` to repair the managed runtime."))
		} else {
			addSuccess("Committed runtime", "controller, service, session, and paths match installed state")
		}
	}

	loaded, running := false, false
	if options.Service != nil {
		loaded = options.Service.IsLoaded(ctx)
		running = loaded && options.Service.IsRunning(ctx)
		switch {
		case !loaded:
			addIssue("Supervisor", problem("ControllerUnavailable", "supervisor unloaded", current.ServiceName, "Run `aria2s start`."))
		case !running:
			addIssue("Supervisor", problem("ControllerUnavailable", "supervisor not running", current.ServiceName, "Inspect the logs, then run `aria2s start`."))
		default:
			addSuccess("Supervisor", "managed service is running")
		}
	}

	probe := observeRPC(ctx, current, options.RPCVersion, options.RPCProbeTimeout, options.RPCSlowThreshold)
	rpcReachable = probe.Reachable
	portOccupied := options.IsPortAvailable != nil && !options.IsPortAvailable(current.RPCPort)
	endpoint := fmt.Sprintf("127.0.0.1:%d", current.RPCPort)
	if probe.Slow {
		issue := problem(
			"RPCSlow",
			"managed RPC is responding slowly",
			fmt.Sprintf("%s; latency=%s", endpoint, formatLatency(probe.Latency)),
			"If this persists, reduce seeding pressure on slow storage or set `max-overall-upload-limit` in aria2.conf.",
		)
		issue.Severity = "warning"
		addIssue("RPC", issue)
	} else if rpcReachable {
		addSuccess("RPC", endpoint+" is responding")
	} else if options.RPCVersion != nil || options.IsPortAvailable != nil {
		switch {
		case running && portOccupied:
			addIssue("RPC", problem("RPCUnresponsive", "managed service is listening but RPC does not respond", endpoint, "Inspect `aria2s logs`; use a recommended repair only when Doctor identifies a startup blocker."))
		case !running && portOccupied:
			addIssue("RPC", problem("PortConflict", "port conflict: RPC port is used by another process", endpoint, "Stop the conflicting process or rerun `aria2s install` to select another port."))
		default:
			addIssue("RPC", problem("RPCUnavailable", "RPC unreachable", endpoint, "Inspect `aria2s logs`, correct the reported startup error, then run `aria2s start`."))
		}
	}

	if running && portOccupied && !rpcReachable {
		readTail := options.ReadLogTail
		if readTail == nil {
			readTail = managedruntime.ReadLogTail
		}
		logPath := current.LogPath
		if logPath == "" {
			logPath = options.Paths.LogFile
		}
		if tail, readErr := readTail(logPath, 256*1024); readErr == nil {
			scanned, _ := jobs.New(options.Paths.StateDir).Scan()
			if gid := currentFileAllocationGID(tail, scanned); gid != "" {
				addIssue("Startup", problem("FileAllocationBlocked", "file allocation is blocking aria2 startup", "gid="+gid+"; current aria2 log is stalled at FileAlloc", "Run `aria2s doctor --repair --discard-unmanaged-tasks`."))
				report.Repair = &Repair{
					Code:    "FileAllocationBlocked",
					Command: "aria2s doctor --repair --discard-unmanaged-tasks",
					Summary: "Restarts aria2 with file preallocation disabled for this process, preserves managed download state, and verifies RPC before reporting success.",
				}
			}
		}
	}

	return report
}

func problem(code, summary, evidence, recovery string) Issue {
	return Issue{Code: code, Severity: "error", Summary: summary, Explanation: summary, Evidence: evidence, Recovery: []string{recovery}}
}

func LifecycleProblem(code, jobID, evidence string) Issue {
	summary := "managed task requires recovery"
	severity := "error"
	if metadata, ok := jobs.LookupIssue(code); ok {
		summary = metadata.Text
		severity = metadata.Severity
	}
	recovery := "Open Dashboard and use Retry after correcting the reported condition."
	switch code {
	case "StorageOffline":
		recovery = "Mount or reconnect the original storage at its registered path, then use Retry."
	case "StorageMismatch":
		recovery = "Verify that the original storage and aria2s staging marker are present at the registered mount path, then use Retry."
	case "StorageStateUnavailable":
		recovery = "Restore or repair the matching storage record in the aria2s state directory, then use Retry."
	case "TargetUnavailable":
		recovery = "Restore access to the task's registered download directory, then use Retry."
	case "TargetMismatch":
		recovery = "Restore the original download directory; published payloads are not automatically relocated. Then use Retry or remove the task."
	case "PayloadStorageMismatch":
		recovery = "Inspect the payload and download paths, restore them to the registered storage relationship, then use Retry."
	case "PublicationConflict":
		recovery = "Use Retry to publish the retained staging payload under the next available suffixed name."
	case "PublicationRecoveryRequired", "PublicationPayloadMismatch", "PublicationPayloadMissing", "PublicationStateUncertain":
		recovery = "Inspect staging and target, preserve the only payload, then use the actions shown in Dashboard."
	case "RestartStateMissing":
		recovery = "Restore the original session/metainfo or inspect retained staging before Retry."
	case "CorruptManifest":
		recovery = "Preserve the aria2s state directory and inspect the matching aria2 task manually; automatic deletion is unavailable because native ownership cannot be proven."
	case "ManagedIdentityConflict":
		recovery = "Stop external RPC changes and inspect the GID/path before retrying."
	case "FinalSeedPathMismatch":
		recovery = "Restore the published files at their original download location, then use Retry; if those files are no longer needed, choose Remove in Dashboard."
	case "RestartCheckpointFailed":
		recovery = "Keep the service running, repair RPC/session access, and retry the operation."
	case "RemovalFailed":
		recovery = "Restore storage access, then choose Remove in Dashboard to finish removal; Retry cannot revive this task."
	case "CleanupFailed":
		recovery = "Restore storage access, run `aria2s dashboard`, select this task, and choose Remove; the published payload is retained."
	case "AddFailed", "FinalSeedStartFailed":
		recovery = "Restore RPC availability and use Retry; do not submit a duplicate manually."
	case "PowerLossDurabilityUnavailable":
		recovery = "No action is required for process-crash safety; avoid relying on host power-loss durability."
	}
	issue := problem(code, summary, fmt.Sprintf("job=%s; %s", jobID, evidence), recovery)
	issue.Severity = severity
	return issue
}

var fileAllocationPattern = regexp.MustCompile(`FileAlloc:#([0-9a-fA-F]{6,16})`)

func currentFileAllocationGID(logTail []byte, scanned []jobs.ScannedJob) string {
	listener := bytes.LastIndex(logTail, []byte("RPC: listening on TCP port"))
	if listener < 0 {
		return ""
	}
	matches := fileAllocationPattern.FindAllSubmatch(logTail[listener:], -1)
	if len(matches) == 0 {
		return ""
	}
	prefix := strings.ToLower(string(matches[len(matches)-1][1]))
	matched := ""
	for _, item := range scanned {
		if item.Err == nil && item.Job.Execution != nil && strings.HasPrefix(item.Job.Execution.GID, prefix) {
			if matched != "" {
				return prefix
			}
			matched = item.Job.Execution.GID
		}
	}
	if matched != "" {
		return matched
	}
	return prefix
}

type SupervisorStatus interface {
	IsLoaded(context.Context) bool
	IsRunning(context.Context) bool
}

type StatusOptions struct {
	Paths            paths.Paths
	Service          SupervisorStatus
	RPCVersion       func(context.Context, state.State) (string, error)
	RPCProbeTimeout  time.Duration
	RPCSlowThreshold time.Duration
}

type StatusReport struct {
	ServiceInstalled  bool
	SupervisorLoaded  bool
	SupervisorRunning bool
	BinaryValid       bool
	RPCReachable      bool
	RPCSlow           bool
	RPCLatency        time.Duration
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
		probe := observeRPC(ctx, current, options.RPCVersion, options.RPCProbeTimeout, options.RPCSlowThreshold)
		report.RPCReachable = probe.Reachable
		report.RPCSlow = probe.Slow
		report.RPCLatency = probe.Latency
		report.Version = probe.Version
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
		if report.RPCSlow {
			rpcText = fmt.Sprintf("reachable (slow, %s)", formatLatency(report.RPCLatency))
		}
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

type rpcObservation struct {
	Reachable bool
	Slow      bool
	Latency   time.Duration
	Version   string
}

func observeRPC(
	ctx context.Context,
	current state.State,
	version func(context.Context, state.State) (string, error),
	timeout time.Duration,
	slowThreshold time.Duration,
) rpcObservation {
	if version == nil {
		return rpcObservation{}
	}
	if timeout <= 0 {
		timeout = defaultRPCProbeTimeout
	}
	if slowThreshold <= 0 {
		slowThreshold = defaultRPCSlowThreshold
	}
	probeCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	started := time.Now()
	value, err := version(probeCtx, current)
	elapsed := time.Since(started)
	if err != nil {
		return rpcObservation{Latency: elapsed}
	}
	return rpcObservation{
		Reachable: true,
		Slow:      elapsed > slowThreshold,
		Latency:   elapsed,
		Version:   value,
	}
}

func formatLatency(value time.Duration) string {
	switch {
	case value < time.Millisecond:
		return "<1ms"
	case value < time.Second:
		return value.Round(time.Millisecond).String()
	default:
		return value.Round(100 * time.Millisecond).String()
	}
}

func isExecutable(path string) bool {
	info, err := os.Stat(path)
	return err == nil && !info.IsDir() && info.Mode().Perm()&0o111 != 0
}

func fileExists(path string) bool {
	_, err := os.Stat(path)
	return err == nil
}
