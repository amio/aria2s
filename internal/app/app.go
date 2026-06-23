package app

import (
	"context"
	"crypto/rand"
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
	"github.com/amio/aria2s/internal/doctor"
	"github.com/amio/aria2s/internal/paths"
	"github.com/amio/aria2s/internal/service"
	"github.com/amio/aria2s/internal/state"
)

type RPC interface {
	Version(context.Context, state.State) (string, error)
	AddURI(context.Context, state.State, string, aria2.AddOptions) (string, error)
	SaveSession(context.Context, state.State) error
	Shutdown(context.Context, state.State) error
}

type consoleRPC interface {
	ListDownloads(context.Context, state.State, aria2.ListOptions) (aria2.DownloadSnapshot, error)
	TaskDetail(context.Context, state.State, string) (aria2.DownloadDetail, error)
	Pause(context.Context, state.State, string) error
	Resume(context.Context, state.State, string) error
	Remove(context.Context, state.State, string) error
	ClearStopped(context.Context, state.State, string) error
}

type Options struct {
	Paths           paths.Paths
	DownloadDir     string
	LookPath        func(string) (string, error)
	Abs             func(string) (string, error)
	IsPortAvailable func(int) bool
	GenerateSecret  func() (string, error)
	RenderService   func(state.State) (string, error)
	Service         service.Backend
	RPC             RPC
	RPCReadyTimeout time.Duration
	RPCPollInterval time.Duration
	ShutdownTimeout time.Duration
	ConsoleRunner   func(*App) error
}

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
		options.RPCReadyTimeout = 5 * time.Second
	}
	if options.RPCPollInterval == 0 {
		options.RPCPollInterval = 100 * time.Millisecond
	}
	if options.ShutdownTimeout == 0 {
		options.ShutdownTimeout = time.Minute
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

func (app *App) Install(ctx context.Context, start bool) error {
	aria2c, err := app.options.LookPath("aria2c")
	if err != nil {
		return fmt.Errorf("aria2c not found in PATH: %w", err)
	}
	aria2c, err = app.options.Abs(aria2c)
	if err != nil {
		return err
	}
	current, err := state.Load(app.options.Paths.StateFile)
	stateExists := true
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			current = state.State{}
			stateExists = false
		} else {
			return fmt.Errorf("load state: %w", err)
		}
	}
	desired := current
	desired.Aria2cPath = aria2c
	desired.ConfigPath = app.options.Paths.ConfigFile
	desired.SessionPath = app.options.Paths.SessionFile
	desired.LogPath = app.options.Paths.LogFile
	desired.ErrorLogPath = app.options.Paths.ErrorLogFile
	desired.ServiceName = app.options.Paths.ServiceName
	if desired.RPCPort == 0 {
		desired.RPCPort, err = app.choosePort()
		if err != nil {
			return err
		}
	}
	if desired.RPCSecret == "" {
		desired.RPCSecret, err = app.options.GenerateSecret()
		if err != nil {
			return err
		}
	}
	stateChanged := !stateExists || !sameState(current, desired)
	current = desired
	existingConfig, err := aria2.ReadConfig(current.ConfigPath)
	if err != nil {
		return err
	}
	managedConfigChanged := aria2.HasManagedDrift(existingConfig, current)
	downloadDir := app.options.DownloadDir
	if downloadDir == "" {
		downloadDir = filepath.Join(filepath.Dir(filepath.Dir(filepath.Dir(current.ConfigPath))), "Downloads")
	}
	content := aria2.BuildConfig(aria2.ManagedConfig{
		RPCPort:     current.RPCPort,
		RPCSecret:   current.RPCSecret,
		SessionFile: current.SessionPath,
		DownloadDir: downloadDir,
	}, existingConfig)
	configChanged, err := fileContentChanged(current.ConfigPath, content)
	if err != nil {
		return err
	}
	sessionNeedsRepair := needs0600File(current.SessionPath)
	logDirNeedsCreate := !dirExists(filepath.Dir(current.LogPath))
	serviceFile, err := app.options.RenderService(current)
	if err != nil {
		return err
	}
	serviceLoaded := false
	serviceRunning := false
	if app.options.Service != nil {
		serviceLoaded = app.options.Service.IsLoaded(ctx)
		serviceRunning = app.options.Service.IsRunning(ctx)
	}
	serviceWasRunning := serviceRunning
	serviceChanged, err := fileContentChanged(app.options.Paths.ServiceFile, serviceFile)
	if err != nil {
		return err
	}
	if !stateChanged && !configChanged && !sessionNeedsRepair && !logDirNeedsCreate && !serviceChanged && serviceLoaded {
		if !start {
			return nil
		}
		if serviceRunning {
			return app.waitForRPC(ctx, current)
		}
	}
	if stateChanged {
		if err := state.Save(app.options.Paths.StateFile, current); err != nil {
			return err
		}
	}
	if configChanged {
		if err := aria2.WriteConfig(current.ConfigPath, content); err != nil {
			return err
		}
	}
	if sessionNeedsRepair {
		if err := touch0600(current.SessionPath); err != nil {
			return err
		}
	}
	if logDirNeedsCreate {
		if err := os.MkdirAll(filepath.Dir(current.LogPath), 0o755); err != nil {
			return err
		}
	}
	if app.options.Service != nil && serviceLoaded && serviceChanged {
		if serviceRunning {
			if err := app.gracefulShutdown(ctx, current); err != nil {
				return err
			}
			serviceRunning = false
		}
		if err := app.options.Service.Uninstall(ctx); err != nil {
			return err
		}
		serviceLoaded = false
	}
	if serviceChanged {
		if err := os.MkdirAll(filepath.Dir(app.options.Paths.ServiceFile), 0o755); err != nil {
			return err
		}
		if err := os.WriteFile(app.options.Paths.ServiceFile, []byte(serviceFile), 0o644); err != nil {
			return err
		}
	}
	if app.options.Service != nil {
		didWaitForRPC := false
		if !serviceLoaded {
			if err := app.options.Service.Install(ctx); err != nil {
				return err
			}
		}
		if start {
			if serviceWasRunning && managedConfigChanged && !serviceChanged {
				if err := app.restartServiceGracefully(ctx, current); err != nil {
					return err
				}
				didWaitForRPC = true
			} else if err := app.options.Service.Start(ctx); err != nil {
				return err
			}
		} else if serviceWasRunning && serviceChanged {
			if err := app.options.Service.Start(ctx); err != nil {
				return err
			}
		}
		if start {
			if didWaitForRPC {
				return nil
			}
			return app.waitForRPC(ctx, current)
		}
	}
	return nil
}

