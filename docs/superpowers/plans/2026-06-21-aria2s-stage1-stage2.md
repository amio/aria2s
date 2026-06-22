# aria2s Stage 1 and Stage 2 Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Build `aria2s` through the end of Stage 2: a macOS-first Go CLI that manages a local `aria2c` background service and an interactive download console.

**Architecture:** Stage 1 builds a small Cobra CLI over a set of focused internal packages: path resolution, managed state persistence, aria2 config generation and repair, JSON-RPC access, launchd service management, and doctor/status reporting. Stage 2 adds a Bubble Tea TUI that reuses the same RPC client and state loading so interactive management does not fork connection logic or token handling.

**Tech Stack:** Go 1.26, `github.com/spf13/cobra`, `github.com/charmbracelet/bubbletea`, standard library, `make`

---

## Scope and Constraints

- Stage 1 implementation target: macOS only. Linux backend remains out of scope for this rollout.
- Direct third-party dependency budget: 2 packages (`cobra`, `bubbletea`).
- Tests should focus on high-risk and high-value behavior: state/config ownership, port selection stability, launchd command wiring, RPC request formatting, and TUI update logic.
- The final git history for this rollout must contain one commit for Stage 1 and one commit for Stage 2.

## Planned File Structure

**Create**

- `go.mod`
- `main.go`
- `Makefile`
- `README.md`
- `cmd/root.go`
- `cmd/install.go`
- `cmd/uninstall.go`
- `cmd/start.go`
- `cmd/stop.go`
- `cmd/restart.go`
- `cmd/status.go`
- `cmd/logs.go`
- `cmd/doctor.go`
- `cmd/add.go`
- `cmd/console.go`
- `internal/app/app.go`
- `internal/aria2/config.go`
- `internal/aria2/config_test.go`
- `internal/aria2/rpc.go`
- `internal/aria2/rpc_test.go`
- `internal/aria2/downloads.go`
- `internal/aria2/downloads_test.go`
- `internal/doctor/doctor.go`
- `internal/doctor/doctor_test.go`
- `internal/paths/darwin.go`
- `internal/paths/paths.go`
- `internal/paths/paths_test.go`
- `internal/service/backend.go`
- `internal/service/launchd.go`
- `internal/service/launchd_test.go`
- `internal/state/state.go`
- `internal/state/state_test.go`
- `internal/tui/model.go`
- `internal/tui/model_test.go`
- `internal/tui/view.go`

**Modify**

- `docs/aria2s-tech-design.md`
  - Only if implementation reveals a requirement that must be clarified for long-term maintenance.

## Task 1: Stage 1 Core Service CLI

**Files:**

- Create: `go.mod`, `main.go`, `Makefile`, `README.md`
- Create: `cmd/root.go`, `cmd/install.go`, `cmd/uninstall.go`, `cmd/start.go`, `cmd/stop.go`, `cmd/restart.go`, `cmd/status.go`, `cmd/logs.go`, `cmd/doctor.go`, `cmd/add.go`
- Create: `internal/app/app.go`
- Create: `internal/aria2/config.go`, `internal/aria2/config_test.go`, `internal/aria2/rpc.go`, `internal/aria2/rpc_test.go`
- Create: `internal/doctor/doctor.go`, `internal/doctor/doctor_test.go`
- Create: `internal/paths/darwin.go`, `internal/paths/paths.go`, `internal/paths/paths_test.go`
- Create: `internal/service/backend.go`, `internal/service/launchd.go`, `internal/service/launchd_test.go`
- Create: `internal/state/state.go`, `internal/state/state_test.go`

- [ ] **Step 1: Write the failing foundational tests**

