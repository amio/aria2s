// Package app owns managed-download workflows and composition. Durable job
// intent is reconciled with aria2 observations here; filesystem and supervisor
// packages expose mechanisms but never decide lifecycle transitions.
package app

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"time"

	"github.com/amio/aria2s/internal/aria2"
	"github.com/amio/aria2s/internal/atomicfile"
	"github.com/amio/aria2s/internal/doctor"
	"github.com/amio/aria2s/internal/jobs"
	"github.com/amio/aria2s/internal/paths"
	managedruntime "github.com/amio/aria2s/internal/runtime"
	"github.com/amio/aria2s/internal/service"
	"github.com/amio/aria2s/internal/state"
	"golang.org/x/sys/unix"
)

type RPC interface {
	Version(context.Context, state.State) (string, error)
	AddURI(context.Context, state.State, string, aria2.AddOptions) (string, error)
	SaveSession(context.Context, state.State) error
	Shutdown(context.Context, state.State) error
}

type dashboardRPC interface {
	Version(context.Context, state.State) (string, error)
	ReadBatch(context.Context, state.State, aria2.ReadBatchQuery) (aria2.ReadBatch, error)
	TaskDetail(context.Context, state.State, string) (aria2.DownloadDetail, error)
	AddURI(context.Context, state.State, string, aria2.AddOptions) (string, error)
	Pause(context.Context, state.State, string) error
	Resume(context.Context, state.State, string) error
	RetrySource(context.Context, state.State, string) (aria2.RetrySource, error)
	AddURIs(context.Context, state.State, []string, aria2.AddOptions) (string, error)
	Remove(context.Context, state.State, string) error
	ClearStopped(context.Context, state.State, string) error
}

// StorageReconnecter exposes platform mount facts and a user-session reconnect
// request. The app remains responsible for deciding when the request is safe.
type StorageReconnecter interface {
	Observe(string) (reconnectURL string, mounted bool, err error)
	Request(context.Context, string) error
}

type Options struct {
	Paths                    paths.Paths
	DownloadDir              string
	LookPath                 func(string) (string, error)
	Abs                      func(string) (string, error)
	IsPortAvailable          func(int) bool
	GenerateSecret           func() (string, error)
	RenderService            func(state.State) (string, error)
	Service                  service.Backend
	RPC                      RPC
	RPCReadyTimeout          time.Duration
	RPCPollInterval          time.Duration
	RPCProbeTimeout          time.Duration
	RPCSlowThreshold         time.Duration
	DashboardReadTimeout     time.Duration
	DashboardMutationTimeout time.Duration
	StorageReconnecter       StorageReconnecter
}

const (
	defaultRPCOperationTimeout = 30 * time.Second
	defaultRPCSlowThreshold    = 2 * time.Second
	localRPCTransportTimeout   = 35 * time.Second
)

type App struct {
	options Options
}

func New(options Options) *App {
	if options.LookPath == nil {
		options.LookPath = exec.LookPath
	}
	if options.Abs == nil {
		options.Abs = filepath.Abs
	}
	if options.IsPortAvailable == nil {
		options.IsPortAvailable = IsPortAvailable
	}
	if options.GenerateSecret == nil {
		options.GenerateSecret = GenerateSecret
	}
	if options.RenderService == nil {
		options.RenderService = inferRenderService(options.Paths)
	}
	if options.Service == nil {
		options.Service = inferServiceBackend(options.Paths)
	}
	if options.RPC == nil {
		options.RPC = &LocalRPC{}
	}
	if options.RPCReadyTimeout == 0 {
		options.RPCReadyTimeout = defaultRPCOperationTimeout
	}
	if options.RPCPollInterval == 0 {
		options.RPCPollInterval = 100 * time.Millisecond
	}
	if options.RPCProbeTimeout == 0 {
		options.RPCProbeTimeout = defaultRPCOperationTimeout
	}
	if options.RPCSlowThreshold == 0 {
		options.RPCSlowThreshold = defaultRPCSlowThreshold
	}
	if options.DashboardReadTimeout == 0 {
		options.DashboardReadTimeout = defaultRPCOperationTimeout
	}
	if options.DashboardMutationTimeout == 0 {
		options.DashboardMutationTimeout = defaultRPCOperationTimeout
	}
	if options.StorageReconnecter == nil && inferServicePlatform(options.Paths) == "darwin" {
		options.StorageReconnecter = newPlatformStorageReconnecter()
	}
	return &App{options: options}
}

func Default() (*App, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return nil, err
	}
	options, err := defaultOptionsForOS(runtime.GOOS, home, os.Getuid(), service.ExecRunner{})
	if err != nil {
		return nil, err
	}
	return New(options), nil
}

func inferRenderService(servicePaths paths.Paths) func(state.State) (string, error) {
	switch inferServicePlatform(servicePaths) {
	case "linux":
		return service.RenderSystemdUnit
	case "darwin":
		return service.RenderLaunchAgent
	default:
		return func(state.State) (string, error) {
			return "", fmt.Errorf("unsupported service layout: %s", servicePaths.ServiceFile)
		}
	}
}

func inferServiceBackend(servicePaths paths.Paths) service.Backend {
	switch inferServicePlatform(servicePaths) {
	case "linux":
		return service.NewSystemdBackend(service.ExecRunner{}, servicePaths.ServiceName)
	case "darwin":
		return service.NewLaunchdBackend(service.ExecRunner{}, os.Getuid(), servicePaths.ServiceName, servicePaths.ServiceFile)
	default:
		return nil
	}
}

