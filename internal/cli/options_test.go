package cli

import (
	"errors"
	"reflect"
	"testing"
)

func TestParseServe(t *testing.T) {
	tests := []struct {
		name      string
		arguments []string
		want      Options
		wantError bool
	}{
		{
			name: "defaults to working directory",
			want: Options{Command: Serve, Directory: "/workspace"},
		},
		{
			name:      "explicit directory and port",
			arguments: []string{"notes", "--port", "4242"},
			want: Options{
				Command:      Serve,
				Directory:    "notes",
				Port:         4242,
				PortExplicit: true,
			},
		},
		{
			name:      "equals port syntax before directory",
			arguments: []string{"--port=65535", "notes"},
			want: Options{
				Command:      Serve,
				Directory:    "notes",
				Port:         65535,
				PortExplicit: true,
			},
		},
		{name: "duplicate port", arguments: []string{"--port", "1", "--port=2"}, wantError: true},
		{name: "managed mode removed", arguments: []string{"--managed-session"}, wantError: true},
		{name: "missing port", arguments: []string{"--port"}, wantError: true},
		{name: "empty equals port", arguments: []string{"--port="}, wantError: true},
		{name: "zero port", arguments: []string{"--port=0"}, wantError: true},
		{name: "out of range port", arguments: []string{"--port=65536"}, wantError: true},
		{name: "non numeric port", arguments: []string{"--port=abc"}, wantError: true},
		{name: "unknown option", arguments: []string{"--other"}, wantError: true},
		{name: "extra directory", arguments: []string{"one", "two"}, wantError: true},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			got, err := Parse(test.arguments, fixedWorkingDirectory("/workspace"))
			if test.wantError {
				if err == nil {
					t.Fatal("Parse() error = nil, want an error")
				}
				return
			}
			if err != nil {
				t.Fatalf("Parse() error = %v", err)
			}
			if !reflect.DeepEqual(got, test.want) {
				t.Fatalf("Parse() = %#v, want %#v", got, test.want)
			}
		})
	}
}

func TestParseManagementCommands(t *testing.T) {
	failingWorkingDirectory := func() (string, error) {
		return "", errors.New("working directory disappeared")
	}
	tests := []struct {
		name      string
		arguments []string
		want      Options
		wantError bool
	}{
		{name: "version", arguments: []string{"--version"}, want: Options{Command: Version}},
		{name: "version rejects arguments", arguments: []string{"--version", "notes"}, wantError: true},
		{name: "setup", arguments: []string{"setup"}, want: Options{Command: Setup}},
		{name: "setup rejects arguments", arguments: []string{"setup", "--managed-session"}, wantError: true},
		{name: "skill status", arguments: []string{"skill", "status"}, want: Options{Command: SkillStatus}},
		{name: "skill missing action", arguments: []string{"skill"}, wantError: true},
		{name: "skill unknown action", arguments: []string{"skill", "repair"}, wantError: true},
		{
			name:      "install targets",
			arguments: []string{"skill", "install", "--target", "codex", "--target=claude", "--target", "pi"},
			want: Options{
				Command: SkillInstall,
				Targets: []Target{TargetCodex, TargetClaude, TargetPi},
			},
		},
		{
			name:      "uninstall explicit target",
			arguments: []string{"skill", "uninstall", "--target", "claude"},
			want: Options{
				Command: SkillUninstall,
				Targets: []Target{TargetClaude},
			},
		},
		{name: "install requires target", arguments: []string{"skill", "install"}, wantError: true},
		{name: "uninstall requires target", arguments: []string{"skill", "uninstall"}, wantError: true},
		{name: "duplicate target", arguments: []string{"skill", "install", "--target", "codex", "--target=codex"}, wantError: true},
		{name: "unknown target", arguments: []string{"skill", "install", "--target", "other"}, wantError: true},
		{name: "install rejects force", arguments: []string{"skill", "install", "--target", "codex", "--force"}, wantError: true},
		{name: "status rejects target", arguments: []string{"skill", "status", "--target", "codex"}, wantError: true},
		{name: "install rejects managed", arguments: []string{"skill", "install", "--target", "codex", "--managed-session"}, wantError: true},
		{name: "install rejects positional", arguments: []string{"skill", "install", "codex"}, wantError: true},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			got, err := Parse(test.arguments, failingWorkingDirectory)
			if test.wantError {
				if err == nil {
					t.Fatal("Parse() error = nil, want an error")
				}
				return
			}
			if err != nil {
				t.Fatalf("Parse() error = %v", err)
			}
			if !reflect.DeepEqual(got, test.want) {
				t.Fatalf("Parse() = %#v, want %#v", got, test.want)
			}
		})
	}
}

func TestParseServeReportsWorkingDirectoryFailure(t *testing.T) {
	want := errors.New("working directory disappeared")
	_, err := Parse(nil, func() (string, error) {
		return "", want
	})
	if !errors.Is(err, want) {
		t.Fatalf("Parse() error = %v, want wrapped %v", err, want)
	}
}

func TestParseServeRejectsMissingWorkingDirectoryProvider(t *testing.T) {
	if _, err := Parse(nil, nil); err == nil {
		t.Fatal("Parse() error = nil, want an error")
	}
}

func fixedWorkingDirectory(directory string) func() (string, error) {
	return func() (string, error) {
		return directory, nil
	}
}
