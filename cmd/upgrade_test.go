package cmd

import (
	"bytes"
	"context"
	"errors"
	"io"
	"strings"
	"testing"

	"github.com/amio/aria2s/internal/upgrade"
)

func TestUpgradeCommandReportsReplacement(t *testing.T) {
	command := newUpgradeCommandWith(func(_ context.Context, options upgrade.Options) (upgrade.Result, error) {
		if options.CurrentVersion != Version {
			t.Fatalf("current version = %q", options.CurrentVersion)
		}
		return upgrade.Result{Current: "1.0.0", Latest: "1.1.0", Updated: true}, nil
	}, func(context.Context) (bool, error) { return true, nil }, nil, func() int { return 501 })
	var output bytes.Buffer
	command.SetOut(&output)
	if err := command.Execute(); err != nil {
		t.Fatal(err)
	}
	if got := output.String(); !strings.Contains(got, "Updated aria2s 1.0.0 → 1.1.0") || !strings.Contains(got, "were not restarted") {
		t.Fatalf("output = %q", got)
	}
}

func TestUpgradeCommandReportsCurrentRelease(t *testing.T) {
	command := newUpgradeCommandWith(func(context.Context, upgrade.Options) (upgrade.Result, error) {
		return upgrade.Result{Current: "1.1.0", Latest: "1.1.0"}, nil
	}, func(context.Context) (bool, error) { return false, nil }, nil, func() int { return 501 })
	var output bytes.Buffer
	command.SetOut(&output)
	if err := command.Execute(); err != nil {
		t.Fatal(err)
	}
	if got := output.String(); got != "aria2s is already up to date (1.1.0).\n" {
		t.Fatalf("output = %q", got)
	}
}

func TestUpgradeCommandRetriesOnlyPrivilegeFailureWithSudo(t *testing.T) {
	called := false
	command := newUpgradeCommandWith(func(context.Context, upgrade.Options) (upgrade.Result, error) {
		return upgrade.Result{}, &upgrade.PrivilegeError{ExecutablePath: "/usr/local/bin/aria2s"}
	}, func(context.Context) (bool, error) { return true, nil }, func(_ context.Context, path string, _ io.Reader, _, _ io.Writer) error {
		called = true
		if path != "/usr/local/bin/aria2s" {
			t.Fatalf("path = %q", path)
		}
		return nil
	}, func() int { return 501 })
	var output bytes.Buffer
	command.SetOut(&output)
	if err := command.Execute(); err != nil {
		t.Fatal(err)
	}
	if !called || !strings.Contains(output.String(), "retrying with sudo") {
		t.Fatalf("called=%v output=%q", called, output.String())
	}
}

func TestUpgradeCommandDoesNotRetryOtherOrRootFailures(t *testing.T) {
	for _, test := range []struct {
		name string
		uid  int
		err  error
	}{
		{name: "ordinary", uid: 501, err: errors.New("network unavailable")},
		{name: "root permission", uid: 0, err: &upgrade.PrivilegeError{ExecutablePath: "/usr/local/bin/aria2s"}},
	} {
		t.Run(test.name, func(t *testing.T) {
			command := newUpgradeCommandWith(func(context.Context, upgrade.Options) (upgrade.Result, error) {
				return upgrade.Result{}, test.err
			}, func(context.Context) (bool, error) { return false, nil }, func(context.Context, string, io.Reader, io.Writer, io.Writer) error {
				t.Fatal("unexpected sudo retry")
				return nil
			}, func() int { return test.uid })
			if err := command.Execute(); !errors.Is(err, test.err) {
				t.Fatalf("error = %v", err)
			}
		})
	}
}

func TestUpgradeCommandReportsCommittedBinaryWhenRebindFails(t *testing.T) {
	command := newUpgradeCommandWith(func(context.Context, upgrade.Options) (upgrade.Result, error) {
		return upgrade.Result{Current: "1.0.0", Latest: "1.1.0", Updated: true}, nil
	}, func(context.Context) (bool, error) {
		return false, errors.New("state is locked")
	}, nil, func() int { return 501 })
	var output bytes.Buffer
	command.SetOut(&output)
	err := command.Execute()
	if err == nil || !strings.Contains(err.Error(), "was updated") || !strings.Contains(err.Error(), "aria2s install") {
		t.Fatalf("error = %v", err)
	}
	if !strings.Contains(output.String(), "Updated aria2s 1.0.0 → 1.1.0") {
		t.Fatalf("output = %q", output.String())
	}
}
