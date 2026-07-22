package publication

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
)

func TestMoveNoReplacePreservesIdentityAndConflict(t *testing.T) {
	root := t.TempDir()
	sourceDir := filepath.Join(root, "stage")
	destinationDir := filepath.Join(root, "target")
	os.Mkdir(sourceDir, 0o700)
	os.Mkdir(destinationDir, 0o700)
	source := filepath.Join(sourceDir, "payload")
	os.WriteFile(source, []byte("payload"), 0o600)
	before, err := Identify(source)
	if err != nil {
		t.Fatal(err)
	}
	destination := filepath.Join(destinationDir, "payload")
	if _, err := MoveNoReplace(source, destination); err != nil {
		t.Fatal(err)
	}
	after, err := Identify(destination)
	if err != nil {
		t.Fatal(err)
	}
	if !SameObject(before, after) {
		t.Fatal("rename did not preserve object identity")
	}
	os.WriteFile(source, []byte("second"), 0o600)
	if _, err := MoveNoReplace(source, destination); !errors.Is(err, ErrConflict) {
		t.Fatalf("conflict error = %v", err)
	}
	data, _ := os.ReadFile(destination)
	if string(data) != "payload" {
		t.Fatalf("destination overwritten: %q", data)
	}
}

func TestValidatePayloadRootRejectsSymlink(t *testing.T) {
	work := t.TempDir()
	target := filepath.Join(work, "payload")
	os.WriteFile(target, []byte("x"), 0o600)
	if _, _, err := ValidatePayloadRoot(work, "payload"); err != nil {
		t.Fatal(err)
	}
	os.Symlink(target, filepath.Join(work, "link"))
	if _, _, err := ValidatePayloadRoot(work, "link"); err == nil {
		t.Fatal("symlink payload accepted")
	}
	if _, _, err := ValidatePayloadRoot(work, filepath.Join("nested", "payload")); err == nil {
		t.Fatal("nested payload root accepted")
	}
}

func TestProbeNoReplaceExercisesCollisionAndIdentity(t *testing.T) {
	root := t.TempDir()
	source := filepath.Join(root, "stage")
	destination := filepath.Join(root, "target")
	if err := os.Mkdir(source, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(destination, 0o700); err != nil {
		t.Fatal(err)
	}
	if _, err := ProbeNoReplace(source, destination); err != nil {
		t.Fatal(err)
	}
	for _, directory := range []string{source, destination} {
		entries, err := os.ReadDir(directory)
		if err != nil || len(entries) != 0 {
			t.Fatalf("probe residue in %s: %v err=%v", directory, entries, err)
		}
	}
}
