package main

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestInstallerRequiresVerifiedArchive(t *testing.T) {
	for _, platform := range []string{"Darwin", "Linux"} {
		t.Run(platform, func(t *testing.T) {
			for _, test := range []struct {
				name      string
				checksums string
				download  string
				hashFails bool
				valid     bool
			}{
				{name: "download fails", download: "fail"},
				{name: "partial download fails", download: "partial", checksums: "HASH  ASSET\n"},
				{name: "empty file"},
				{name: "missing asset", checksums: "HASH  other.tar.gz\n"},
				{name: "regex lookalike", checksums: "HASH  LOOKALIKE\n"},
				{name: "malformed digest", checksums: strings.Repeat("z", 64) + "  ASSET\n"},
				{name: "short digest", checksums: "1234  ASSET\n"},
				{name: "extra field", checksums: "HASH  ASSET unexpected\n"},
				{name: "mismatch", checksums: strings.Repeat("0", 64) + "  ASSET\n"},
				{name: "duplicate", checksums: "HASH  ASSET\nHASH  ASSET\n"},
				{name: "conflicting duplicate", checksums: "HASH  ASSET\n" + strings.Repeat("0", 64) + "  ASSET\n"},
				{name: "malformed duplicate", checksums: "HASH  ASSET\nbroken  ASSET\n"},
				{name: "hasher fails", checksums: "HASH  ASSET\n", hashFails: true},
				{name: "valid", checksums: "HASH  ASSET\n", valid: true},
				{name: "valid exact asset among others", checksums: "HASH  LOOKALIKE\nHASH  ASSET\nHASH  other.tar.gz\n", valid: true},
			} {
				t.Run(test.name, func(t *testing.T) {
					fixture := newInstallerFixture(t, platform)
					checksums := strings.NewReplacer(
						"HASH", fixture.digest,
						"ASSET", fixture.asset,
						"LOOKALIKE", strings.ReplaceAll(fixture.asset, ".", "x"),
					).Replace(test.checksums)
					writeInstallerFile(t, filepath.Join(fixture.root, "checksums.txt"), checksums, 0o600)
					ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
					defer cancel()
					command := exec.CommandContext(ctx, "/bin/sh", "install.sh", "--version", "v1.2.3")
					command.Env = append(fixture.env,
						"INSTALL_TEST_DOWNLOAD="+test.download,
						fmt.Sprintf("INSTALL_TEST_HASH_FAIL=%t", test.hashFails),
					)
					output, err := command.CombinedOutput()
					if (err == nil) != test.valid {
						t.Fatalf("install error = %v, want success %t\n%s", err, test.valid, output)
					}
					if ctx.Err() != nil {
						t.Fatalf("installer timed out: %v\n%s", ctx.Err(), output)
					}
					installed, err := os.ReadFile(filepath.Join(fixture.bindir, "aria2s"))
					if err != nil {
						t.Fatal(err)
					}
					calls, err := os.ReadFile(filepath.Join(fixture.root, "calls"))
					if err != nil && !os.IsNotExist(err) {
						t.Fatal(err)
					}
					if test.valid {
						if string(installed) != installerCandidate {
							t.Fatalf("candidate was not installed: %q", installed)
						}
						hasher := "shasum"
						if platform == "Linux" {
							hasher = "sha256sum"
						}
						wantCalls := hasher + "\ntar\ncandidate:version\ncandidate:install --start\n"
						if string(calls) != wantCalls {
							t.Fatalf("calls = %q, want %q", calls, wantCalls)
						}
					} else {
						if string(installed) != "previous installed binary\n" {
							t.Fatalf("failed verification replaced installed binary: %q", installed)
						}
						if strings.Contains(string(calls), "tar\n") || strings.Contains(string(calls), "candidate:") {
							t.Fatalf("failed verification extracted or executed candidate: %q", calls)
						}
					}
					entries, err := os.ReadDir(fixture.bindir)
					if err != nil || len(entries) != 1 || entries[0].Name() != "aria2s" {
						t.Fatalf("unexpected install artifacts: %v, %v", entries, err)
					}
				})
			}
		})
	}
}

const installerCandidate = `#!/bin/sh
printf 'candidate:%s\n' "$*" >> "$INSTALL_TEST_ROOT/calls"
case "$*" in
  version|"install --start") exit 0 ;;
  *) exit 91 ;;
esac
`

type installerFixture struct {
	root   string
	bindir string
	asset  string
	digest string
	env    []string
}