func (app *App) RunConsole() error {
	if app.options.ConsoleRunner == nil {
		return errors.New("console runner not configured")
	}
	return app.options.ConsoleRunner(app)
}

func (app *App) SetConsoleRunner(runner func(*App) error) {
	app.options.ConsoleRunner = runner
}

func (app *App) EnsureConsoleReady(ctx context.Context) error {
	current, err := state.Load(app.options.Paths.StateFile)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return app.Install(ctx, true)
		}
		return fmt.Errorf("load state: %w", err)
	}
	if !isExecutable(current.Aria2cPath) {
		return app.Install(ctx, true)
	}
	values, err := aria2.ReadConfig(current.ConfigPath)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return app.Install(ctx, true)
		}
		return err
	}
	if aria2.HasManagedDrift(values, current) {
		return app.Install(ctx, true)
	}
	serviceFile, err := app.options.RenderService(current)
	if err != nil {
		return err
	}
	serviceChanged, err := fileContentChanged(app.options.Paths.ServiceFile, serviceFile)
	if err != nil {
		return err
	}
	if serviceChanged || needs0600File(current.SessionPath) || !dirExists(filepath.Dir(current.LogPath)) {
		return app.Install(ctx, true)
	}
	if app.options.Service != nil && !app.options.Service.IsLoaded(ctx) {
		return app.Install(ctx, true)
	}
	return app.Start(ctx)
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
	if app.options.Service.IsRunning(ctx) {
		return app.waitForRPC(ctx, current)
	}
	if err := app.options.Service.Start(ctx); err != nil {
		return err
	}
	return app.waitForRPC(ctx, current)
}

func (app *App) Stop(ctx context.Context) error {
	if app.options.Service.IsRunning(ctx) {
		current, err := state.Load(app.options.Paths.StateFile)
		if err != nil {
			return err
		}
		if err := app.gracefulShutdown(ctx, current); err != nil {
			return err
		}
	}
	return app.options.Service.Stop(ctx)
}

