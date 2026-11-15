package main

import (
	"debug/elf"
	"encoding/binary"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"testing"

	"github.com/cilium/ebpf"
	"github.com/cilium/ebpf/asm"
	"github.com/cilium/ebpf/btf"
)

func TestKernelProbesLoadRealCallbackAndPacketAccess(t *testing.T) {
	var names []string
	maps := 0
	if err := validateKernel(func(spec *ebpf.ProgramSpec) error {
		if spec.License != "GPL" || len(spec.Instructions) < 2 {
			t.Fatal("incomplete kernel probe")
		}
		if spec.Type == ebpf.SchedCLS && len(spec.Instructions) < 8 {
			t.Fatal("missing TC callback/packet-access instructions")
		}
		names = append(names, spec.Name)
		return nil
	}, func(spec *ebpf.MapSpec) error {
		maps++
		if spec.Type != ebpf.SockHash || spec.KeySize != 40 || spec.ValueSize != 8 || spec.MaxEntries != 1 {
			t.Fatal("incorrect or oversized socket-map probe", spec)
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if len(names) != 5 || names[0] != "nl_loop" || names[1] != "nl_packet" || names[2] != "nl_sockops" || names[3] != "nl_skmsg" || names[4] != "nl_sockaddr" || maps != 1 {
		t.Fatal(names, maps)
	}
	var callback, loop bool
	functions := 0
	for _, instruction := range kernelProbeSpecs()[0].Instructions {
		if btf.FuncMetadata(&instruction) != nil {
			functions++
		}
		callback = callback || (instruction.IsLoadOfFunctionPointer() && instruction.Reference() == "callback")
		loop = loop || (instruction.OpCode == asm.FnLoop.Call().OpCode && instruction.Constant == int64(asm.FnLoop))
	}
	if !callback || !loop || functions != 2 {
		t.Fatal("helper-ID-only test cannot validate callback/JIT support")
	}
}

func TestKernelProbeFailureIsReturnedWithoutTryingLaterPrograms(t *testing.T) {
	want := errors.New("callback rejected")
	calls := 0
	err := validateKernel(func(*ebpf.ProgramSpec) error { calls++; return want }, func(*ebpf.MapSpec) error {
		t.Fatal("map probe must not run after a program failure")
		return nil
	})
	if !errors.Is(err, want) || calls != 1 {
		t.Fatal(err, calls)
	}
}

func TestSocketProbeFailuresStayFatal(t *testing.T) {
	for _, name := range []string{"nl_sockops", "nl_skmsg", "nl_sockaddr"} {
		t.Run(name, func(t *testing.T) {
			want := errors.New("socket program unavailable")
			err := validateKernel(func(spec *ebpf.ProgramSpec) error {
				if spec.Name == name {
					return want
				}
				return nil
			}, func(*ebpf.MapSpec) error {
				t.Fatal("must stop before allocating maps")
				return nil
			})
			if !errors.Is(err, want) {
				t.Fatal(err)
			}
		})
	}
	want := errors.New("SOCKHASH unavailable")
	err := validateKernel(func(*ebpf.ProgramSpec) error { return nil }, func(*ebpf.MapSpec) error { return want })
	if !errors.Is(err, want) {
		t.Fatal(err)
	}
}

func TestSocketProbeTypesAndVerdicts(t *testing.T) {
	for i, want := range []struct {
		kind    ebpf.ProgramType
		attach  ebpf.AttachType
		verdict int64
	}{
		{ebpf.SockOps, ebpf.AttachCGroupSockOps, 0},
		{ebpf.SkMsg, ebpf.AttachSkMsgVerdict, 1},
		{ebpf.CGroupSockAddr, ebpf.AttachCGroupInet4Connect, 1},
	} {
		spec := kernelProbeSpecs()[i+2]
		if spec.Type != want.kind || spec.AttachType != want.attach || len(spec.Instructions) != 2 || spec.Instructions[0].Constant != want.verdict || spec.Instructions[1].OpCode != asm.Return().OpCode {
			t.Fatalf("wrong socket compatibility probe: %+v", spec)
		}
	}
}

func TestELFArchitectureProfiles(t *testing.T) {
	for _, item := range []struct {
		arch    string
		class   elf.Class
		data    elf.Data
		machine elf.Machine
	}{
		{"amd64", elf.ELFCLASS64, elf.ELFDATA2LSB, elf.EM_X86_64},
		{"arm64", elf.ELFCLASS64, elf.ELFDATA2LSB, elf.EM_AARCH64},
		{"arm", elf.ELFCLASS32, elf.ELFDATA2LSB, elf.EM_ARM},
		{"mips", elf.ELFCLASS32, elf.ELFDATA2MSB, elf.EM_MIPS},
		{"mipsle", elf.ELFCLASS32, elf.ELFDATA2LSB, elf.EM_MIPS},
	} {
		class, data, machine := elfArchitecture(item.arch)
		if class != item.class || data != item.data || machine != item.machine {
			t.Fatal("wrong ELF ABI", item.arch, class, data, machine)
		}
	}
	if class, _, _ := elfArchitecture("unsupported"); class != elf.ELFCLASSNONE {
		t.Fatal("unsupported ABI accepted")
	}
}

func rawBTF(t *testing.T, names ...string) []byte {
	t.Helper()
	var types []btf.Type
	for _, name := range names {
		types = append(types, &btf.Struct{Name: name, Size: 4, Members: []btf.Member{{Name: "fixture", Type: &btf.Int{Name: "u32", Size: 4, Encoding: btf.Unsigned}}}})
	}
	builder, err := btf.NewBuilder(types)
	if err != nil {
		t.Fatal(err)
	}
	data, err := builder.Marshal(nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	return data
}

func TestValidateBTFValidRawAndSymlink(t *testing.T) {
	directory := t.TempDir()
	file := filepath.Join(directory, "vmlinux")
	if err := os.WriteFile(file, rawBTF(t, "sk_buff", "sock", "net_device"), 0600); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(directory, "vmlinux-6.12.94")
	if err := os.Symlink("vmlinux", link); err != nil {
		t.Fatal(err)
	}
	if err := validateBTF(link); err != nil {
		t.Fatal(err)
	}
}

func TestValidateBTFRejectsInvalidData(t *testing.T) {
	valid := rawBTF(t, "sk_buff", "sock", "net_device")
	wrongEndian := append([]byte(nil), valid...)
	wrongEndian[0], wrongEndian[1] = wrongEndian[1], wrongEndian[0]
	for name, data := range map[string][]byte{
		"empty": nil, "text": []byte("not BTF, despite a matching filename"),
		"truncated": valid[:24], "wrong-endian": wrongEndian,
		"missing-types": rawBTF(t, "other_type"),
	} {
		t.Run(name, func(t *testing.T) {
			file := filepath.Join(t.TempDir(), "vmlinux")
			if err := os.WriteFile(file, data, 0600); err != nil {
				t.Fatal(err)
			}
			if err := validateBTF(file); err == nil {
				t.Fatal("invalid BTF accepted")
			}
		})
	}
}

func TestValidateBTFRejectsForeignELF(t *testing.T) {
	data := make([]byte, 64)
	copy(data, []byte{'\x7f', 'E', 'L', 'F', 2, 1, 1})
	binary.LittleEndian.PutUint16(data[16:], 1)
	machine := uint16(183)
	if runtime.GOARCH == "arm64" {
		machine = 62
	}
	binary.LittleEndian.PutUint16(data[18:], machine)
	binary.LittleEndian.PutUint32(data[20:], 1)
	binary.LittleEndian.PutUint16(data[52:], 64)
	file := filepath.Join(t.TempDir(), "vmlinux")
	if err := os.WriteFile(file, data, 0600); err != nil {
		t.Fatal(err)
	}
	if err := validateBTF(file); err == nil {
		t.Fatal("foreign ELF accepted")
	}
}