func inferServicePlatform(servicePaths paths.Paths) string {
	if strings.HasSuffix(servicePaths.ServiceFile, ".service") || strings.HasSuffix(servicePaths.ServiceName, ".service") {
		return "linux"
	}
	if strings.HasSuffix(servicePaths.ServiceFile, ".plist") || strings.Contains(servicePaths.ServiceFile, "LaunchAgents") {
		return "darwin"
	}
	switch runtime.GOOS {
	case "linux", "darwin":
		return runtime.GOOS
	default:
		return ""
	}
}

func defaultOptionsForOS(goos, home string, uid int, runner service.CommandRunner) (Options, error) {
	servicePaths, err := paths.NewForOS(goos, home)
	if err != nil {
		return Options{}, err
	}
	options := Options{
		Paths:       servicePaths,
		DownloadDir: filepath.Join(home, "Downloads"),
	}
	switch goos {
	case "darwin":
		options.RenderService = service.RenderLaunchAgent
		options.Service = service.NewLaunchdBackend(runner, uid, servicePaths.ServiceName, servicePaths.ServiceFile)
	case "linux":
		options.RenderService = service.RenderSystemdUnit
		options.Service = service.NewSystemdBackend(runner, servicePaths.ServiceName)
	default:
		return Options{}, fmt.Errorf("unsupported OS: %s", goos)
	}
	return options, nil
}

type InstallRequest struct {
	Start              bool
	DiscardLegacyTasks bool
}

func (app *App) Install(ctx context.Context, start bool) error {
	return app.InstallManaged(ctx, InstallRequest{Start: start})
}

// RebindManagedController refreshes the committed identity of an existing v2
// controller after its executable is atomically upgraded. A byte-identical
// supervisor artifact takes a controller-only path; a changed artifact remains
// owned by full runtime reconciliation.
func (app *App) RebindManagedController(ctx context.Context) (bool, error) {
	current, err := state.Load(app.options.Paths.StateFile)
	if errors.Is(err, os.ErrNotExist) || (err == nil && current.RuntimeSchemaVersion != 2) {
		return false, nil
	}
	if err != nil {
		return false, fmt.Errorf("load managed state: %w", err)
	}
	rebound := current
	rebound.ControllerPath, rebound.ControllerIdentity, err = currentControllerIdentity()
	if err != nil {
		return false, err
	}
	serviceFile, err := app.options.RenderService(rebound)
	if err != nil {
		return false, err
	}
	serviceHash := sha256.Sum256([]byte(serviceFile))
	serviceIdentity := hex.EncodeToString(serviceHash[:])
	serviceChanged, err := fileContentChanged(app.options.Paths.ServiceFile, serviceFile)
	if err != nil {
		return false, err
	}
	if !serviceChanged && current.ServiceIdentity == serviceIdentity {
		if current.ControllerPath != rebound.ControllerPath || current.ControllerIdentity != rebound.ControllerIdentity {
			if err := state.Save(app.options.Paths.StateFile, rebound); err != nil {
				return false, fmt.Errorf("save managed controller identity: %w", err)
			}
		}
		return true, nil
	}
	desired, err := app.desiredManagedState(current.Aria2cPath)
	if err != nil {
		return false, err
	}
	if _, err := app.reconcileManagedRuntime(ctx, desired); err != nil {
		return false, err
	}
	return true, nil
}

func (app *App) InstallManaged(ctx context.Context, request InstallRequest) error {
	if err := app.legacyInstallGate(ctx, request.DiscardLegacyTasks); err != nil {
		return err
	}
	desired, err := app.desiredManagedState("")
	if err != nil {
		return err
	}
	current, err := app.reconcileManagedRuntime(ctx, desired)
	if err != nil || !request.Start {
		return err
	}
	if err := app.startSupervisor(ctx); err != nil {
		return err
	}
	return app.waitForRPC(ctx, current)
}

func (app *App) desiredManagedState(storedExecutable string) (state.State, error) {
	aria2c := storedExecutable
	var err error
	if !isExecutable(aria2c) {
		aria2c, err = app.options.LookPath("aria2c")
		if err != nil {
			return state.State{}, fmt.Errorf("aria2c not found in PATH: %w", err)
		}
		aria2c, err = app.options.Abs(aria2c)
		if err != nil {
			return state.State{}, err
		}
	}
	current, err := state.Load(app.options.Paths.StateFile)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			current = state.State{}
		} else {
			return state.State{}, fmt.Errorf("load state: %w", err)
		}
	}
	desired := current
	controller, controllerIdentity, err := currentControllerIdentity()
	if err != nil {
		return state.State{}, err
	}
	desired.RuntimeSchemaVersion = 2
	desired.ControllerPath = controller
	desired.ControllerIdentity = controllerIdentity
	desired.Aria2cPath = aria2c
	desired.SessionPath = app.options.Paths.SessionFile
	desired.StartupInputPath = app.options.Paths.StartupInputFile
	desired.LogPath = app.options.Paths.LogFile
	desired.ErrorLogPath = app.options.Paths.ErrorLogFile
	desired.ServiceName = app.options.Paths.ServiceName
	if desired.RPCPort == 0 {
		desired.RPCPort, err = app.choosePort()
		if err != nil {
			return state.State{}, err
		}
	}
	if desired.RPCSecret == "" {
		desired.RPCSecret, err = app.options.GenerateSecret()
		if err != nil {
			return state.State{}, err
		}
	}
	return desired, nil
}

