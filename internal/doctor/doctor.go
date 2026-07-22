package doctor

import (
	"context"
	"fmt"
	"os"
	"strings"

	"github.com/amio/aria2s/internal/paths"
	"github.com/amio/aria2s/internal/state"
)

type Issue struct {
	// Message is the short stable label, used by tests and any scripts
	// parsing `- <label>` lines. Keep it unchanged when editing wording.
	Message string
	// Detail explains what the issue means and its likely cause.
	Detail string
	// Remedy is the concrete next step, typically a single command.
	Remedy string
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
		return Report{
			Healthy: false,
			Issues: []Issue{{
				Message: "state file missing or unreadable",
				Detail:  fmt.Sprintf("aria2s can't read its install state at %s.", options.Paths.StateFile),
				Remedy:  "Run `aria2s install` to set up aria2s again.",
			}},
		}
	}
	endpoint := fmt.Sprintf("http://127.0.0.1:%d/jsonrpc", current.RPCPort)
	if !isExecutable(current.Aria2cPath) {
		issues = append(issues, Issue{
			Message: "missing aria2c binary",
			Detail:  fmt.Sprintf("The aria2c binary recorded in state is not executable: %s.", current.Aria2cPath),
			Remedy:  "Run `aria2s install` to reinstall aria2c.",
		})
	}
	if !fileExists(options.Paths.ServiceFile) {
		issues = append(issues, Issue{
			Message: "missing service file",
			Detail:  "The launchd/systemd unit is absent.",
			Remedy:  "Run `aria2s install` to register the service.",
		})
	}
	if options.Service != nil && !options.Service.IsLoaded(ctx) {
		issues = append(issues, Issue{
			Message: "supervisor unloaded",
			Detail:  "The service unit exists but is not loaded by the supervisor.",
			Remedy:  "Run `aria2s start`.",
		})
	}
	if options.Service != nil && options.Service.IsLoaded(ctx) && !options.Service.IsRunning(ctx) {
		issues = append(issues, Issue{
			Message: "supervisor not running",
			Detail:  "The supervisor loaded the unit but aria2 is not running.",
			Remedy:  "Run `aria2s start`; if it keeps stopping, check logs with `aria2s logs`.",
		})
	}
	if options.RPCReachable != nil && !options.RPCReachable(ctx, current) {
		issues = append(issues, Issue{
			Message: "RPC unreachable",
			Detail:  fmt.Sprintf("aria2 is not responding to JSON-RPC at %s. The service may be stopped, starting up, or crashed.", endpoint),
			Remedy:  "Run `aria2s start` (or `aria2s restart` if it was running). If it still fails, see `aria2s logs`.",
		})
	}
	if options.IsPortAvailable != nil && !options.IsPortAvailable(current.RPCPort) && !managedServiceOwnsPort(ctx, options, current) {
		issues = append(issues, Issue{
			Message: "port conflict",
			Detail:  fmt.Sprintf("Port %d is already in use, but the managed aria2 service is not the one serving it. A crashed aria2, another aria2 instance, or an unrelated process may be holding it.", current.RPCPort),
			Remedy:  fmt.Sprintf("Run `aria2s restart`. If that fails, find the holder with `lsof -i :%d` and stop it, then `aria2s start`.", current.RPCPort),
		})
	}
	return Report{Healthy: len(issues) == 0, Issues: issues}
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