```go
func TestSaveStateWrites0600AndRoundTrips(t *testing.T) {
	root := t.TempDir()
	paths := paths.NewDarwin(filepath.Join(root, "home"))
	current := state.State{
		Aria2cPath:   "/opt/homebrew/bin/aria2c",
		RPCPort:      6800,
		RPCSecret:    "secret-token",
		ConfigPath:   paths.ConfigFile,
		SessionPath:  paths.SessionFile,
		LogPath:      paths.LogFile,
		ErrorLogPath: paths.ErrorLogFile,
		ServiceName:  "io.github.amio.aria2s",
	}

	if err := state.Save(paths.StateFile, current); err != nil {
		t.Fatalf("save state: %v", err)
	}

	info, err := os.Stat(paths.StateFile)
	if err != nil {
		t.Fatalf("stat state: %v", err)
	}
	if got := info.Mode().Perm(); got != 0o600 {
		t.Fatalf("expected 0600, got %o", got)
	}

	reloaded, err := state.Load(paths.StateFile)
	if err != nil {
		t.Fatalf("load state: %v", err)
	}
	if reloaded.RPCPort != current.RPCPort || reloaded.Aria2cPath != current.Aria2cPath {
		t.Fatalf("round trip mismatch: %#v", reloaded)
	}
}

func TestBuildManagedConfigRepairsManagedKeysButPreservesUserKeys(t *testing.T) {
	managed := aria2.ManagedConfig{
		RPCPort:     6800,
		RPCSecret:   "secret-token",
		SessionFile: "/tmp/session",
		DownloadDir: "/tmp/downloads",
	}
	current := map[string]string{
		"dir":                    "/Users/amio/Downloads/custom",
		"split":                  "16",
		"rpc-listen-port":        "9999",
		"save-session-interval":  "10",
	}

	rendered := aria2.BuildConfig(managed, current)

	assertContains(t, rendered, "rpc-listen-port=6800")
	assertContains(t, rendered, "rpc-secret=secret-token")
	assertContains(t, rendered, "save-session-interval=60")
	assertContains(t, rendered, "dir=/Users/amio/Downloads/custom")
	assertContains(t, rendered, "split=16")
}

func TestCallAddsTokenAndPayload(t *testing.T) {
	var body []byte
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var err error
		body, err = io.ReadAll(r.Body)
		if err != nil {
			t.Fatalf("read body: %v", err)
		}
		fmt.Fprint(w, `{"jsonrpc":"2.0","id":"1","result":"2089b05ecca3d829"}`)
	}))
	defer server.Close()

	client := aria2.NewRPCClient(server.URL, "secret-token", server.Client())
	result, err := client.AddURI(context.Background(), "https://example.com/file.zip")
	if err != nil {
		t.Fatalf("add uri: %v", err)
	}
	if result != "2089b05ecca3d829" {
		t.Fatalf("unexpected gid: %s", result)
	}
	assertContains(t, string(body), `"method":"aria2.addUri"`)
	assertContains(t, string(body), `"token:secret-token"`)
}
```

- [ ] **Step 2: Run foundational tests to verify they fail**

Run: `go test ./internal/...`
Expected: FAIL because the packages and functions do not exist yet.

- [ ] **Step 3: Write the minimal project scaffold and implementations needed to pass the foundational tests**

```go
module github.com/amio/aria2s

go 1.26

require (
	github.com/spf13/cobra v1.10.1
)
```

```go
type State struct {
	Aria2cPath   string `json:"aria2cPath"`
	RPCPort      int    `json:"rpcPort"`
	RPCSecret    string `json:"rpcSecret"`
	ConfigPath   string `json:"configPath"`
	SessionPath  string `json:"sessionPath"`
	LogPath      string `json:"logPath"`
	ErrorLogPath string `json:"errorLogPath"`
	ServiceName  string `json:"serviceName"`
}

func Save(path string, current State) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	data, err := json.MarshalIndent(current, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(path, data, 0o600)
}
```

- [ ] **Step 4: Write the failing integration-focused Stage 1 tests**