func newInstallerFixture(t *testing.T, platform string) installerFixture {
	t.Helper()
	root := t.TempDir()
	stubdir := filepath.Join(root, "commands")
	bindir := filepath.Join(root, "installed binaries")
	for _, directory := range []string{stubdir, bindir} {
		if err := os.Mkdir(directory, 0o700); err != nil {
			t.Fatal(err)
		}
	}
	writeInstallerFile(t, filepath.Join(bindir, "aria2s"), "previous installed binary\n", 0o755)
	var archive bytes.Buffer
	compressed := gzip.NewWriter(&archive)
	tarball := tar.NewWriter(compressed)
	if err := tarball.WriteHeader(&tar.Header{Name: "aria2s", Mode: 0o755, Size: int64(len(installerCandidate))}); err != nil {
		t.Fatal(err)
	}
	if _, err := tarball.Write([]byte(installerCandidate)); err != nil {
		t.Fatal(err)
	}
	if err := tarball.Close(); err != nil {
		t.Fatal(err)
	}
	if err := compressed.Close(); err != nil {
		t.Fatal(err)
	}
	writeInstallerFile(t, filepath.Join(root, "archive.tar.gz"), archive.String(), 0o600)
	realTar, err := exec.LookPath("tar")
	if err != nil {
		t.Fatal(err)
	}
	realHasher, err := exec.LookPath("shasum")
	hashCommand := `exec "$INSTALL_TEST_HASHER" -a 256 "$@"`
	if err != nil {
		realHasher, err = exec.LookPath("sha256sum")
		hashCommand = `exec "$INSTALL_TEST_HASHER" "$@"`
		if err != nil {
			t.Fatal("installer tests require shasum or sha256sum")
		}
	}
	for name, body := range map[string]string{
		"uname": `case "$1" in -s) printf '%s\n' "$INSTALL_TEST_PLATFORM" ;; -m) echo arm64 ;; *) exit 92 ;; esac`,
		"curl": `
while [ "$#" -gt 0 ]; do
  case "$1" in
    -o) output="$2"; shift 2 ;;
    https://*) url="$1"; shift ;;
    *) shift ;;
  esac
done
case "$url" in
  */checksums.txt)
    [ "$INSTALL_TEST_DOWNLOAD" != fail ] || exit 22
    cp "$INSTALL_TEST_ROOT/checksums.txt" "$output"
    [ "$INSTALL_TEST_DOWNLOAD" != partial ] || exit 22
    ;;
  */"$INSTALL_TEST_ASSET") cp "$INSTALL_TEST_ROOT/archive.tar.gz" "$output" ;;
  *) exit 93 ;;
esac`,
		"tar": `printf 'tar\n' >> "$INSTALL_TEST_ROOT/calls"
exec "$INSTALL_TEST_TAR" "$@"`,
		"shasum": `printf 'shasum\n' >> "$INSTALL_TEST_ROOT/calls"
[ "$#" -eq 3 ] && [ "$1" = -a ] && [ "$2" = 256 ] || exit 94
shift 2
[ "$INSTALL_TEST_HASH_FAIL" != true ] || exit 95
` + hashCommand,
		"sha256sum": `printf 'sha256sum\n' >> "$INSTALL_TEST_ROOT/calls"
[ "$#" -eq 1 ] || exit 94
[ "$INSTALL_TEST_HASH_FAIL" != true ] || exit 95
` + hashCommand,
		"sudo":      `echo 'unexpected sudo invocation' >&2; exit 96`,
		"systemctl": `exit 97`,
	} {
		writeInstallerFile(t, filepath.Join(stubdir, name), "#!/bin/sh\nset -eu\n"+body+"\n", 0o755)
	}
	asset := "aria2s_1.2.3_" + strings.ToLower(platform) + "_arm64.tar.gz"
	return installerFixture{
		root: root, bindir: bindir, asset: asset,
		digest: fmt.Sprintf("%x", sha256.Sum256(archive.Bytes())),
		env: append(os.Environ(),
			"PATH="+stubdir+string(os.PathListSeparator)+os.Getenv("PATH"),
			"BINDIR="+bindir, "INSTALL_TEST_ROOT="+root,
			"INSTALL_TEST_PLATFORM="+platform, "INSTALL_TEST_ASSET="+asset,
			"INSTALL_TEST_TAR="+realTar, "INSTALL_TEST_HASHER="+realHasher,
		),
	}
}

func writeInstallerFile(t *testing.T, path, content string, mode os.FileMode) {
	t.Helper()
	if err := os.WriteFile(path, []byte(content), mode); err != nil {
		t.Fatal(err)
	}
}