func (app *App) Restart(ctx context.Context) error {
	current, err := app.preflightLifecycle()
	if err != nil {
		return err
	}
	return app.restartServiceGracefully(ctx, current)
}

func (app *App) restartServiceGracefully(ctx context.Context, current state.State) error {
	if app.options.Service.IsRunning(ctx) {
		if err := app.gracefulShutdown(ctx, current); err != nil {
			return err
		}
	}
	if err := app.options.Service.Start(ctx); err != nil {
		return err
	}
	return app.waitForRPC(ctx, current)
}

func (app *App) gracefulShutdown(ctx context.Context, current state.State) error {
	if err := app.saveSession(ctx, current); err != nil {
		if errors.Is(err, aria2.ErrTransportUnavailable) {
			return app.waitForServiceStop(ctx)
		}
		return err
	}
	if err := app.shutdown(ctx, current); err != nil {
		if errors.Is(err, aria2.ErrTransportUnavailable) {
			return app.waitForServiceStop(ctx)
		}
		return err
	}
	return app.waitForServiceStop(ctx)
}

func (app *App) saveSession(ctx context.Context, current state.State) error {
	if err := app.options.RPC.SaveSession(ctx, current); err != nil {
		return fmt.Errorf("save session: %w", err)
	}
	return nil
}

func (app *App) shutdown(ctx context.Context, current state.State) error {
	if err := app.options.RPC.Shutdown(ctx, current); err != nil {
		return fmt.Errorf("shutdown aria2: %w", err)
	}
	return nil
}

func (app *App) Status(ctx context.Context) doctor.StatusReport {
	return doctor.Status(ctx, doctor.StatusOptions{
		Paths:   app.options.Paths,
		Service: app.options.Service,
		RPCVersion: func(ctx context.Context, current state.State) (string, error) {
			return app.options.RPC.Version(ctx, current)
		},
	})
}