```go
func TestInstallStartStatusAndAddFlow(t *testing.T) {
	env := newTestEnvironment(t)
	aria2c := env.WriteExecutable("aria2c")
	env.SetLookPath("aria2c", aria2c)
	env.ReservePort(6800, false)
	env.SetRPCVersion("1.37.0")

	result := env.Run("install", "--start")
	if result.Err != nil {
		t.Fatalf("install failed: %v", result.Err)
	}
	if !strings.Contains(result.Stdout, "aria2s installed and started.") {
		t.Fatalf("unexpected install output: %s", result.Stdout)
	}

	status := env.Run("status")
	if status.Err != nil {
		t.Fatalf("status failed: %v", status.Err)
	}
	assertContains(t, status.Stdout, "RPC:        reachable")
	assertContains(t, status.Stdout, "Endpoint:   http://127.0.0.1:6800/jsonrpc")

	add := env.Run("add", "https://example.com/file.zip")
	if add.Err != nil {
		t.Fatalf("add failed: %v", add.Err)
	}
	assertContains(t, add.Stdout, "Added download.")
	assertContains(t, add.Stdout, "2089b05ecca3d829")
}

func TestDoctorDetectsMissingBinaryAndConfigDrift(t *testing.T) {
	env := newTestEnvironment(t)
	env.WriteState(state.State{
		Aria2cPath:  "/tmp/missing-aria2c",
		RPCPort:     6800,
		RPCSecret:   "secret-token",
		ConfigPath:  env.ConfigFile(),
		SessionPath: env.SessionFile(),
		ServiceName: "io.github.amio.aria2s",
	})
	env.WriteConfig("rpc-listen-port=9999\nrpc-secret=wrong\n")

	result := env.Run("doctor")
	if result.Err == nil {
		t.Fatal("expected doctor to report failure")
	}
	assertContains(t, result.Stdout, "missing aria2c binary")
	assertContains(t, result.Stdout, "managed config drift")
}
```

- [ ] **Step 5: Run the Stage 1 tests to verify they fail for the expected reasons**

Run: `go test ./...`
Expected: FAIL because command wiring, service management, doctor checks, and add flow are still incomplete.

- [ ] **Step 6: Implement the minimal Stage 1 behavior to make those tests pass**

```go
type Backend interface {
	Install(ctx context.Context, current state.State) error
	Uninstall(ctx context.Context, current state.State) error
	Start(ctx context.Context, serviceName string) error
	Stop(ctx context.Context, serviceName string) error
	Restart(ctx context.Context, serviceName string) error
	Status(ctx context.Context, serviceName string) (ServiceStatus, error)
	LogsCommand(current state.State) *exec.Cmd
}
```

```go
func (a *App) Install(ctx context.Context, start bool) (*InstallResult, error) {
	aria2cPath, err := a.locateAria2c()
	if err != nil {
		return nil, err
	}
	current, err := a.prepareState(ctx, aria2cPath)
	if err != nil {
		return nil, err
	}
	if err := a.writeManagedFiles(current); err != nil {
		return nil, err
	}
	if err := a.backend.Install(ctx, current); err != nil {
		return nil, err
	}
	if start {
		if err := a.backend.Start(ctx, current.ServiceName); err != nil {
			return nil, err
		}
		if err := a.rpcClientFromState(current).GetVersion(ctx); err != nil {
			return nil, err
		}
	}
	return &InstallResult{State: current}, nil
}
```

- [ ] **Step 7: Run the Stage 1 verification suite**

Run: `make test`
Expected: PASS

Run: `make build`
Expected: PASS and produces `./bin/asv`

Run: `make test-stage1`
Expected: PASS for Stage 1 focused tests

- [ ] **Step 8: Commit Stage 1**

```bash
git add .
git commit -m "feat(stage1): implement macOS aria2 service CLI"
```

## Task 2: Stage 2 Interactive Console

**Files:**

- Modify: `go.mod`
- Modify: `cmd/root.go`
- Create: `cmd/console.go`
- Create: `internal/aria2/downloads.go`, `internal/aria2/downloads_test.go`
- Create: `internal/tui/model.go`, `internal/tui/model_test.go`, `internal/tui/view.go`

- [ ] **Step 1: Write the failing Stage 2 tests**

