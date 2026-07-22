package atomicfile

import (
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
)

func TestWriteSurvivesProcessCrashAtCommitBoundary(t *testing.T) {
	for _, test := range []struct {
		step string
		want string
	}{{step: "before-rename", want: "old"}, {step: "after-rename", want: "new"}} {
		t.Run(test.step, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "state.json")
			if err := Write(path, []byte("old"), 0o600); err != nil {
				t.Fatal(err)
			}
			command := exec.Command(os.Args[0], "-test.run=^TestAtomicWriteCrashHelper$")
			command.Env = append(os.Environ(),
				"ARIA2S_ATOMIC_CRASH_HELPER=1",
				"ARIA2S_ATOMIC_CRASH_PATH="+path,
				"ARIA2S_ATOMIC_CRASH_STEP="+test.step,
			)
			if err := command.Run(); err == nil {
				t.Fatal("crash helper completed instead of terminating")
			} else if exit, ok := err.(*exec.ExitError); !ok || exit.ExitCode() != 91 {
				t.Fatalf("crash helper exit = %v", err)
			}
			data, err := os.ReadFile(path)
			if err != nil || string(data) != test.want {
				t.Fatalf("authoritative file after %s = %q err=%v", test.step, data, err)
			}
		})
	}
}

func TestAtomicWriteCrashHelper(t *testing.T) {
	if os.Getenv("ARIA2S_ATOMIC_CRASH_HELPER") != "1" {
		return
	}
	path := os.Getenv("ARIA2S_ATOMIC_CRASH_PATH")
	crashStep := os.Getenv("ARIA2S_ATOMIC_CRASH_STEP")
	err := write(path, []byte("new"), 0o600, func(step string) {
		if step == crashStep {
			os.Exit(91)
		}
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Fatalf("write did not reach crash step %q", crashStep)
}

func TestWriteReplacesContentAndMode(t *testing.T) {
	path := filepath.Join(t.TempDir(), "nested", "state.json")
	if err := Write(path, []byte("first"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := Write(path, []byte("second"), 0o640); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if string(data) != "second" {
		t.Fatalf("content = %q", data)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0o640 {
		t.Fatalf("mode = %o", info.Mode().Perm())
	}
}

func TestCreateNeverOverwritesExistingFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "aria2.conf")
	if err := Create(path, []byte("user"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := Create(path, []byte("managed"), 0o600); !errors.Is(err, os.ErrExist) {
		t.Fatalf("second create error = %v", err)
	}
	data, err := os.ReadFile(path)
	if err != nil || string(data) != "user" {
		t.Fatalf("existing file changed: %q err=%v", data, err)
	}
}
