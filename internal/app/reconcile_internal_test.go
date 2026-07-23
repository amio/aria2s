package app

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/amio/aria2s/internal/jobs"
	"github.com/amio/aria2s/internal/publication"
)

func TestReconcilePublishingConvergesMoveAndReliablePostMoveCrash(t *testing.T) {
	for _, afterRename := range []bool{false, true} {
		t.Run(map[bool]string{false: "detached-before-rename", true: "renamed-before-commit"}[afterRename], func(t *testing.T) {
			root := t.TempDir()
			target := filepath.Join(root, "target")
			if err := os.Mkdir(target, 0o700); err != nil {
				t.Fatal(err)
			}
			repository := jobs.New(filepath.Join(root, "state"))
			scope := jobs.StorageScope{ID: "fedcba9876543210", MountPoint: root, StagingAnchor: root}
			work := jobs.WorkDir(scope, "0123456789abcdef")
			if err := os.MkdirAll(work, 0o700); err != nil {
				t.Fatal(err)
			}
			source := filepath.Join(work, "payload.bin")
			if err := os.WriteFile(source, []byte("payload"), 0o600); err != nil {
				t.Fatal(err)
			}
			identity, err := publication.Identify(source)
			if err != nil {
				t.Fatal(err)
			}
			targetIdentity, err := publication.Identify(target)
			if err != nil {
				t.Fatal(err)
			}
			job := jobs.Job{ID: "0123456789abcdef", Source: "https://example.test/payload", TargetDir: target, TargetIdentity: jobIdentity(targetIdentity), StorageID: scope.ID, Phase: jobs.PhasePublishing, ActivityIntent: jobs.ActivityRunning, PayloadRoot: "payload.bin", PayloadIdentity: jobIdentity(identity)}
			token, err := repository.Create(job)
			if err != nil {
				t.Fatal(err)
			}
			if afterRename {
				if _, err := publication.Move(source, filepath.Join(target, "payload.bin")); err != nil {
					t.Fatal(err)
				}
			}
			job, token, err = reconcilePublishing(repository, job, token, scope)
			if err != nil {
				t.Fatal(err)
			}
			if job.Phase != jobs.PhasePublished || job.ProblemCode != "" {
				t.Fatalf("job=%+v token=%x", job, token)
			}
			if job.ActivityIntent != jobs.ActivityStopped {
				t.Fatalf("HTTP publication recovery retained seed intent: %+v", job)
			}
			if data, err := os.ReadFile(filepath.Join(target, "payload.bin")); err != nil || string(data) != "payload" {
				t.Fatalf("data=%q err=%v", data, err)
			}
		})
	}
}

func TestReconcilePublishingConvergesWeakIdentityPostMoveCrash(t *testing.T) {
	root := t.TempDir()
	target := filepath.Join(root, "target")
	if err := os.Mkdir(target, 0o700); err != nil {
		t.Fatal(err)
	}
	repository := jobs.New(filepath.Join(root, "state"))
	scope := jobs.StorageScope{ID: "fedcba9876543210", MountPoint: root, StagingAnchor: root}
	work := jobs.WorkDir(scope, "0123456789abcdef")
	if err := os.MkdirAll(work, 0o700); err != nil {
		t.Fatal(err)
	}
	source := filepath.Join(work, "payload.bin")
	if err := os.WriteFile(source, []byte("payload"), 0o600); err != nil {
		t.Fatal(err)
	}
	identity, err := publication.Identify(source)
	if err != nil {
		t.Fatal(err)
	}
	targetIdentity, err := publication.Identify(target)
	if err != nil {
		t.Fatal(err)
	}
	job := jobs.Job{ID: "0123456789abcdef", Source: "https://example.test/payload", TargetDir: target, TargetIdentity: jobIdentity(targetIdentity), StorageID: scope.ID, Phase: jobs.PhasePublishing, ActivityIntent: jobs.ActivityRunning, PayloadRoot: "payload.bin", PayloadIdentity: jobIdentity(identity)}
	job.PayloadIdentity.ReliableAcrossRename = false
	token, err := repository.Create(job)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := publication.Move(source, filepath.Join(target, "payload.bin")); err != nil {
		t.Fatal(err)
	}
	job, _, err = reconcilePublishing(repository, job, token, scope)
	if err != nil {
		t.Fatal(err)
	}
	if job.Phase != jobs.PhasePublished || job.ProblemCode != "" || job.ActivityIntent != jobs.ActivityStopped {
		t.Fatalf("weak-identity publication did not converge: %+v", job)
	}
}

