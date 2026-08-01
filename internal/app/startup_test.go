package app

import (
	"path/filepath"
	"slices"
	"testing"

	"github.com/amio/aria2s/internal/aria2"
	"github.com/amio/aria2s/internal/jobs"
	"github.com/amio/aria2s/internal/state"
)

func TestManagedRuntimeArgsUseSafeFileAllocationOnlyWhenRequested(t *testing.T) {
	current := state.State{RPCPort: 6800, RPCSecret: "secret", StartupInputPath: "/state/startup", SessionPath: "/state/session"}
	normal := managedRuntimeArgs(current, "/state/hooks", false)
	if slices.Contains(normal, "--file-allocation=none") {
		t.Fatalf("normal startup unexpectedly overrides file allocation: %v", normal)
	}
	safe := managedRuntimeArgs(current, "/state/hooks", true)
	if !slices.Contains(safe, "--file-allocation=none") || safe[len(safe)-1] != "--file-allocation=none" {
		t.Fatalf("safe startup does not apply final file allocation override: %v", safe)
	}
}

func TestPlanStartupNormalizesHTTPAndFailsClosedWithoutRoot(t *testing.T) {
	root := t.TempDir()
	job := jobs.Job{ID: "0123456789abcdef", Source: "https://example.test/file", TargetDir: filepath.Join(root, "target"), StorageID: "fedcba9876543210", Phase: jobs.PhaseStaged, ActivityIntent: jobs.ActivityStopped, PayloadRoot: "resolved.bin"}
	scope := jobs.StorageScope{ID: job.StorageID, StagingAnchor: root}
	work := jobs.WorkDir(scope, job.ID)
	block := aria2.SessionBlock{URI: job.Source, Options: []aria2.SessionOption{{Key: "gid", Value: job.ID}, {Key: "dir", Value: work}, {Key: "out", Value: "stale.bin"}, {Key: "header", Value: "opaque"}}}
	plan := PlanStartup([]jobs.Job{job}, map[string]jobs.StorageScope{scope.ID: scope}, map[string]StartupFact{job.ID: {StorageAvailable: true}}, []aria2.SessionBlock{block})
	if len(plan.Problems) != 0 || len(plan.Blocks) != 1 {
		t.Fatalf("plan = %+v", plan)
	}
	if out, _ := plan.Blocks[0].Option("out"); out != "resolved.bin" {
		t.Fatalf("out = %q", out)
	}
	if pause, _ := plan.Blocks[0].Option("pause"); pause != "true" {
		t.Fatalf("pause = %q", pause)
	}
	job.PayloadRoot = ""
	plan = PlanStartup([]jobs.Job{job}, map[string]jobs.StorageScope{scope.ID: scope}, map[string]StartupFact{job.ID: {StorageAvailable: true}}, []aria2.SessionBlock{block})
	if len(plan.Blocks) != 0 || len(plan.Problems) != 1 || plan.Problems[0].Code != "RestartStateMissing" {
		t.Fatalf("fail-closed plan = %+v", plan)
	}
}

func TestPlanStartupRejectsDuplicateAndGeneratesFinalSeed(t *testing.T) {
	root := t.TempDir()
	scope := jobs.StorageScope{ID: "fedcba9876543210", StagingAnchor: root}
	staged := jobs.Job{ID: "0123456789abcdef", Source: "magnet:?xt=x", TargetDir: filepath.Join(root, "target"), StorageID: scope.ID, Phase: jobs.PhaseStaged, ActivityIntent: jobs.ActivityRunning}
	duplicate := aria2.SessionBlock{URI: staged.Source, Options: []aria2.SessionOption{{Key: "gid", Value: staged.ID}}}
	plan := PlanStartup([]jobs.Job{staged}, map[string]jobs.StorageScope{scope.ID: scope}, map[string]StartupFact{staged.ID: {StorageAvailable: true, WorkEmpty: true}}, []aria2.SessionBlock{duplicate, duplicate})
	if len(plan.Blocks) != 0 || len(plan.Problems) != 1 {
		t.Fatalf("duplicate plan = %+v", plan)
	}
	published := staged
	published.Phase = jobs.PhasePublished
	plan = PlanStartup([]jobs.Job{published}, map[string]jobs.StorageScope{scope.ID: scope}, map[string]StartupFact{published.ID: {StorageAvailable: true, Torrent: true, HasMetainfo: true, MetainfoPath: filepath.Join(root, "meta.torrent")}}, nil)
	if len(plan.Blocks) != 1 {
		t.Fatalf("final seed plan = %+v", plan)
	}
	if value, _ := plan.Blocks[0].Option("bt-seed-unverified"); value != "true" {
		t.Fatalf("seed options = %+v", plan.Blocks[0].Options)
	}
}

