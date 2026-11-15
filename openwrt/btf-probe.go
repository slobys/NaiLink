// SPDX-License-Identifier: MIT
// Build from wing/ to reuse the runtime's existing cilium/ebpf dependency.
package main

import (
	"debug/elf"
	"encoding/binary"
	"flag"
	"fmt"
	"os"
	"runtime"

	"github.com/cilium/ebpf"
	"github.com/cilium/ebpf/asm"
	"github.com/cilium/ebpf/btf"
	"github.com/cilium/ebpf/rlimit"
)

func elfArchitecture(arch string) (elf.Class, elf.Data, elf.Machine) {
	switch arch {
	case "amd64":
		return elf.ELFCLASS64, elf.ELFDATA2LSB, elf.EM_X86_64
	case "arm64":
		return elf.ELFCLASS64, elf.ELFDATA2LSB, elf.EM_AARCH64
	case "arm":
		return elf.ELFCLASS32, elf.ELFDATA2LSB, elf.EM_ARM
	case "mips":
		return elf.ELFCLASS32, elf.ELFDATA2MSB, elf.EM_MIPS
	case "mipsle":
		return elf.ELFCLASS32, elf.ELFDATA2LSB, elf.EM_MIPS
	default:
		return elf.ELFCLASSNONE, elf.ELFDATANONE, elf.EM_NONE
	}
}

func validateBTF(path string) error {
	file, err := os.Open(path)
	if err != nil {
		return err
	}
	defer file.Close()
	info, err := file.Stat()
	if err != nil || !info.Mode().IsRegular() || info.Size() < 24 || info.Size() > 128*1024*1024 {
		return fmt.Errorf("BTF must be a regular file between 24 bytes and 128 MiB")
	}
	var header [4]byte
	if _, err = file.ReadAt(header[:], 0); err != nil {
		return err
	}
	if string(header[:]) == "\x7fELF" {
		image, err := elf.NewFile(file)
		if err != nil {
			return err
		}
		class, data, machine := elfArchitecture(runtime.GOARCH)
		if class == elf.ELFCLASSNONE || image.Class != class || image.Data != data || image.Machine != machine {
			return fmt.Errorf("vmlinux ELF architecture does not match this router")
		}
	} else if binary.NativeEndian.Uint16(header[:2]) != 0xeb9f || header[2] != 1 {
		return fmt.Errorf("invalid native-endian BTF header")
	}
	spec, err := btf.LoadSpec(path)
	if err != nil {
		return fmt.Errorf("parse BTF: %w", err)
	}
	for _, name := range []string{"sk_buff", "sock", "net_device"} {
		if _, err := spec.AnyTypeByName(name); err != nil {
			return fmt.Errorf("required kernel type %s: %w", name, err)
		}
	}
	return nil
}