func (app *App) reconcileManagedRuntime(ctx context.Context, desired state.State) (state.State, error) {
	current, err := state.Load(app.options.Paths.StateFile)
	stateExists := true
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			current, stateExists = state.State{}, false
		} else {
			return state.State{}, fmt.Errorf("load state: %w", err)
		}
	}
	previous := current
	serviceFile, err := app.options.RenderService(desired)
	if err != nil {
		return state.State{}, err
	}
	serviceHash := sha256.Sum256([]byte(serviceFile))
	desired.ServiceIdentity = hex.EncodeToString(serviceHash[:])
	stateChanged := !stateExists || !sameState(current, desired)
	current = desired
	configNeedsCreate, err := fileMissing(app.options.Paths.ConfigFile)
	if err != nil {
		return state.State{}, err
	}
	sessionNeedsRepair := needs0600File(current.SessionPath)
	if !stateExists || previous.RuntimeSchemaVersion != 2 {
		empty, proofErr := stableEmptyFile(current.SessionPath)
		if proofErr != nil || !empty {
			return state.State{}, errors.New("InstallIncomplete: runtime-v2 session is not a fresh empty file")
		}
		sessionNeedsRepair = true
	}
	logDirNeedsCreate := !dirExists(filepath.Dir(current.LogPath))
	serviceLoaded := false
	serviceRunning := false
	if app.options.Service != nil {
		serviceLoaded = app.options.Service.IsLoaded(ctx)
		serviceRunning = app.options.Service.IsRunning(ctx)
	}
	serviceWasRunning := serviceRunning
	serviceChanged, err := fileContentChanged(app.options.Paths.ServiceFile, serviceFile)
	if err != nil {
		return state.State{}, err
	}
	if !stateChanged && !configNeedsCreate && !sessionNeedsRepair && !logDirNeedsCreate && !serviceChanged && serviceLoaded {
		return current, nil
	}
	if configNeedsCreate {
		if err := aria2.WriteConfig(app.options.Paths.ConfigFile, aria2.DefaultConfig(app.defaultDownloadDir())); err != nil {
			return state.State{}, err
		}
	}
	if sessionNeedsRepair {
		if err := touch0600(current.SessionPath); err != nil {
			return state.State{}, err
		}
	}
	if logDirNeedsCreate {
		if err := os.MkdirAll(filepath.Dir(current.LogPath), 0o755); err != nil {
			return state.State{}, err
		}
	}
	if app.options.Service != nil && serviceLoaded && serviceChanged {
		if serviceRunning {
			if previous.RuntimeSchemaVersion == 2 {
				if err := app.guardUnmanagedTasks(ctx, previous, false); err != nil {
					return state.State{}, err
				}
				if err := app.saveSession(ctx, previous); err != nil {
					return state.State{}, err
				}
			}
			if err := app.options.Service.Stop(ctx); err != nil {
				return state.State{}, err
			}
			serviceRunning = false
		}
		if err := app.options.Service.Uninstall(ctx); err != nil {
			return state.State{}, err
		}
		serviceLoaded = false
	}
	if serviceChanged {
		if err := os.MkdirAll(filepath.Dir(app.options.Paths.ServiceFile), 0o755); err != nil {
			return state.State{}, err
		}
		if err := atomicfile.Write(app.options.Paths.ServiceFile, []byte(serviceFile), 0o644); err != nil {
			return state.State{}, err
		}
	}
	installedService, err := os.ReadFile(app.options.Paths.ServiceFile)
	if err != nil || string(installedService) != serviceFile {
		return state.State{}, errors.New("InstallIncomplete: service artifact readback does not match rendered configuration")
	}
	if stateChanged {
		if err := state.Save(app.options.Paths.StateFile, current); err != nil {
			return state.State{}, err
		}
	}
	if app.options.Service != nil {
		if !serviceLoaded {
			if err := app.options.Service.Install(ctx); err != nil {
				return state.State{}, err
			}
		}
		if serviceWasRunning && serviceChanged {
			if err := app.options.Service.Start(ctx); err != nil {
				return state.State{}, err
			}
		}
	}
	return current, nil
}

const legacyV1RunningRecovery = `Option 1 — Keep existing tasks

Install the last v1 release:
  curl -fsSL https://raw.githubusercontent.com/amio/aria2s/main/install.sh | sh -s -- --version v0.4.0

Finish or remove every task, then stop v1:
  aria2s dashboard
  aria2s stop

Reinstall the latest version:
  curl -fsSL https://raw.githubusercontent.com/amio/aria2s/main/install.sh | sh

Option 2 — Discard existing tasks

Run:
  aria2s install --discard-legacy-tasks

Discard stops the v1 service and installs v2 without importing its tasks.`

const legacyV1SessionRecovery = `The stopped v1 service still has saved restart entries. V2 cannot determine
whether they are unfinished tasks or stale entries retained by aria2 after
Dashboard cleanup.

Option 1 — The v1 Dashboard was empty before stop

Using the v2 binary that printed this message, run:
  aria2s install --discard-legacy-tasks

This confirms that the legacy restart entries can be ignored. It does not
delete legacy files or downloaded payloads.

Option 2 — Inspect or keep existing tasks

Install the last v1 release:
  curl -fsSL https://raw.githubusercontent.com/amio/aria2s/main/install.sh | sh -s -- --version v0.4.0

Inspect or finish the tasks, then stop v1:
  aria2s dashboard
  aria2s stop

If the Dashboard was empty but aria2 retained restart entries, use Option 1.`

