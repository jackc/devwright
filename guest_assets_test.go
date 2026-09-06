package devsandbox

import (
	"bytes"
	"compress/gzip"
	"debug/elf"
	"io"
	"testing"
)

func TestEmbeddedGuestArchitectures(t *testing.T) {
	for arch, machine := range map[string]elf.Machine{"arm64": elf.EM_AARCH64, "amd64": elf.EM_X86_64} {
		t.Run(arch, func(t *testing.T) {
			payload, err := recipe.ReadFile("guestbin/verify-linux-" + arch + ".gz")
			if err != nil {
				t.Fatal(err)
			}
			reader, err := gzip.NewReader(bytes.NewReader(payload))
			if err != nil {
				t.Fatal(err)
			}
			defer reader.Close()
			binary, err := io.ReadAll(reader)
			if err != nil {
				t.Fatal(err)
			}
			executable, err := elf.NewFile(bytes.NewReader(binary))
			if err != nil {
				t.Fatal(err)
			}
			defer executable.Close()
			if executable.Machine != machine {
				t.Fatalf("guest architecture: %s", executable.Machine)
			}
			for _, program := range executable.Progs {
				if program.Type == elf.PT_INTERP {
					t.Fatal("guest verifier requires a dynamic loader")
				}
			}
		})
	}
}
