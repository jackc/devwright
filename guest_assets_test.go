package devsandbox

import (
	"bytes"
	"compress/gzip"
	"debug/elf"
	"debug/macho"
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

func TestEmbeddedDarwinArchitectures(t *testing.T) {
	for arch, cpu := range map[string]macho.Cpu{"arm64": macho.CpuArm64, "amd64": macho.CpuAmd64} {
		payload, err := recipe.ReadFile("guestbin/verify-darwin-" + arch + ".gz")
		if err != nil {
			t.Fatal(err)
		}
		reader, err := gzip.NewReader(bytes.NewReader(payload))
		if err != nil {
			t.Fatal(err)
		}
		binary, err := io.ReadAll(reader)
		reader.Close()
		if err != nil {
			t.Fatal(err)
		}
		executable, err := macho.NewFile(bytes.NewReader(binary))
		if err != nil {
			t.Fatal(err)
		}
		if executable.Cpu != cpu {
			t.Fatalf("unexpected Darwin CPU: %v", executable.Cpu)
		}
		executable.Close()
	}
}