const legacyV1ReadyRecovery = `The v1 service is stopped and its saved session is empty.

Install and start the latest version:
  curl -fsSL https://raw.githubusercontent.com/amio/aria2s/main/install.sh | sh`

func legacyRuntimeError(code, summary, recovery string) error {
	return fmt.Errorf("%s: %s\n\n%s", code, summary, recovery)
}

func legacyReadyToInstallError() error {
	return fmt.Errorf("UpgradeRequired: managed runtime v1 is stopped and has no saved tasks; v2 is ready to install\n\n%s", legacyV1ReadyRecovery)
}

func (app *App) legacyInstallGate(ctx context.Context, discard bool) error {
	current, err := state.Load(app.options.Paths.StateFile)
	if err == nil && current.RuntimeSchemaVersion == 2 {
		return nil
	}
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("load legacy state: %w", err)
	}
	if !discard && app.options.Service != nil && app.options.Service.IsRunning(ctx) {
		return legacyRuntimeError("UpgradeRequired", "managed runtime v1 is still running", legacyV1RunningRecovery)
	}
	if !discard {
		empty, proofErr := stableEmptyFile(app.options.Paths.LegacySessionFile)
		if proofErr != nil || !empty {
			return legacyRuntimeError("LegacySessionPresent", "the stopped v1 runtime still has saved restart entries", legacyV1SessionRecovery)
		}
	}
	if app.options.Service != nil {
		// Re-read after the session proof so a late legacy start cannot escape the gate.
		loaded := app.options.Service.IsLoaded(ctx)
		running := app.options.Service.IsRunning(ctx)
		if running && !discard {
			return legacyRuntimeError("UpgradeRequired", "managed runtime v1 started while upgrading", legacyV1RunningRecovery)
		}
		if running && !loaded {
			if err := app.options.Service.Stop(ctx); err != nil {
				return fmt.Errorf("stop legacy supervisor: %w", err)
			}
		}
		if loaded {
			if err := app.options.Service.Uninstall(ctx); err != nil {
				return fmt.Errorf("disable legacy supervisor: %w", err)
			}
		}
		if app.options.Service.IsLoaded(ctx) || app.options.Service.IsRunning(ctx) {
			return errors.New("InstallIncomplete: legacy supervisor could not be disabled")
		}
	}
	if !discard {
		empty, proofErr := stableEmptyFile(app.options.Paths.LegacySessionFile)
		if proofErr != nil || !empty {
			return legacyRuntimeError("LegacySessionPresent", "the v1 session changed while disabling its service", legacyV1SessionRecovery)
		}
	}
	return nil
}

func stableEmptyFile(path string) (bool, error) {
	before, err := os.Lstat(path)
	if errors.Is(err, os.ErrNotExist) {
		return true, nil
	}
	if err != nil || !before.Mode().IsRegular() || before.Mode()&os.ModeSymlink != 0 || before.Size() != 0 {
		return false, err
	}
	fd, err := unix.Open(path, unix.O_RDONLY|unix.O_NONBLOCK|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0)
	if err != nil {
		return false, err
	}
	file := os.NewFile(uintptr(fd), path)
	after, statErr := file.Stat()
	closeErr := file.Close()
	if statErr != nil {
		return false, statErr
	}
	if closeErr != nil {
		return false, closeErr
	}
	current, pathErr := os.Lstat(path)
	if pathErr != nil {
		return false, pathErr
	}
	return after.Mode().IsRegular() && after.Mode()&os.ModeSymlink == 0 && after.Size() == 0 &&
		os.SameFile(before, after) && os.SameFile(after, current) &&
		before.ModTime() == after.ModTime() && after.ModTime() == current.ModTime(), nil
}

func fileIdentity(path string) (string, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return "", err
	}
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:]), nil
}

func currentControllerIdentity() (string, string, error) {
	controller, err := os.Executable()
	if err != nil {
		return "", "", err
	}
	controller, err = filepath.EvalSymlinks(controller)
	if err != nil {
		return "", "", err
	}
	identity, err := fileIdentity(controller)
	if err != nil {
		return "", "", err
	}
	return controller, identity, nil
}

