package app

import (
	"fmt"
	"path/filepath"
	"sort"
	"strings"

	"github.com/amio/aria2s/internal/aria2"
	"github.com/amio/aria2s/internal/jobs"
)

type StartupFact struct {
	StorageAvailable bool
	WorkEmpty        bool
	HasControl       bool
	InferredRoot     string
	HasMetainfo      bool
	MetainfoPath     string
	Torrent          bool
}

type StartupProblem struct {
	JobID string
	Code  string
	Cause string
}

type StartupPlan struct {
	Blocks   []aria2.SessionBlock
	Problems []StartupProblem
}

func PlanStartup(manifests []jobs.Job, storages map[string]jobs.StorageScope, facts map[string]StartupFact, native []aria2.SessionBlock) StartupPlan {
	byGID := make(map[string][]aria2.SessionBlock)
	for _, block := range native {
		gid, ok := block.Option("gid")
		if ok {
			byGID[gid] = append(byGID[gid], block)
		}
	}
	sort.Slice(manifests, func(i, j int) bool { return manifests[i].ID < manifests[j].ID })
	plan := StartupPlan{}
	for _, job := range manifests {
		if job.Phase == jobs.PhasePending || job.Phase == jobs.PhaseRemoved {
			continue
		}
		fact := facts[job.ID]
		scope, storageKnown := storages[job.StorageID]
		if !storageKnown || !fact.StorageAvailable {
			plan.problem(job.ID, "StorageOffline", "registered storage is unavailable")
			continue
		}
		blocks := byGID[job.ID]
		if len(blocks) > 1 {
			plan.problem(job.ID, "RestartStateMissing", "duplicate native session blocks")
			continue
		}
		switch job.Phase {
		case jobs.PhasePublishing:
			plan.problem(job.ID, "PublicationRecoveryRequired", "publication must reconcile before startup planning")
			continue
		case jobs.PhasePublished:
			if job.ActivityIntent == jobs.ActivityRunning {
				if !fact.Torrent || !fact.HasMetainfo {
					plan.problem(job.ID, "RestartStateMissing", "published seed has no valid retained metainfo")
					continue
				}
				plan.Blocks = append(plan.Blocks, generatedTorrentBlock(job, fact.MetainfoPath, job.TargetDir, false, true))
			}
			continue
		case jobs.PhaseStaged:
			workDir := jobs.WorkDir(scope, job.ID)
			if len(blocks) == 1 {
				block, problem := normalizeStagedBlock(blocks[0], job, workDir, fact)
				if problem != "" {
					plan.problem(job.ID, "RestartStateMissing", problem)
					continue
				}
				applyMissingControlRecovery(&block, fact)
				plan.Blocks = append(plan.Blocks, block)
				continue
			}
			if fact.Torrent && fact.HasMetainfo {
				block := generatedTorrentBlock(job, fact.MetainfoPath, workDir, job.ActivityIntent == jobs.ActivityStopped, false)
				applyMissingControlRecovery(&block, fact)
				plan.Blocks = append(plan.Blocks, block)
				continue
			}
			if fact.WorkEmpty && completeSubmittedSource(job.Source) {
				block := aria2.SessionBlock{URI: job.Source}
				applyManagedOptions(&block, job, workDir)
				plan.Blocks = append(plan.Blocks, block)
				continue
			}
			plan.problem(job.ID, "RestartStateMissing", "native block is missing beside staged artifacts")
		}
	}
	return plan
}

func normalizeStagedBlock(block aria2.SessionBlock, job jobs.Job, workDir string, fact StartupFact) (aria2.SessionBlock, string) {
	block = block.Clone()
	dir, ok := block.Option("dir")
	if ok && filepath.Clean(dir) != filepath.Clean(workDir) {
		return aria2.SessionBlock{}, "native block points outside the managed work directory"
	}
	if strings.HasPrefix(block.URI, "http://") || strings.HasPrefix(block.URI, "https://") {
		if !fact.WorkEmpty {
			root := job.PayloadRoot
			if root == "" {
				root = fact.InferredRoot
			}
			if !validRelativeRoot(root) {
				return aria2.SessionBlock{}, "non-empty HTTP work directory has no safe payload root"
			}
			block.SetOption("out", root)
		}
	}
	applyManagedOptions(&block, job, workDir)
	return block, ""
}

func applyManagedOptions(block *aria2.SessionBlock, job jobs.Job, dir string) {
	block.SetOption("gid", job.ID)
	block.SetOption("dir", dir)
	block.SetOption("allow-overwrite", "false")
	block.SetOption("auto-file-renaming", "false")
	block.SetOption("remove-control-file", "false")
	block.SetOption("force-save", "true")
	block.SetOption("follow-torrent", "false")
	// Managed staged jobs may inherit a legacy global true value otherwise.
	// Only generated final-seed blocks override this after publication.
	block.SetOption("bt-seed-unverified", "false")
	block.SetOption("pause", fmt.Sprintf("%t", job.ActivityIntent == jobs.ActivityStopped))
}

func generatedTorrentBlock(job jobs.Job, metainfoPath, dir string, paused, finalSeed bool) aria2.SessionBlock {
	block := aria2.SessionBlock{URI: metainfoPath}
	applyManagedOptions(&block, job, dir)
	block.SetOption("pause", fmt.Sprintf("%t", paused))
	if finalSeed {
		block.SetOption("bt-seed-unverified", "true")
		block.SetOption("check-integrity", "false")
		block.SetOption("force-save", "false")
		block.SetOption("remove-control-file", "true")
	}
	return block
}

func applyMissingControlRecovery(block *aria2.SessionBlock, fact StartupFact) {
	if stagedIntegrityRequired(fact) {
		// aria2 can reconstruct torrent progress from piece hashes when the
		// native control file is missing. Never trust staged bytes as a seed.
		block.SetOption("check-integrity", "true")
	}
}

func stagedIntegrityRequired(fact StartupFact) bool {
	return fact.Torrent && !fact.WorkEmpty && !fact.HasControl
}

func completeSubmittedSource(source string) bool {
	return strings.HasPrefix(source, "http://") || strings.HasPrefix(source, "https://") || strings.HasPrefix(source, "magnet:")
}

func validRelativeRoot(root string) bool {
	return root != "" && !filepath.IsAbs(root) && filepath.Clean(root) != ".." && !strings.HasPrefix(filepath.Clean(root), ".."+string(filepath.Separator))
}

func (plan *StartupPlan) problem(jobID, code, cause string) {
	plan.Problems = append(plan.Problems, StartupProblem{JobID: jobID, Code: code, Cause: cause})
}
