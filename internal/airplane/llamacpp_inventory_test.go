package airplane

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func TestLlamaCppInventoryBindsFilesModesSymlinksAndNoExtras(t *testing.T) {
	root := t.TempDir()
	t.Cleanup(func() {
		_ = filepath.WalkDir(root, func(path string, entry os.DirEntry, err error) error {
			if err == nil && entry.Type()&os.ModeSymlink == 0 {
				_ = os.Chmod(path, 0o700)
			}
			return nil
		})
	})
	binary := filepath.Join(root, "llama-server")
	library := filepath.Join(root, "libllama.1.dylib")
	if err := os.WriteFile(binary, []byte("server"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(library, []byte("library"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink("libllama.1.dylib", filepath.Join(root, "libllama.dylib")); err != nil {
		t.Fatal(err)
	}
	if err := makeLlamaCppTreeReadOnly(root); err != nil {
		t.Fatal(err)
	}
	original, err := llamaCppInventorySHA256(root)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(library, 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := llamaCppInventorySHA256(root); err == nil {
		t.Fatal("writable runtime file was accepted")
	}
	if err := os.WriteFile(library, []byte("changed"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(library, 0o444); err != nil {
		t.Fatal(err)
	}
	changed, err := llamaCppInventorySHA256(root)
	if err != nil {
		t.Fatal(err)
	}
	if changed == original {
		t.Fatal("runtime file mutation did not change inventory")
	}
	if err := os.Chmod(root, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "extra"), []byte("extra"), 0o444); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(root, 0o555); err != nil {
		t.Fatal(err)
	}
	extra, err := llamaCppInventorySHA256(root)
	if err != nil {
		t.Fatal(err)
	}
	if extra == changed {
		t.Fatal("extra runtime file did not change inventory")
	}
}

func TestLlamaCppInventoryRejectsEntryCountAndExtendedAttributes(t *testing.T) {
	root := t.TempDir()
	t.Cleanup(func() { _ = os.Chmod(root, 0o700) })
	for index := 0; index < llamaDistributionMaxEntries; index++ {
		if err := os.Symlink("target", filepath.Join(root, fmt.Sprintf("link-%04d", index))); err != nil {
			t.Fatal(err)
		}
	}
	if err := makeLlamaCppTreeReadOnly(root); err != nil {
		t.Fatal(err)
	}
	if _, err := llamaCppInventorySHA256(root); err == nil || !strings.Contains(err.Error(), "entries") {
		t.Fatalf("oversized inventory was accepted: %v", err)
	}

	if runtime.GOOS == "darwin" {
		xattrRoot := t.TempDir()
		t.Cleanup(func() { _ = os.Chmod(xattrRoot, 0o700) })
		file := filepath.Join(xattrRoot, "llama-server")
		if err := os.WriteFile(file, []byte("server"), 0o444); err != nil {
			t.Fatal(err)
		}
		if output, err := exec.Command("xattr", "-w", "s46.test", "present", file).CombinedOutput(); err != nil {
			t.Skipf("cannot seed extended attribute: %v: %s", err, output)
		}
		if err := os.Chmod(xattrRoot, 0o555); err != nil {
			t.Fatal(err)
		}
		if _, err := llamaCppInventorySHA256(xattrRoot); err == nil || !strings.Contains(err.Error(), "extended attribute") {
			t.Fatalf("extended attributes were accepted: %v", err)
		}
	}
}

func TestEmbeddedLlamaCppInventoryMatchesReviewedArchive(t *testing.T) {
	manifest, err := embeddedAirplaneReleaseManifest()
	if err != nil {
		t.Fatal(err)
	}
	pin, ok := manifest.LlamaCpp.Platforms["darwin/arm64"]
	if !ok {
		t.Fatal("missing darwin/arm64 llama.cpp pin")
	}
	if pin.InventorySHA256 != "5188ea71039a80e56a3026fa4c7a823e6b550887a50475fe9c45001ee436c167" {
		t.Fatalf("unexpected reviewed runtime inventory: %s", pin.InventorySHA256)
	}
}