// inspectDashboard validates the runtime committed by install or update. A
// running aria2c is already authoritative for Dashboard; on-disk bootstrap
// artifacts are required only when Dashboard needs to start the supervisor.
// The invoking CLI must never derive or publish replacement service metadata.
func (app *App) inspectDashboard(ctx context.Context) (state.State, bool, error) {
	current, err := state.Load(app.options.Paths.StateFile)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return state.State{}, false, errors.New("UpgradeRequired: run `aria2s install` before opening Dashboard")
		}
		return state.State{}, false, fmt.Errorf("load state: %w", err)
	}
	if current.RuntimeSchemaVersion != 2 {
		if app.options.Service != nil && app.options.Service.IsRunning(ctx) {
			return current, false, legacyRuntimeError("UpgradeRequired", "managed runtime v1 is still running", legacyV1RunningRecovery)
		}
		empty, proofErr := stableEmptyFile(app.options.Paths.LegacySessionFile)
		if proofErr != nil || !empty {
			return current, false, legacyRuntimeError("LegacySessionPresent", "managed runtime v1 is stopped, but aria2 retained saved restart entries", legacyV1SessionRecovery)
		}
		return current, false, legacyReadyToInstallError()
	}
	if current.SessionPath != app.options.Paths.SessionFile || current.StartupInputPath != app.options.Paths.StartupInputFile ||
		current.LogPath != app.options.Paths.LogFile || current.ErrorLogPath != app.options.Paths.ErrorLogFile ||
		current.ServiceName != app.options.Paths.ServiceName || current.RPCPort == 0 || current.RPCSecret == "" {
		return current, false, dashboardInstallRequired("managed runtime state does not match the current platform layout")
	}
	serviceRunning := app.options.Service != nil && app.options.Service.IsRunning(ctx)
	if serviceRunning {
		return current, true, nil
	}
	if !isExecutable(current.Aria2cPath) {
		return current, false, dashboardInstallRequired("stored aria2c path is not executable")
	}
	serviceInfo, err := os.Lstat(app.options.Paths.ServiceFile)
	if err != nil || !serviceInfo.Mode().IsRegular() || serviceInfo.Mode()&os.ModeSymlink != 0 {
		return current, false, dashboardInstallRequired("service artifact is missing or invalid")
	}
	serviceData, err := os.ReadFile(app.options.Paths.ServiceFile)
	if err != nil {
		return current, false, dashboardInstallRequired("service artifact cannot be read")
	}
	serviceHash := sha256.Sum256(serviceData)
	if current.ServiceIdentity == "" || current.ServiceIdentity != hex.EncodeToString(serviceHash[:]) {
		return current, false, dashboardInstallRequired("service artifact identity does not match committed state")
	}
	if !isExecutable(current.ControllerPath) {
		return current, false, dashboardInstallRequired("controller executable is missing or invalid")
	}
	controllerIdentity, identityErr := fileIdentity(current.ControllerPath)
	if identityErr != nil || current.ControllerIdentity == "" || controllerIdentity != current.ControllerIdentity {
		return current, false, dashboardInstallRequired("controller executable identity does not match committed state")
	}
	if needs0600File(current.SessionPath) {
		return current, false, dashboardInstallRequired("managed session file is missing or invalid")
	}
	if !dirExists(filepath.Dir(current.LogPath)) {
		return current, false, dashboardInstallRequired("managed log directory is missing")
	}
	return current, false, nil
}

func dashboardInstallRequired(reason string) error {
	return fmt.Errorf("InstallIncomplete: %s; run `aria2s install`", reason)
}

/** PrepareDashboard validates installed runtime state and starts the supervisor without waiting for RPC. */
func (app *App) PrepareDashboard(ctx context.Context) (*DashboardSession, error) {
	current, running, err := app.inspectDashboard(ctx)
	if err != nil {
		return nil, err
	}
	if !running {
		if err := app.startSupervisor(ctx); err != nil {
			return nil, err
		}
	}
	rpc, ok := app.options.RPC.(dashboardRPC)
	if !ok {
		return nil, errors.New("configured RPC client does not support dashboard task management")
	}
	return &DashboardSession{app: app, identity: current, rpc: rpc}, nil
}

func (app *App) startSupervisor(ctx context.Context) error {
	if app.options.Service != nil && !app.options.Service.IsRunning(ctx) {
		_ = clearStartupProgress(app.options.Paths.StartupProgressFile)
		return app.options.Service.Start(ctx)
	}
	return nil
}

func (app *App) Uninstall(ctx context.Context) error {
	if app.options.Service != nil && app.options.Service.IsLoaded(ctx) {
		if err := app.options.Service.Uninstall(ctx); err != nil {
			return err
		}
	}
	if err := os.Remove(app.options.Paths.ServiceFile); err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	return nil
}

func (app *App) Start(ctx context.Context) error {
	current, err := app.preflightLifecycle()
	if err != nil {
		return err
	}
	if err := app.startSupervisor(ctx); err != nil {
		return err
	}
	return app.waitForRPC(ctx, current)
}

func (app *App) Stop(ctx context.Context) error {
	return app.StopManaged(ctx, StopOptions{})
}

type StopOptions struct {
	DiscardUnmanagedTasks bool
}

func (app *App) StopManaged(ctx context.Context, options StopOptions) error {
	var saveErr error
	if app.options.Service.IsRunning(ctx) {
		current, err := state.Load(app.options.Paths.StateFile)
		if err != nil {
			saveErr = err
		} else if err := app.guardUnmanagedTasks(ctx, current, options.DiscardUnmanagedTasks); err != nil {
			return err
		} else if err := app.saveSession(ctx, current); err != nil {
			if !errors.Is(err, aria2.ErrTransportUnavailable) {
				saveErr = err
			}
		}
	}
	if err := app.options.Service.Stop(ctx); err != nil {
		return err
	}
	return saveErr
}