```go
func TestModelRefreshLoadsActiveWaitingAndVisibleStoppedPage(t *testing.T) {
	client := newStubRPCClient()
	client.Active = []aria2.Download{{GID: "active-1", Name: "file.zip", Status: "active", Progress: 42.1}}
	client.Waiting = []aria2.Download{{GID: "waiting-1", Name: "ubuntu.iso", Status: "waiting"}}
	client.Stopped = []aria2.Download{{GID: "stopped-1", Name: "video.mp4", Status: "complete"}}

	model := tui.NewModel(client, tui.Options{StoppedPageSize: 100})
	updated, cmd := model.Update(tui.RefreshTickMsg{})
	if cmd == nil {
		t.Fatal("expected refresh command")
	}

	next := runCommand(t, updated, cmd)
	if len(next.Active) != 1 || len(next.Waiting) != 1 || len(next.Stopped) != 1 {
		t.Fatalf("unexpected refresh result: %#v", next)
	}
}

func TestModelHandlesPauseResumeRemoveAndAdd(t *testing.T) {
	client := newStubRPCClient()
	client.Active = []aria2.Download{{GID: "active-1", Name: "file.zip", Status: "active"}}

	model := tui.NewModel(client, tui.Options{StoppedPageSize: 100})
	model = model.WithActiveDownloads(client.Active)
	model = model.WithInputValue("https://example.com/file.zip")

	model, _ = model.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'p'}})
	if client.PausedGID != "active-1" {
		t.Fatalf("expected pause on selected gid, got %q", client.PausedGID)
	}

	model, _ = model.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'a'}})
	model, _ = model.Update(tea.KeyMsg{Type: tea.KeyEnter})
	if client.AddedURI != "https://example.com/file.zip" {
		t.Fatalf("expected add uri, got %q", client.AddedURI)
	}
}

func TestConsoleViewShowsSectionsAndKeyHints(t *testing.T) {
	model := tui.NewModel(newStubRPCClient(), tui.Options{StoppedPageSize: 100})
	model = model.WithActiveDownloads([]aria2.Download{{GID: "2089b05e", Name: "file.zip", Status: "active", Progress: 42.1, DownloadSpeed: "3.2 MiB/s", ETA: "01:13"}})

	view := model.View()
	assertContains(t, view, "aria2s console")
	assertContains(t, view, "Active Downloads")
	assertContains(t, view, "Waiting")
	assertContains(t, view, "Stopped")
	assertContains(t, view, "a add")
	assertContains(t, view, "p pause")
	assertContains(t, view, "r resume")
	assertContains(t, view, "d remove")
	assertContains(t, view, "enter details")
}
```

- [ ] **Step 2: Run the Stage 2 tests to verify they fail**

Run: `go test ./internal/tui ./internal/aria2 -run 'TestModel|TestConsoleView'`
Expected: FAIL because the console model, refresh loop, and console command do not exist yet.

- [ ] **Step 3: Add the minimal Stage 2 dependencies and RPC list/detail support**

```go
require (
	github.com/charmbracelet/bubbletea v1.3.4
	github.com/spf13/cobra v1.10.1
)
```

```go
type Download struct {
	GID           string
	Name          string
	Status        string
	Progress      float64
	DownloadSpeed string
	ETA           string
	TotalLength   string
	Completed     string
}
```

- [ ] **Step 4: Implement the minimal Bubble Tea model and `asv console` command**

```go
func (m Model) Update(msg tea.Msg) (Model, tea.Cmd) {
	switch msg := msg.(type) {
	case RefreshTickMsg:
		return m, m.refreshCmd()
	case refreshResultMsg:
		m.active = msg.Active
		m.waiting = msg.Waiting
		m.stopped = msg.Stopped
		return m, tickEvery(m.refreshInterval)
	case tea.KeyMsg:
		return m.handleKey(msg)
	default:
		return m, nil
	}
}
```

```go
func newConsoleCommand(app *app.App) *cobra.Command {
	return &cobra.Command{
		Use:   "console",
		Short: "Open the interactive aria2 console",
		RunE: func(cmd *cobra.Command, args []string) error {
			program := tea.NewProgram(tui.NewModel(app.ConsoleClient(), tui.DefaultOptions()))
			_, err := program.Run()
			return err
		},
	}
}
```

- [ ] **Step 5: Run the Stage 2 verification suite**

Run: `go test ./...`
Expected: PASS

Run: `make test-stage2`
Expected: PASS for Stage 2 focused tests

Run: `make build`
Expected: PASS and `./bin/asv console` launches the TUI entrypoint

- [ ] **Step 6: Commit Stage 2**

```bash
git add .
git commit -m "feat(stage2): add interactive aria2 console"
```

## Verification Checklist

- `make test`
- `make test-stage1`
- `make test-stage2`
- `make build`
- `go test ./...`
- Manual smoke on macOS if `aria2c` is installed:
  - `./bin/asv install --start`
  - `./bin/asv status`
  - `./bin/asv add https://example.com/file.zip`
  - `./bin/asv console`

