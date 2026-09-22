package app

import (
	"path/filepath"
	"strings"

	"github.com/amio/aria2s/internal/aria2"
	"github.com/amio/aria2s/internal/jobs"
)

type StartupFact struct {
	WorkEmpty    bool
	HasControl   bool
	InferredRoot string
	HasMetainfo  bool
	MetainfoPath string
	Torrent      bool
}

func normalizeStagedBlock(block aria2.SessionBlock, job jobs.Job, workDir string, fact StartupFact) (aria2.SessionBlock, string) {
	block = block.Clone()
	dir, ok := block.Option("dir")
	if ok && filepath.Clean(dir) != filepath.Clean(workDir) {
		return aria2.SessionBlock{}, "native block points outside the managed work directory"
	}
	options := managedTransferOptions(job, workDir, block.URI, fact)
	if strings.HasPrefix(block.URI, "http://") || strings.HasPrefix(block.URI, "https://") {
		if !fact.WorkEmpty {
			root := job.Payload.Root
			if root == "" {
				root = fact.InferredRoot
			}
			if !validRelativeRoot(root) {
				return aria2.SessionBlock{}, "non-empty HTTP work directory has no safe payload root"
			}
			options.Out = root
		}
	}
	block.ApplyOptions(options)
	return block, ""
}

func completeSubmittedSource(source string) bool {
	return strings.HasPrefix(source, "http://") || strings.HasPrefix(source, "https://") || strings.HasPrefix(source, "magnet:")
}

func validRelativeRoot(root string) bool {
	return root != "" && !filepath.IsAbs(root) && filepath.Clean(root) != ".." && !strings.HasPrefix(filepath.Clean(root), ".."+string(filepath.Separator))
}