func (app *App) guardUnmanagedTasks(ctx context.Context, current state.State, discard bool) error {
	if current.RuntimeSchemaVersion != 2 || discard {
		return nil
	}
	type censusRPC interface {
		CompleteCensus(context.Context, state.State) ([]aria2.LifecycleStatus, error)
	}
	rpc, ok := app.options.RPC.(censusRPC)
	if !ok {
		return errors.New("UnmanagedTaskCensusUnavailable: refusing lifecycle stop without a complete census")
	}
	native, err := rpc.CompleteCensus(ctx, current)
	if err != nil {
		return err
	}
	scanned, err := jobs.New(app.options.Paths.StateDir).Scan()
	if err != nil {
		return err
	}
	managed := make(map[string]struct{}, len(scanned))
	for _, job := range scanned {
		if job.Err == nil && job.Job.Execution != nil {
			managed[job.Job.Execution.GID] = struct{}{}
		}
	}
	for _, task := range native {
		if _, ok := managed[task.GID]; ok {
			continue
		}
		if task.Status == "active" || task.CompletedLength < task.TotalLength {
			return errors.New("UnmanagedTasksWouldBeLost: stop refused; pass --discard-unmanaged-tasks to acknowledge")
		}
	}
	return nil
}

func (app *App) Restart(ctx context.Context) error {
	return app.RestartManaged(ctx, StopOptions{})
}

// RecoverRPC performs an explicitly acknowledged managed-only restart. It
// retains durable managed state and disables file preallocation for the new
// process so startup work cannot block JSON-RPC recovery.
func (app *App) RecoverRPC(ctx context.Context, discardUnmanaged bool) error {
	current, err := app.preflightLifecycle()
	if err != nil {
		return err
	}
	if _, err := app.options.RPC.Version(ctx, current); err == nil {
		return nil
	}
	if !discardUnmanaged {
		return errors.New("UnmanagedTaskCensusUnavailable: RPC is blocked; rerun with --discard-unmanaged-tasks to acknowledge a managed-only recovery")
	}
	if err := managedruntime.EnableSafeStartup(app.options.Paths.SafeStartupFile); err != nil {
		return fmt.Errorf("enable safe startup: %w", err)
	}
	if app.options.Service.IsRunning(ctx) || app.options.Service.IsLoaded(ctx) {
		if err := app.options.Service.Stop(ctx); err != nil {
			_ = managedruntime.DisableSafeStartup(app.options.Paths.SafeStartupFile)
			return fmt.Errorf("stop blocked service: %w", err)
		}
	}
	if err := app.options.Service.Start(ctx); err != nil {
		return fmt.Errorf("start safe service: %w", err)
	}
	if err := app.waitForRPC(ctx, current); err != nil {
		return fmt.Errorf("safe startup did not restore RPC: %w", err)
	}
	if err := managedruntime.DisableSafeStartup(app.options.Paths.SafeStartupFile); err != nil {
		return fmt.Errorf("RPC recovered but safe-startup marker cleanup failed: %w", err)
	}
	return nil
}

func (app *App) RestartManaged(ctx context.Context, options StopOptions) error {
	current, err := app.preflightLifecycle()
	if err != nil {
		return err
	}
	return app.restartServiceGracefully(ctx, current, options.DiscardUnmanagedTasks)
}

func (app *App) restartServiceGracefully(ctx context.Context, current state.State, discardUnmanaged bool) error {
	if app.options.Service.IsRunning(ctx) {
		if err := app.guardUnmanagedTasks(ctx, current, discardUnmanaged); err != nil {
			return err
		}
		if err := app.saveSession(ctx, current); err != nil {
			if !errors.Is(err, aria2.ErrTransportUnavailable) {
				return err
			}
		}
		if err := app.options.Service.Stop(ctx); err != nil {
			return err
		}
	}
	if err := app.options.Service.Start(ctx); err != nil {
		return err
	}
	return app.waitForRPC(ctx, current)
}

func (app *App) saveSession(ctx context.Context, current state.State) error {
	if err := app.options.RPC.SaveSession(ctx, current); err != nil {
		return fmt.Errorf("save session: %w", err)
	}
	return nil
}

func (app *App) Status(ctx context.Context) doctor.StatusReport {
	return doctor.Status(ctx, doctor.StatusOptions{
		Paths:            app.options.Paths,
		Service:          app.options.Service,
		RPCProbeTimeout:  app.options.RPCProbeTimeout,
		RPCSlowThreshold: app.options.RPCSlowThreshold,
		RPCVersion: func(ctx context.Context, current state.State) (string, error) {
			return app.options.RPC.Version(ctx, current)
		},
	})
}

func (app *App) Doctor(ctx context.Context) doctor.Report {
	return doctor.Check(ctx, doctor.Options{
		Paths:            app.options.Paths,
		IsPortAvailable:  app.options.IsPortAvailable,
		Service:          app.options.Service,
		RPCProbeTimeout:  app.options.RPCProbeTimeout,
		RPCSlowThreshold: app.options.RPCSlowThreshold,
		RPCVersion: func(ctx context.Context, current state.State) (string, error) {
			return app.options.RPC.Version(ctx, current)
		},
	})
}

func (app *App) Add(ctx context.Context, uri string, opts aria2.AddOptions) (string, error) {
	current, err := state.Load(app.options.Paths.StateFile)
	if err != nil {
		return "", err
	}
	gid, err := app.options.RPC.AddURI(ctx, current, uri, opts)
	if err != nil {
		return "", err
	}
	if opts.Dir != "" {
		_ = app.recordDir(opts.Dir)
	}
	return gid, nil
}

func (app *App) DefaultDir() string {
	return app.defaultDownloadDir()
}

func (app *App) defaultDownloadDir() string {
	if app.options.DownloadDir != "" {
		return app.options.DownloadDir
	}
	return filepath.Join(filepath.Dir(filepath.Dir(app.options.Paths.ConfigFile)), "Downloads")
}

