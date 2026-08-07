package upgrade

import (
	"bytes"
	"context"
	"crypto/sha256"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func TestRunReplacesExecutableWithVerifiedRelease(t *testing.T) {
	executable := writeExecutable(t, "1.0.0")
	releaseBinary := executableScript("1.1.0")
	server := releaseServer(t, "v1.1.0", releaseBinary, checksumText(releaseBinary), http.StatusOK)

	result, err := Run(context.Background(), Options{
		CurrentVersion: "1.0.0",
		RepositoryURL:  server.URL + "/amio/aria2s",
		ExecutablePath: executable,
		Client:         server.Client(),
	})
	if err != nil {
		t.Fatal(err)
	}
	if !result.Updated || result.Current != "1.0.0" || result.Latest != "1.1.0" {
		t.Fatalf("result = %+v", result)
	}
	data, err := os.ReadFile(executable)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(data, releaseBinary) {
		t.Fatalf("installed executable = %q", data)
	}
}

func TestRunDoesNotDownloadWhenCurrentIsLatestOrNewer(t *testing.T) {
	for _, current := range []string{"1.1.0", "2.0.0"} {
		t.Run(current, func(t *testing.T) {
			downloads := 0
			server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
				switch request.URL.Path {
				case "/amio/aria2s/releases/latest":
					http.Redirect(writer, request, "/amio/aria2s/releases/tag/v1.1.0", http.StatusFound)
				case "/amio/aria2s/releases/tag/v1.1.0":
					writer.WriteHeader(http.StatusOK)
				default:
					downloads++
					http.NotFound(writer, request)
				}
			}))
			defer server.Close()
			result, err := Run(context.Background(), Options{
				CurrentVersion: current,
				RepositoryURL:  server.URL + "/amio/aria2s",
				Client:         server.Client(),
			})
			if err != nil {
				t.Fatal(err)
			}
			if result.Updated || downloads != 0 {
				t.Fatalf("result=%+v downloads=%d", result, downloads)
			}
		})
	}
}

func TestRunRejectsDevelopmentVersionBeforeNetwork(t *testing.T) {
	_, err := Run(context.Background(), Options{CurrentVersion: "dev"})
	if err == nil || !strings.Contains(err.Error(), "development version") {
		t.Fatalf("error = %v", err)
	}
}

func TestRunPreservesExecutableOnChecksumMismatch(t *testing.T) {
	executable := writeExecutable(t, "1.0.0")
	before, err := os.ReadFile(executable)
	if err != nil {
		t.Fatal(err)
	}
	releaseBinary := executableScript("1.1.0")
	server := releaseServer(t, "v1.1.0", releaseBinary, strings.Repeat("0", 64)+"  "+assetName("1.1.0")+"\n", http.StatusOK)

	_, err = Run(context.Background(), Options{
		CurrentVersion: "1.0.0",
		RepositoryURL:  server.URL + "/amio/aria2s",
		ExecutablePath: executable,
		Client:         server.Client(),
	})
	if err == nil || !strings.Contains(err.Error(), "checksum mismatch") {
		t.Fatalf("error = %v", err)
	}
	after, readErr := os.ReadFile(executable)
	if readErr != nil || !bytes.Equal(after, before) {
		t.Fatalf("executable changed: %q err=%v", after, readErr)
	}
}

func TestRunRejectsEmptyOrIncorrectReleaseBinary(t *testing.T) {
	tests := []struct {
		name    string
		content []byte
		want    string
	}{
		{name: "empty", content: nil, want: "release binary is empty"},
		{name: "wrong version", content: executableScript("9.0.0"), want: "expected 1.1.0"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			executable := writeExecutable(t, "1.0.0")
			before, _ := os.ReadFile(executable)
			server := releaseServer(t, "v1.1.0", test.content, checksumText(test.content), http.StatusOK)
			_, err := Run(context.Background(), Options{
				CurrentVersion: "1.0.0",
				RepositoryURL:  server.URL + "/amio/aria2s",
				ExecutablePath: executable,
				Client:         server.Client(),
			})
			if err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("error = %v", err)
			}
			after, _ := os.ReadFile(executable)
			if !bytes.Equal(after, before) {
				t.Fatal("old executable was replaced")
			}
		})
	}
}

func TestRunRequiresExactSingleChecksum(t *testing.T) {
	releaseBinary := executableScript("1.1.0")
	valid := checksumText(releaseBinary)
	for _, test := range []struct {
		name      string
		checksums string
		want      string
	}{
		{name: "missing", checksums: strings.Repeat("0", 64) + "  other.tar.gz\n", want: "no entry"},
		{name: "duplicate", checksums: valid + valid, want: "duplicate entries"},
	} {
		t.Run(test.name, func(t *testing.T) {
			executable := writeExecutable(t, "1.0.0")
			server := releaseServer(t, "v1.1.0", releaseBinary, test.checksums, http.StatusOK)
			_, err := Run(context.Background(), Options{
				CurrentVersion: "1.0.0",
				RepositoryURL:  server.URL + "/amio/aria2s",
				ExecutablePath: executable,
				Client:         server.Client(),
			})
			if err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("error = %v", err)
			}
		})
	}
}

