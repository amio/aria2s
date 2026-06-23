package paths

import "fmt"

/** Paths contains the filesystem locations owned by aria2s on the active platform. */
type Paths struct {
	ServiceName  string
	ServiceFile  string
	ConfigFile   string
	StateFile    string
	SessionFile  string
	LogFile      string
	ErrorLogFile string
}

func NewForOS(goos, home string) (Paths, error) {
	switch goos {
	case "darwin":
		return NewDarwin(home), nil
	case "linux":
		return NewLinux(home), nil
	default:
		return Paths{}, fmt.Errorf("unsupported OS: %s", goos)
	}
}