func (app *App) RecentDirs(context.Context) ([]string, error) {
	current, err := state.Load(app.options.Paths.StateFile)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil, nil
		}
		return nil, err
	}
	return current.RecentDirs, nil
}

func (app *App) DeleteRecentDir(_ context.Context, dir string) error {
	if dir == "" {
		return nil
	}
	current, err := state.Load(app.options.Paths.StateFile)
	if err != nil {
		return err
	}
	filtered := make([]string, 0, len(current.RecentDirs))
	removed := false
	for _, existing := range current.RecentDirs {
		if existing == dir {
			removed = true
			continue
		}
		filtered = append(filtered, existing)
	}
	if !removed {
		return nil
	}
	current.RecentDirs = filtered
	return state.Save(app.options.Paths.StateFile, current)
}

func (app *App) recordDir(dir string) error {
	if dir == "" {
		return nil
	}
	current, err := state.Load(app.options.Paths.StateFile)
	if err != nil {
		return err
	}
	filtered := make([]string, 0, len(current.RecentDirs)+1)
	for _, existing := range current.RecentDirs {
		if existing != dir {
			filtered = append(filtered, existing)
		}
	}
	filtered = append([]string{dir}, filtered...)
	const recentDirLimit = 8
	if len(filtered) > recentDirLimit {
		filtered = filtered[:recentDirLimit]
	}
	current.RecentDirs = filtered
	return state.Save(app.options.Paths.StateFile, current)
}

func (app *App) Paths() paths.Paths {
	return app.options.Paths
}

func (app *App) preflightLifecycle() (state.State, error) {
	current, err := state.Load(app.options.Paths.StateFile)
	if err != nil {
		return state.State{}, fmt.Errorf("load state: %w", err)
	}
	if !isExecutable(current.Aria2cPath) {
		return state.State{}, fmt.Errorf("stored aria2c path is not executable: %s", current.Aria2cPath)
	}
	if current.RuntimeSchemaVersion != 2 {
		return state.State{}, errors.New("UpgradeRequired: install managed runtime v2 before controlling the service")
	}
	return current, nil
}

func (app *App) waitForRPC(ctx context.Context, current state.State) error {
	readyCtx, cancel := context.WithTimeout(ctx, app.options.RPCReadyTimeout)
	defer cancel()
	var lastErr error
	for {
		if _, err := app.options.RPC.Version(readyCtx, current); err == nil {
			_ = clearStartupProgress(app.options.Paths.StartupProgressFile)
			return nil
		} else {
			lastErr = err
		}
		if ctx.Err() != nil {
			return ctx.Err()
		}
		if readyCtx.Err() != nil {
			return app.rpcReadyError(current, lastErr)
		}
		timer := time.NewTimer(app.options.RPCPollInterval)
		select {
		case <-ctx.Done():
			timer.Stop()
			return ctx.Err()
		case <-readyCtx.Done():
			timer.Stop()
			return app.rpcReadyError(current, lastErr)
		case <-timer.C:
		}
	}
}

func (app *App) rpcReadyError(current state.State, cause error) error {
	return fmt.Errorf(
		"aria2 did not become reachable within %s at %s: %w\nCheck logs at %s or run `aria2s doctor` for diagnostics",
		app.options.RPCReadyTimeout,
		endpoint(current.RPCPort),
		cause,
		current.LogPath,
	)
}

func (app *App) choosePort() (int, error) {
	if app.options.IsPortAvailable(6800) {
		return 6800, nil
	}
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return 0, err
	}
	defer listener.Close()
	return listener.Addr().(*net.TCPAddr).Port, nil
}

func fileContentChanged(path, content string) (bool, error) {
	info, err := os.Lstat(path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return true, nil
		}
		return false, err
	}
	if !info.Mode().IsRegular() || info.Mode()&os.ModeSymlink != 0 {
		return true, nil
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return false, err
	}
	return string(data) != content, nil
}

func fileMissing(path string) (bool, error) {
	_, err := os.Stat(path)
	if err == nil {
		return false, nil
	}
	if errors.Is(err, os.ErrNotExist) {
		return true, nil
	}
	return false, err
}

func isExecutable(path string) bool {
	info, err := os.Stat(path)
	return err == nil && !info.IsDir() && info.Mode().Perm()&0o111 != 0
}

type LocalRPC struct {
	httpOnce sync.Once
	http     *http.Client
	clients  sync.Map // key: rpcCacheKey, value: *aria2.RPCClient
}

func (r *LocalRPC) httpClient() *http.Client {
	r.httpOnce.Do(func() {
		transport := http.DefaultTransport.(*http.Transport).Clone()
		// The managed endpoint is loopback-only. Proxying it can leak the RPC
		// secret and makes local health depend on an unrelated proxy process.
		transport.Proxy = nil
		r.http = &http.Client{
			Timeout:   localRPCTransportTimeout,
			Transport: transport,
		}
	})
	return r.http
}

func (r *LocalRPC) rpcClient(current state.State) *aria2.RPCClient {
	key := rpcCacheKey(current.RPCPort, current.RPCSecret)
	if cached, ok := r.clients.Load(key); ok {
		return cached.(*aria2.RPCClient)
	}
	client := aria2.NewRPCClient(endpoint(current.RPCPort), current.RPCSecret, r.httpClient())
	actual, _ := r.clients.LoadOrStore(key, client)
	return actual.(*aria2.RPCClient)
}