func TestPlanStartupOverridesUnverifiedSeedingByPublicationPhase(t *testing.T) {
	root := t.TempDir()
	scope := jobs.StorageScope{ID: "fedcba9876543210", StagingAnchor: root}
	staged := jobs.Job{
		ID:             "0123456789abcdef",
		Source:         "magnet:?xt=urn:btih:test",
		TargetDir:      filepath.Join(root, "target"),
		StorageID:      scope.ID,
		Phase:          jobs.PhaseStaged,
		ActivityIntent: jobs.ActivityRunning,
	}
	fact := StartupFact{
		StorageAvailable: true,
		Torrent:          true,
		HasMetainfo:      true,
		MetainfoPath:     filepath.Join(root, "meta.torrent"),
	}

	unsafeNative := aria2.SessionBlock{
		URI: fact.MetainfoPath,
		Options: []aria2.SessionOption{
			{Key: "gid", Value: staged.ID},
			{Key: "dir", Value: jobs.WorkDir(scope, staged.ID)},
			{Key: "bt-seed-unverified", Value: "true"},
		},
	}
	normalized := PlanStartup(
		[]jobs.Job{staged},
		map[string]jobs.StorageScope{scope.ID: scope},
		map[string]StartupFact{staged.ID: fact},
		[]aria2.SessionBlock{unsafeNative},
	)
	if len(normalized.Blocks) != 1 {
		t.Fatalf("normalized staged plan = %+v", normalized)
	}
	if value, _ := normalized.Blocks[0].Option("bt-seed-unverified"); value != "false" {
		t.Fatalf("normalized staged options = %+v", normalized.Blocks[0].Options)
	}

	generated := PlanStartup(
		[]jobs.Job{staged},
		map[string]jobs.StorageScope{scope.ID: scope},
		map[string]StartupFact{staged.ID: fact},
		nil,
	)
	if len(generated.Blocks) != 1 {
		t.Fatalf("generated staged plan = %+v", generated)
	}
	if value, _ := generated.Blocks[0].Option("bt-seed-unverified"); value != "false" {
		t.Fatalf("generated staged options = %+v", generated.Blocks[0].Options)
	}

	published := staged
	published.Phase = jobs.PhasePublished
	finalSeed := PlanStartup(
		[]jobs.Job{published},
		map[string]jobs.StorageScope{scope.ID: scope},
		map[string]StartupFact{published.ID: fact},
		nil,
	)
	if len(finalSeed.Blocks) != 1 {
		t.Fatalf("final seed plan = %+v", finalSeed)
	}
	if value, _ := finalSeed.Blocks[0].Option("bt-seed-unverified"); value != "true" {
		t.Fatalf("final seed options = %+v", finalSeed.Blocks[0].Options)
	}
}

func TestPlanStartupIsolatesOfflineStorage(t *testing.T) {
	root := t.TempDir()
	healthyScope := jobs.StorageScope{ID: "1111111111111111", StagingAnchor: filepath.Join(root, "healthy")}
	offlineScope := jobs.StorageScope{ID: "2222222222222222", StagingAnchor: filepath.Join(root, "offline")}
	healthy := jobs.Job{ID: "aaaaaaaaaaaaaaaa", Source: "https://example.test/healthy", StorageID: healthyScope.ID, Phase: jobs.PhaseStaged, ActivityIntent: jobs.ActivityRunning}
	offline := jobs.Job{ID: "bbbbbbbbbbbbbbbb", Source: "https://example.test/offline", StorageID: offlineScope.ID, Phase: jobs.PhaseStaged, ActivityIntent: jobs.ActivityRunning}
	healthyBlock := aria2.SessionBlock{URI: healthy.Source, Options: []aria2.SessionOption{{Key: "gid", Value: healthy.ID}}}
	plan := PlanStartup(
		[]jobs.Job{offline, healthy},
		map[string]jobs.StorageScope{healthyScope.ID: healthyScope, offlineScope.ID: offlineScope},
		map[string]StartupFact{healthy.ID: {StorageAvailable: true, WorkEmpty: true}, offline.ID: {StorageAvailable: false}},
		[]aria2.SessionBlock{healthyBlock},
	)
	if len(plan.Blocks) != 1 {
		t.Fatalf("healthy storage was suppressed by offline peer: %+v", plan)
	}
	if gid, _ := plan.Blocks[0].Option("gid"); gid != healthy.ID {
		t.Fatalf("planned gid = %q, want %q", gid, healthy.ID)
	}
	if len(plan.Problems) != 1 || plan.Problems[0].JobID != offline.ID || plan.Problems[0].Code != "StorageOffline" {
		t.Fatalf("offline storage problem = %+v", plan.Problems)
	}
}