// A helper-ID/version check is insufficient: 32-bit kernels can recognize
// bpf_loop while their JIT cannot load callbacks. Load real, unattached probe
// programs and close every FD; do not change sysctls, interfaces or routing.
func kernelProbeSpecs() []*ebpf.ProgramSpec {
	callback := asm.LoadImm(asm.R2, -1, asm.DWord).WithReference("callback")
	callback.Src = asm.PseudoFunc
	integer := &btf.Int{Name: "int", Size: 4, Encoding: btf.Signed}
	voidPointer := &btf.Pointer{Target: &btf.Void{}}
	entry := btf.WithFuncMetadata(asm.Mov.Imm(asm.R1, 1), &btf.Func{Name: "nl_loop", Type: &btf.FuncProto{
		Return: integer, Params: []btf.FuncParam{{Name: "ctx", Type: voidPointer}},
	}})
	callbackEntry := btf.WithFuncMetadata(asm.Mov.Imm(asm.R0, 1).WithSymbol("callback"), &btf.Func{Name: "callback", Type: &btf.FuncProto{
		Return: integer, Params: []btf.FuncParam{{Name: "index", Type: &btf.Int{Name: "u32", Size: 4}}, {Name: "ctx", Type: voidPointer}},
	}})
	return []*ebpf.ProgramSpec{
		{Name: "nl_loop", Type: ebpf.SchedCLS, License: "GPL", Instructions: asm.Instructions{
			entry, callback,
			asm.Mov.Imm(asm.R3, 0), asm.Mov.Imm(asm.R4, 0), asm.FnLoop.Call(),
			asm.Mov.Imm(asm.R0, 0), asm.Return(),
			callbackEntry, asm.Return(),
		}},
		// The L3 path reads IPv4 fields directly from skb->data. Kernels with
		// strict packet alignment must reject this before package replacement.
		{Name: "nl_packet", Type: ebpf.SchedCLS, License: "GPL", Instructions: asm.Instructions{
			asm.LoadMem(asm.R2, asm.R1, 76, asm.Word),
			asm.LoadMem(asm.R3, asm.R1, 80, asm.Word),
			asm.Mov.Reg(asm.R4, asm.R2), asm.Add.Imm(asm.R4, 20),
			asm.JGT.Reg(asm.R4, asm.R3, "done"),
			asm.LoadMem(asm.R0, asm.R2, 12, asm.Word),
			asm.Mov.Imm(asm.R0, 0).WithSymbol("done"), asm.Return(),
		}},
		// The pinned dae core still loads these programs even though its old
		// TCP relay offload is disabled. Probe their actual kernel support;
		// CONFIG_BPF_STREAM_PARSER controls a different (SK_SKB parser) hook.
		{Name: "nl_sockops", Type: ebpf.SockOps, AttachType: ebpf.AttachCGroupSockOps, License: "GPL",
			Instructions: asm.Instructions{asm.Mov.Imm(asm.R0, 0), asm.Return()}},
		{Name: "nl_skmsg", Type: ebpf.SkMsg, AttachType: ebpf.AttachSkMsgVerdict, License: "GPL",
			Instructions: asm.Instructions{asm.Mov.Imm(asm.R0, 1), asm.Return()}},
		{Name: "nl_sockaddr", Type: ebpf.CGroupSockAddr, AttachType: ebpf.AttachCGroupInet4Connect, License: "GPL",
			Instructions: asm.Instructions{asm.Mov.Imm(asm.R0, 1), asm.Return()}},
	}
}

func kernelProbeMapSpec() *ebpf.MapSpec {
	return &ebpf.MapSpec{Name: "nl_sockhash", Type: ebpf.SockHash, KeySize: 40, ValueSize: 8, MaxEntries: 1}
}

func validateKernel(load func(*ebpf.ProgramSpec) error, createMap func(*ebpf.MapSpec) error) error {
	for _, spec := range kernelProbeSpecs() {
		if err := load(spec); err != nil {
			return fmt.Errorf("kernel cannot load transparent-proxy requirement %s: %w; CPU, kernel version and BTF alone do not prove compatibility; a compatible kernel/JIT is required", spec.Name, err)
		}
	}
	spec := kernelProbeMapSpec()
	if err := createMap(spec); err != nil {
		return fmt.Errorf("kernel cannot load transparent-proxy requirement %s: %w; a compatible kernel with SOCKHASH support is required", spec.Name, err)
	}
	return nil
}

func loadKernelProbe(spec *ebpf.ProgramSpec) error {
	program, err := ebpf.NewProgram(spec)
	if err != nil {
		return err
	}
	return program.Close()
}

func loadKernelMap(spec *ebpf.MapSpec) error {
	mapping, err := ebpf.NewMap(spec)
	if err != nil {
		return err
	}
	return mapping.Close()
}

func main() {
	path := flag.String("file", "", "BTF/vmlinux file selected from the runtime's kernel search paths")
	kernel := flag.Bool("kernel", false, "load and close unattached TC, socket-program and SOCKHASH probes")
	flag.Parse()
	if (*path == "" && !*kernel) || flag.NArg() != 0 {
		fmt.Fprintln(os.Stderr, "usage: btf-probe --file /path/to/vmlinux [--kernel], or btf-probe --kernel")
		os.Exit(2)
	}
	if *path != "" {
		if err := validateBTF(*path); err != nil {
			fmt.Fprintln(os.Stderr, err)
			os.Exit(1)
		}
		fmt.Println("BTF parsed; required network types found.")
	}
	if *kernel {
		if err := rlimit.RemoveMemlock(); err != nil {
			fmt.Fprintln(os.Stderr, err)
			os.Exit(1)
		}
		if err := validateKernel(loadKernelProbe, loadKernelMap); err != nil {
			fmt.Fprintln(os.Stderr, err)
			os.Exit(1)
		}
		fmt.Println("Kernel TC callback, packet-access, socket-program and SOCKHASH probes loaded and closed; complete datapath validation is still required.")
	}
}
