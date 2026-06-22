package paths

/** Paths contains the macOS Stage 1 filesystem locations owned by aria2s. */
type Paths struct {
	ServiceName  string
	ServiceFile  string
	ConfigFile   string
	StateFile    string
	SessionFile  string
	LogFile      string
	ErrorLogFile string
}