func TestDetectLatestRejectsCrossHostRedirect(t *testing.T) {
	destination := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
		writer.WriteHeader(http.StatusOK)
	}))
	defer destination.Close()
	source := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		http.Redirect(writer, request, destination.URL+"/amio/aria2s/releases/tag/v1.1.0", http.StatusFound)
	}))
	defer source.Close()
	_, _, err := detectLatest(context.Background(), source.Client(), source.URL+"/amio/aria2s")
	if err == nil || !strings.Contains(err.Error(), "unexpected URL") {
		t.Fatalf("error = %v", err)
	}
}

func TestDownloadBinaryRejectsDeclaredOversize(t *testing.T) {
	destination, err := os.CreateTemp(t.TempDir(), "candidate-*")
	if err != nil {
		t.Fatal(err)
	}
	defer destination.Close()
	client := &http.Client{Transport: roundTripFunc(func(request *http.Request) (*http.Response, error) {
		return &http.Response{
			StatusCode:    http.StatusOK,
			Status:        "200 OK",
			Body:          io.NopCloser(strings.NewReader("x")),
			ContentLength: maxBinarySize + 1,
			Request:       request,
		}, nil
	})}
	_, err = downloadBinary(context.Background(), client, "https://example.test/aria2s", destination)
	if err == nil || !strings.Contains(err.Error(), "exceeds") {
		t.Fatalf("error = %v", err)
	}
}

func TestHomebrewPathsArePackageManagerOwned(t *testing.T) {
	if !isHomebrewPath("/opt/homebrew/Cellar/aria2s/1.0.0/bin/aria2s") {
		t.Fatal("Homebrew Cellar path was not detected")
	}
	if isHomebrewPath("/usr/local/bin/aria2s") {
		t.Fatal("ordinary install path was treated as Homebrew-owned")
	}
}

func TestParseVersionAndOrdering(t *testing.T) {
	parsed, err := parseVersion("v12.3.4")
	if err != nil || parsed.String() != "12.3.4" {
		t.Fatalf("parsed=%+v err=%v", parsed, err)
	}
	older, _ := parseVersion("12.3.3")
	newer, _ := parseVersion("12.4.0")
	if parsed.compare(older) <= 0 || parsed.compare(newer) >= 0 {
		t.Fatal("version ordering is incorrect")
	}
	for _, invalid := range []string{"dev", "1.2", "1.2.3-dirty", "01.2.3", ""} {
		if _, err := parseVersion(invalid); err == nil {
			t.Fatalf("parseVersion(%q) succeeded", invalid)
		}
	}
}

func writeExecutable(t *testing.T, releaseVersion string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "aria2s")
	if err := os.WriteFile(path, executableScript(releaseVersion), 0o755); err != nil {
		t.Fatal(err)
	}
	return path
}

func executableScript(releaseVersion string) []byte {
	return []byte("#!/bin/sh\nprintf 'aria2s version " + releaseVersion + "\\n'\n")
}

func checksumText(binary []byte) string {
	sum := sha256.Sum256(binary)
	return fmt.Sprintf("%x  %s\n", sum, assetName("1.1.0"))
}

func assetName(releaseVersion string) string {
	return fmt.Sprintf("aria2s_%s_%s_%s", releaseVersion, runtime.GOOS, runtime.GOARCH)
}

func releaseServer(t *testing.T, tag string, binary []byte, checksums string, binaryStatus int) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		base := "/amio/aria2s"
		switch request.URL.Path {
		case base + "/releases/latest":
			http.Redirect(writer, request, base+"/releases/tag/"+tag, http.StatusFound)
		case base + "/releases/tag/" + tag:
			writer.WriteHeader(http.StatusOK)
		case base + "/releases/download/" + tag + "/checksums.txt":
			fmt.Fprint(writer, checksums)
		case base + "/releases/download/" + tag + "/" + assetName(strings.TrimPrefix(tag, "v")):
			writer.WriteHeader(binaryStatus)
			if binaryStatus == http.StatusOK {
				writer.Write(binary)
			}
		default:
			http.NotFound(writer, request)
		}
	}))
}

type roundTripFunc func(*http.Request) (*http.Response, error)

func (function roundTripFunc) RoundTrip(request *http.Request) (*http.Response, error) {
	return function(request)
}
