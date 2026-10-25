package airplane

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

type llamaArchiveMember struct {
	name     string
	linkname string
	typeflag byte
	mode     int64
	content  string
}

func TestExtractLlamaCppDistributionRejectsUnsafeMembers(t *testing.T) {
	for _, test := range []struct {
		name    string
		members []llamaArchiveMember
	}{
		{name: "traversal", members: []llamaArchiveMember{{name: "../escape", typeflag: tar.TypeReg, mode: 0o644, content: "bad"}}},
		{name: "absolute", members: []llamaArchiveMember{{name: "/escape", typeflag: tar.TypeReg, mode: 0o644, content: "bad"}}},
		{name: "parent-symlink", members: []llamaArchiveMember{{name: "runtime", typeflag: tar.TypeDir, mode: 0o755}, {name: "runtime/link", linkname: "..", typeflag: tar.TypeSymlink}, {name: "runtime/link/escape", typeflag: tar.TypeReg, mode: 0o644, content: "bad"}}},
		{name: "duplicate", members: []llamaArchiveMember{{name: "runtime", typeflag: tar.TypeDir, mode: 0o755}, {name: "runtime/llama-server", typeflag: tar.TypeReg, mode: 0o755, content: "one"}, {name: "runtime/llama-server", typeflag: tar.TypeReg, mode: 0o755, content: "two"}}},
	} {
		t.Run(test.name, func(t *testing.T) {
			destination := t.TempDir()
			if err := extractLlamaCppDistribution(llamaArchive(t, test.members...), destination); err == nil {
				t.Fatal("unsafe archive was accepted")
			}
			if _, err := os.Lstat(filepath.Join(filepath.Dir(destination), "escape")); !os.IsNotExist(err) {
				t.Fatalf("archive wrote outside destination: %v", err)
			}
		})
	}
}

func TestExtractLlamaCppDistributionRejectsBoundViolationsAndExtendedMetadata(t *testing.T) {
	tooMany := make([]llamaArchiveMember, llamaDistributionMaxEntries+1)
	for index := range tooMany {
		tooMany[index] = llamaArchiveMember{name: fmt.Sprintf("runtime/dir-%04d", index), typeflag: tar.TypeDir, mode: 0o755}
	}
	if err := extractLlamaCppDistribution(llamaArchive(t, tooMany...), t.TempDir()); err == nil || !strings.Contains(err.Error(), "members") {
		t.Fatalf("oversized member cohort was accepted: %v", err)
	}
	longName := "runtime/" + strings.Repeat("a", archivePathMaxBytes)
	if err := extractLlamaCppDistribution(llamaArchive(t, llamaArchiveMember{name: longName, typeflag: tar.TypeReg, mode: 0o644, content: "x"}), t.TempDir()); err == nil || !strings.Contains(err.Error(), "metadata") {
		t.Fatalf("oversized path was accepted: %v", err)
	}
	archive := archiveWithPAXRecord(t, "runtime/llama-server")
	if err := extractLlamaCppDistribution(archive, t.TempDir()); err == nil || !strings.Contains(err.Error(), "extended metadata") {
		t.Fatalf("PAX metadata was accepted: %v", err)
	}
}

func TestGatewayArchiveRejectsUnexpectedAndSpecialMembers(t *testing.T) {
	for _, member := range []llamaArchiveMember{
		{name: "README", typeflag: tar.TypeReg, mode: 0o644, content: "unexpected"},
		{name: GatewayBinaryName, typeflag: tar.TypeSymlink, mode: 0o777, linkname: "other"},
		{name: GatewayBinaryName, typeflag: tar.TypeFifo, mode: 0o600},
	} {
		if _, err := gatewayBinaryFromArchive(llamaArchive(t, member)); err == nil {
			t.Fatalf("gateway archive member %#v was accepted", member)
		}
	}
	if _, err := gatewayBinaryFromArchive(archiveWithPAXRecord(t, GatewayBinaryName)); err == nil || !strings.Contains(err.Error(), "extended metadata") {
		t.Fatalf("gateway PAX metadata was accepted: %v", err)
	}
}

func TestLlamaExtractedSizeRejectsOverflowBeforeAddition(t *testing.T) {
	if _, err := addLlamaExtractedSize(llamaDistributionMaxBytes-1, int64(^uint64(0)>>1)); err == nil {
		t.Fatal("overflowing extracted size was accepted")
	}
	if total, err := addLlamaExtractedSize(llamaDistributionMaxBytes-1, 1); err != nil || total != llamaDistributionMaxBytes {
		t.Fatalf("bounded extracted size rejected: total=%d err=%v", total, err)
	}
}

func TestVerifyLlamaCppBinaryRejectsDigestAndVersionMismatch(t *testing.T) {
	binary := filepath.Join(t.TempDir(), "llama-server")
	content := []byte("#!/bin/sh\nprintf 'version: wrong\\n'\n")
	if err := os.WriteFile(binary, content, 0o755); err != nil {
		t.Fatal(err)
	}
	pin := llamaCppResolvedPin{llamaCppPlatformPin: llamaCppPlatformPin{BinarySHA256: strings.Repeat("0", 64), BinarySize: int64(len(content)), VersionOutput: "version: expected"}}
	if err := verifyLlamaCppBinary(t.Context(), binary, pin); err == nil || !strings.Contains(err.Error(), "SHA-256") {
		t.Fatalf("digest mismatch was accepted: %v", err)
	}
	pin.BinarySHA256 = sha256Hex(content)
	if err := verifyLlamaCppBinary(t.Context(), binary, pin); err == nil || !strings.Contains(err.Error(), "version output") {
		t.Fatalf("version mismatch was accepted: %v", err)
	}
}

func archiveWithPAXRecord(t *testing.T, name string) []byte {
	t.Helper()
	var buffer bytes.Buffer
	gzipWriter := gzip.NewWriter(&buffer)
	tarWriter := tar.NewWriter(gzipWriter)
	content := []byte("x")
	header := &tar.Header{Name: name, Typeflag: tar.TypeReg, Mode: 0o755, Size: 1, PAXRecords: map[string]string{"comment": "unsupported"}}
	if err := tarWriter.WriteHeader(header); err != nil {
		t.Fatal(err)
	}
	if _, err := tarWriter.Write(content); err != nil {
		t.Fatal(err)
	}
	if err := tarWriter.Close(); err != nil {
		t.Fatal(err)
	}
	if err := gzipWriter.Close(); err != nil {
		t.Fatal(err)
	}
	return buffer.Bytes()
}

func llamaArchive(t *testing.T, members ...llamaArchiveMember) []byte {
	t.Helper()
	var buffer bytes.Buffer
	gzipWriter := gzip.NewWriter(&buffer)
	tarWriter := tar.NewWriter(gzipWriter)
	for _, member := range members {
		header := &tar.Header{Name: member.name, Linkname: member.linkname, Typeflag: member.typeflag, Mode: member.mode, Size: int64(len(member.content))}
		if err := tarWriter.WriteHeader(header); err != nil {
			t.Fatal(err)
		}
		if member.content != "" {
			if _, err := tarWriter.Write([]byte(member.content)); err != nil {
				t.Fatal(err)
			}
		}
	}
	if err := tarWriter.Close(); err != nil {
		t.Fatal(err)
	}
	if err := gzipWriter.Close(); err != nil {
		t.Fatal(err)
	}
	return buffer.Bytes()
}