func TestReconcilePublishingFailsClosedOnConflict(t *testing.T) {
	root := t.TempDir()
	target := filepath.Join(root, "target")
	os.Mkdir(target, 0o700)
	repository := jobs.New(filepath.Join(root, "state"))
	scope := jobs.StorageScope{ID: "fedcba9876543210", MountPoint: root, StagingAnchor: root}
	work := jobs.WorkDir(scope, "0123456789abcdef")
	os.MkdirAll(work, 0o700)
	os.WriteFile(filepath.Join(work, "payload.bin"), []byte("managed"), 0o600)
	os.WriteFile(filepath.Join(target, "payload.bin"), []byte("external"), 0o600)
	identity, _ := publication.Identify(filepath.Join(work, "payload.bin"))
	targetIdentity, _ := publication.Identify(target)
	job := jobs.Job{ID: "0123456789abcdef", Source: "https://example.test/payload", TargetDir: target, TargetIdentity: jobIdentity(targetIdentity), StorageID: scope.ID, Phase: jobs.PhasePublishing, ActivityIntent: jobs.ActivityRunning, PayloadRoot: "payload.bin", PayloadIdentity: jobIdentity(identity)}
	token, _ := repository.Create(job)
	job, _, err := reconcilePublishing(repository, job, token, scope)
	if err != nil {
		t.Fatal(err)
	}
	if job.Phase != jobs.PhasePublishing || job.ProblemCode != "PublicationConflict" {
		t.Fatalf("job=%+v", job)
	}
	if data, _ := os.ReadFile(filepath.Join(target, "payload.bin")); string(data) != "external" {
		t.Fatalf("destination overwritten: %q", data)
	}
}

func TestReconcileHTTPDescriptorPreservesRunningSeedIntent(t *testing.T) {
	root := t.TempDir()
	target := filepath.Join(root, "target")
	if err := os.Mkdir(target, 0o700); err != nil {
		t.Fatal(err)
	}
	repository := jobs.New(filepath.Join(root, "state"))
	scope := jobs.StorageScope{ID: "fedcba9876543210", MountPoint: root, StagingAnchor: root}
	work := jobs.WorkDir(scope, "0123456789abcdef")
	if err := os.MkdirAll(work, 0o700); err != nil {
		t.Fatal(err)
	}
	source := filepath.Join(work, "payload.bin")
	if err := os.WriteFile(source, []byte("payload"), 0o600); err != nil {
		t.Fatal(err)
	}
	identity, err := publication.Identify(source)
	if err != nil {
		t.Fatal(err)
	}
	targetIdentity, err := publication.Identify(target)
	if err != nil {
		t.Fatal(err)
	}
	job := jobs.Job{ID: "0123456789abcdef", Source: "https://example.test/file.torrent", TargetDir: target, TargetIdentity: jobIdentity(targetIdentity), StorageID: scope.ID, Phase: jobs.PhasePublishing, ActivityIntent: jobs.ActivityRunning, PayloadRoot: "payload.bin", PayloadIdentity: jobIdentity(identity)}
	token, err := repository.Create(job)
	if err != nil {
		t.Fatal(err)
	}
	metainfo := []byte("d4:infod6:lengthi1e4:name1:x12:piece lengthi1e6:pieces20:01234567890123456789ee")
	if err := repository.WriteMetainfo(job.ID, metainfo); err != nil {
		t.Fatal(err)
	}
	if _, err := publication.Move(source, filepath.Join(target, "payload.bin")); err != nil {
		t.Fatal(err)
	}
	job, _, err = reconcilePublishing(repository, job, token, scope)
	if err != nil {
		t.Fatal(err)
	}
	if job.Phase != jobs.PhasePublished || job.ActivityIntent != jobs.ActivityRunning || job.ProblemCode != "" {
		t.Fatalf("descriptor publication recovery lost seed intent: %+v", job)
	}
}