func (r *LocalRPC) Version(ctx context.Context, current state.State) (string, error) {
	return r.rpcClient(current).Version(ctx)
}

func (r *LocalRPC) AddURI(ctx context.Context, current state.State, uri string, opts aria2.AddOptions) (string, error) {
	return r.rpcClient(current).AddURI(ctx, uri, opts)
}

func (r *LocalRPC) AddTorrent(ctx context.Context, current state.State, metainfo []byte, opts aria2.AddOptions) (string, error) {
	return r.rpcClient(current).AddTorrent(ctx, metainfo, opts)
}

func (r *LocalRPC) LifecycleStatus(ctx context.Context, current state.State, gid string) (aria2.LifecycleStatus, error) {
	return r.rpcClient(current).LifecycleStatus(ctx, gid)
}

func (r *LocalRPC) CompleteCensus(ctx context.Context, current state.State) ([]aria2.LifecycleStatus, error) {
	return r.rpcClient(current).CompleteCensus(ctx)
}

func (r *LocalRPC) ForceRemove(ctx context.Context, current state.State, gid string) error {
	return r.rpcClient(current).ForceRemove(ctx, gid)
}

func (r *LocalRPC) RemoveDownloadResult(ctx context.Context, current state.State, gid string) error {
	return r.rpcClient(current).RemoveDownloadResult(ctx, gid)
}

func (r *LocalRPC) SaveSession(ctx context.Context, current state.State) error {
	return aria2.WrapTransportError(r.rpcClient(current).SaveSession(ctx))
}

func (r *LocalRPC) Shutdown(ctx context.Context, current state.State) error {
	return aria2.WrapTransportError(r.rpcClient(current).Shutdown(ctx))
}

func (r *LocalRPC) ReadBatch(ctx context.Context, current state.State, query aria2.ReadBatchQuery) (aria2.ReadBatch, error) {
	return r.rpcClient(current).ReadBatch(ctx, query)
}

func (r *LocalRPC) TaskDetail(ctx context.Context, current state.State, gid string) (aria2.DownloadDetail, error) {
	return r.rpcClient(current).TaskDetail(ctx, gid)
}

func (r *LocalRPC) Pause(ctx context.Context, current state.State, gid string) error {
	return r.rpcClient(current).Pause(ctx, gid)
}

func (r *LocalRPC) Resume(ctx context.Context, current state.State, gid string) error {
	return r.rpcClient(current).Resume(ctx, gid)
}

func (r *LocalRPC) RetrySource(ctx context.Context, current state.State, gid string) (aria2.RetrySource, error) {
	return r.rpcClient(current).RetrySource(ctx, gid)
}

func (r *LocalRPC) AddURIs(ctx context.Context, current state.State, uris []string, opts aria2.AddOptions) (string, error) {
	return r.rpcClient(current).AddURIs(ctx, uris, opts)
}

func (r *LocalRPC) Remove(ctx context.Context, current state.State, gid string) error {
	return r.rpcClient(current).Remove(ctx, gid)
}

func (r *LocalRPC) ClearStopped(ctx context.Context, current state.State, gid string) error {
	return r.rpcClient(current).RemoveDownloadResult(ctx, gid)
}

func rpcCacheKey(port int, secret string) string {
	return endpoint(port) + "\x00" + secret
}

func endpoint(port int) string {
	return fmt.Sprintf("http://127.0.0.1:%d/jsonrpc", port)
}

func IsPortAvailable(port int) bool {
	listener, err := net.Listen("tcp", fmt.Sprintf("127.0.0.1:%d", port))
	if err != nil {
		return false
	}
	_ = listener.Close()
	return true
}

func GenerateSecret() (string, error) {
	var data [32]byte
	if _, err := rand.Read(data[:]); err != nil {
		return "", err
	}
	return hex.EncodeToString(data[:]), nil
}

func touch0600(path string) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	file, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY, 0o600)
	if err != nil {
		return err
	}
	if err := file.Chmod(0o600); err != nil {
		file.Close()
		return err
	}
	if err := file.Sync(); err != nil {
		file.Close()
		return err
	}
	if err := file.Close(); err != nil {
		return err
	}
	return atomicfile.SyncDirectory(filepath.Dir(path))
}

func sameState(left, right state.State) bool {
	if left.RuntimeSchemaVersion != right.RuntimeSchemaVersion ||
		left.ControllerPath != right.ControllerPath ||
		left.ControllerIdentity != right.ControllerIdentity ||
		left.ServiceIdentity != right.ServiceIdentity ||
		left.Aria2cPath != right.Aria2cPath ||
		left.RPCPort != right.RPCPort ||
		left.RPCSecret != right.RPCSecret ||
		left.SessionPath != right.SessionPath || left.StartupInputPath != right.StartupInputPath ||
		left.LogPath != right.LogPath ||
		left.ErrorLogPath != right.ErrorLogPath ||
		left.ServiceName != right.ServiceName ||
		len(left.RecentDirs) != len(right.RecentDirs) {
		return false
	}
	for index := range left.RecentDirs {
		if left.RecentDirs[index] != right.RecentDirs[index] {
			return false
		}
	}
	return true
}

func needs0600File(path string) bool {
	info, err := os.Stat(path)
	if err != nil {
		return true
	}
	return info.IsDir() || info.Mode().Perm() != 0o600
}

func dirExists(path string) bool {
	info, err := os.Stat(path)
	return err == nil && info.IsDir()
}
