package jobs

// IssueMetadata is the single presentation policy for durable issue codes.
// Manifests persist only the code; severity, text, and actions are derived.
type IssueMetadata struct {
	Severity string
	Text     string
	// nil keeps ordinary status actions; a non-nil slice overrides them,
	// including an empty slice that explicitly forbids every action.
	Actions []string
}

var issueMetadata = map[string]IssueMetadata{
	"AddFailed":                      {"error", "aria2 could not start this transfer", []string{"retry", "remove"}},
	"CleanupFailed":                  {"warning", "managed staging cleanup is incomplete", []string{"remove"}},
	"CorruptManifest":                {"error", "managed task metadata is corrupt", []string{}},
	"FinalSeedPathMismatch":          {"error", "seed files are missing or changed; restore them to the download location and retry, or remove the task", []string{"retry", "remove"}},
	"FinalSeedStartFailed":           {"error", "published payload could not start seeding", []string{"retry", "remove"}},
	"ManagedIdentityConflict":        {"error", "native execution does not match managed ownership", []string{"retry"}},
	"PublicationConflict":            {"error", "publication destination conflicts with another payload", []string{"retry"}},
	"PublicationPayloadMismatch":     {"error", "payload identity changed during publication", []string{"retry"}},
	"PublicationPayloadMissing":      {"error", "prepared payload is missing", []string{"retry"}},
	"PublicationRecoveryRequired":    {"error", "publication requires reconciliation", []string{"retry"}},
	"PublicationStateUncertain":      {"error", "publication filesystem state is uncertain", []string{"retry"}},
	"PublicationUnsupported":         {"error", "payload cannot be published atomically", []string{"retry"}},
	"PowerLossDurabilityUnavailable": {"warning", "storage cannot guarantee directory durability across host power loss", nil},
	"RestartCheckpointFailed":        {"warning", "aria2 restart state could not be saved", []string{"retry"}},
	"RestartStateMissing":            {"error", "native restart state is missing or invalid", []string{"retry", "remove"}},
	"StorageOffline":                 {"error", "registered storage is unavailable or changed", []string{"retry", "remove"}},
	"StorageMismatch":                {"error", "registered storage identity changed", []string{"retry"}},
}

func LookupIssue(code string) (IssueMetadata, bool) {
	metadata, ok := issueMetadata[code]
	if metadata.Actions != nil {
		metadata.Actions = append([]string{}, metadata.Actions...)
	}
	return metadata, ok
}
