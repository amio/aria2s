package aria2

import "strconv"

// AddOptions carries native per-task overrides for RPC additions and session
// replay. Nil booleans preserve aria2 defaults or an existing session option;
// non-nil values explicitly override both true and false.
type AddOptions struct {
	Dir               string
	GID               string
	Out               string
	Pause             *bool
	AllowOverwrite    *bool
	AutoFileRenaming  *bool
	FollowTorrent     *bool
	MetadataOnly      *bool
	SaveMetadata      *bool
	SeedUnverified    *bool
	CheckIntegrity    *bool
	ForceSave         *bool
	RemoveControlFile *bool
}

func (opts AddOptions) values() map[string]string {
	values := make(map[string]string)
	for key, value := range map[string]string{
		"dir": opts.Dir, "gid": opts.GID, "out": opts.Out,
	} {
		if value != "" {
			values[key] = value
		}
	}
	for key, value := range map[string]*bool{
		"pause":               opts.Pause,
		"allow-overwrite":     opts.AllowOverwrite,
		"auto-file-renaming":  opts.AutoFileRenaming,
		"follow-torrent":      opts.FollowTorrent,
		"bt-metadata-only":    opts.MetadataOnly,
		"bt-save-metadata":    opts.SaveMetadata,
		"bt-seed-unverified":  opts.SeedUnverified,
		"check-integrity":     opts.CheckIntegrity,
		"force-save":          opts.ForceSave,
		"remove-control-file": opts.RemoveControlFile,
	} {
		if value != nil {
			values[key] = strconv.FormatBool(*value)
		}
	}
	return values
}