func (app *App) Doctor(ctx context.Context) doctor.Report {
	return doctor.Check(ctx, doctor.Options{
		Paths:           app.options.Paths,
		IsPortAvailable: app.options.IsPortAvailable,
		Service:         app.options.Service,
		RPCReachable: func(ctx context.Context, current state.State) bool {
			_, err := app.options.RPC.Version(ctx, current)
			return err == nil
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

func (app *App) AddURI(ctx context.Context, uri string, opts aria2.AddOptions) (string, error) {
	return app.Add(ctx, uri, opts)
}

func (app *App) DefaultDir() string {
	return app.options.DownloadDir
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

func (app *App) ListDownloads(ctx context.Context, options aria2.ListOptions) (aria2.DownloadSnapshot, error) {
	current, rpc, err := app.consoleRPC()
	if err != nil {
		return aria2.DownloadSnapshot{}, err
	}
	return rpc.ListDownloads(ctx, current, options)
}

func (app *App) TaskDetail(ctx context.Context, gid string) (aria2.DownloadDetail, error) {
	current, rpc, err := app.consoleRPC()
	if err != nil {
		return aria2.DownloadDetail{}, err
	}
	return rpc.TaskDetail(ctx, current, gid)
}

func (app *App) Pause(ctx context.Context, gid string) error {
	current, rpc, err := app.consoleRPC()
	if err != nil {
		return err
	}
	return rpc.Pause(ctx, current, gid)
}

func (app *App) Resume(ctx context.Context, gid string) error {
	current, rpc, err := app.consoleRPC()
	if err != nil {
		return err
	}
	return rpc.Resume(ctx, current, gid)
}

func (app *App) Remove(ctx context.Context, gid string) error {
	current, rpc, err := app.consoleRPC()
	if err != nil {
		return err
	}
	return rpc.Remove(ctx, current, gid)
}

func (app *App) ClearStopped(ctx context.Context, gid string) error {
	current, rpc, err := app.consoleRPC()
	if err != nil {
		return err
	}
	return rpc.ClearStopped(ctx, current, gid)
}

func (app *App) Subscribe(ctx context.Context) <-chan aria2.Notification {
	current, err := state.Load(app.options.Paths.StateFile)
	if err != nil {
		return nil
	}
	wsClient, err := aria2.NewWSClient(endpoint(current.RPCPort))
	if err != nil {
		return nil
	}
	wsClient.Connect(ctx)
	return wsClient.Events()
}

func (app *App) Paths() paths.Paths {
	return app.options.Paths
}

func (app *App) consoleRPC() (state.State, consoleRPC, error) {
	current, err := state.Load(app.options.Paths.StateFile)
	if err != nil {
		return state.State{}, nil, err
	}
	rpc, ok := app.options.RPC.(consoleRPC)
	if !ok {
		return state.State{}, nil, errors.New("configured RPC client does not support console task management")
	}
	return current, rpc, nil
}

func (app *App) preflightLifecycle() (state.State, error) {
	current, err := state.Load(app.options.Paths.StateFile)
	if err != nil {
		return state.State{}, fmt.Errorf("load state: %w", err)
	}
	if !isExecutable(current.Aria2cPath) {
		return state.State{}, fmt.Errorf("stored aria2c path is not executable: %s", current.Aria2cPath)
	}
	values, err := aria2.ReadConfig(current.ConfigPath)
	if err != nil {
		return state.State{}, err
	}
	if aria2.HasManagedDrift(values, current) {
		return state.State{}, fmt.Errorf("managed config drift detected; run `aria2s install` to repair %s", current.ConfigPath)
	}
	return current, nil
}

func (app *App) waitForRPC(ctx context.Context, current state.State) error {
	deadline := time.Now().Add(app.options.RPCReadyTimeout)
	var lastErr error
	for {
		if _, err := app.options.RPC.Version(ctx, current); err == nil {
			return nil
		} else {
			lastErr = err
		}
		if time.Now().Add(app.options.RPCPollInterval).After(deadline) {
			return app.rpcReadyError(current, lastErr)
		}
		timer := time.NewTimer(app.options.RPCPollInterval)
		select {
		case <-ctx.Done():
			timer.Stop()
			return ctx.Err()
		case <-timer.C:
		}
	}
}

func (app *App) waitForServiceStop(ctx context.Context) error {
	deadline := time.Now().Add(app.options.ShutdownTimeout)
	for {
		if !app.options.Service.IsRunning(ctx) {
			return nil
		}
		if time.Now().Add(app.options.RPCPollInterval).After(deadline) {
			return fmt.Errorf("aria2 did not stop within %s after graceful shutdown", app.options.ShutdownTimeout)
		}
		timer := time.NewTimer(app.options.RPCPollInterval)
		select {
		case <-ctx.Done():
			timer.Stop()
			return ctx.Err()
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
	data, err := os.ReadFile(path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return true, nil
		}
		return false, err
	}
	return string(data) != content, nil
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
		r.http = &http.Client{
			Timeout:   10 * time.Second,
			Transport: http.DefaultTransport,
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
	ctx, cancel := context.WithTimeout(ctx, 2*time.Second)
	defer cancel()
	return r.rpcClient(current).Version(ctx)
}

func (r *LocalRPC) AddURI(ctx context.Context, current state.State, uri string, opts aria2.AddOptions) (string, error) {
	return r.rpcClient(current).AddURI(ctx, uri, opts)
}

func (r *LocalRPC) SaveSession(ctx context.Context, current state.State) error {
	return aria2.WrapTransportError(r.rpcClient(current).SaveSession(ctx))
}

func (r *LocalRPC) Shutdown(ctx context.Context, current state.State) error {
	return aria2.WrapTransportError(r.rpcClient(current).Shutdown(ctx))
}

func (r *LocalRPC) ListDownloads(ctx context.Context, current state.State, options aria2.ListOptions) (aria2.DownloadSnapshot, error) {
	return r.rpcClient(current).ListDownloads(ctx, options)
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
	file, err := os.OpenFile(path, os.O_CREATE, 0o600)
	if err != nil {
		return err
	}
	if err := file.Close(); err != nil {
		return err
	}
	return os.Chmod(path, 0o600)
}

func sameState(left, right state.State) bool {
	if left.Aria2cPath != right.Aria2cPath ||
		left.RPCPort != right.RPCPort ||
		left.RPCSecret != right.RPCSecret ||
		left.ConfigPath != right.ConfigPath ||
		left.SessionPath != right.SessionPath ||
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
