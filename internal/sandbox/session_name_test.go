package sandbox

import (
	"os"
	"path/filepath"
	"regexp"
	"testing"
)

func TestSessionName(t *testing.T) {
	nameRegex := regexp.MustCompile(`^\d{4}-\d{2}-\d{2}T\d{2}-\d{2}-\d{2}-[a-z]+-[a-z]+-[0-9a-f]{8}$`)

	t.Run("FormatValidation", func(t *testing.T) {
		name, err := GenerateSessionName()
		if err != nil {
			t.Fatalf("GenerateSessionName failed: %v", err)
		}

		if !nameRegex.MatchString(name) {
			t.Errorf("name %q does not match regex %v", name, nameRegex.String())
		}
	})

	t.Run("Uniqueness", func(t *testing.T) {
		generated := make(map[string]bool)
		for i := 0; i < 1000; i++ {
			name, err := GenerateSessionName()
			if err != nil {
				t.Fatalf("GenerateSessionName failed: %v", err)
			}
			if generated[name] {
				t.Errorf("duplicate name generated: %q", name)
			}
			generated[name] = true
		}
	})

	t.Run("CreateSessionDir_Success", func(t *testing.T) {
		basePath := t.TempDir()

		sessionPath, err := CreateSessionDir(basePath)
		if err != nil {
			t.Fatalf("CreateSessionDir failed: %v", err)
		}

		if sessionPath == "" {
			t.Fatalf("returned sessionPath is empty")
		}

		info, err := os.Stat(sessionPath)
		if err != nil {
			t.Fatalf("failed to stat session dir: %v", err)
		}

		if !info.IsDir() {
			t.Errorf("expected session path to be a directory")
		}
	})

	t.Run("CreateSessionDir_BasePathDoesNotExist", func(t *testing.T) {
		basePath := t.TempDir()
		missingParent := filepath.Join(basePath, "does-not-exist")
		sessionPath, err := CreateSessionDir(missingParent)
		if err != nil {
			t.Fatalf("expected CreateSessionDir to create missing base path and succeed, got err: %v", err)
		}

		info, err := os.Stat(sessionPath)
		if err != nil {
			t.Fatalf("failed to stat session dir: %v", err)
		}
		if !info.IsDir() {
			t.Errorf("expected session path to be a directory")
		}
	})
	
	t.Run("CreateSessionDir_BasePathReadOnly", func(t *testing.T) {
		basePath := t.TempDir()
		readonlyParent := filepath.Join(basePath, "ro")
		err := os.Mkdir(readonlyParent, 0500)
		if err != nil {
			t.Fatalf("failed to create ro parent: %v", err)
		}
		
		_, err = CreateSessionDir(readonlyParent)
		if err == nil {
			t.Errorf("expected error when creating session dir in read-only base path")
		}
	})
}
