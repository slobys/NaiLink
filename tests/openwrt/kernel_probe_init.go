// SPDX-License-Identifier: MIT
// Test-only PID 1 for an isolated QEMU kernel; never run on the host.
package main

import (
	"compress/gzip"
	"fmt"
	"io"
	"os"
	"os/exec"
	"strings"
	"time"

	"golang.org/x/sys/unix"
)

func main() {
	if os.Getpid() != 1 {
		fmt.Fprintln(os.Stderr, "kernel probe init must only run as PID 1 inside the test VM")
		os.Exit(2)
	}
	for path, kind := range map[string]string{"/proc": "proc", "/sys": "sysfs"} {
		os.MkdirAll(path, 0755)
		if err := unix.Mount(kind, path, kind, 0, ""); err != nil {
			panic(err)
		}
	}
	if release, err := os.ReadFile("/proc/sys/kernel/osrelease"); err == nil {
		fmt.Printf("KernelRelease=%s\n", strings.TrimSpace(string(release)))
	}
	if file, err := os.Open("/proc/config.gz"); err == nil {
		if reader, err := gzip.NewReader(file); err == nil {
			if data, err := io.ReadAll(reader); err == nil {
				for _, line := range strings.Split(string(data), "\n") {
					if strings.HasPrefix(line, "CONFIG_BPF_STREAM_PARSER=") || line == "# CONFIG_BPF_STREAM_PARSER is not set" || strings.HasPrefix(line, "CONFIG_NET_SOCK_MSG=") {
						fmt.Printf("KernelConfig=%s\n", line)
					}
				}
			}
			reader.Close()
		}
		file.Close()
	} else {
		fmt.Println("KernelConfig=unavailable")
	}
	// This sysctl change is confined to the disposable VM. The product probe
	// never changes JIT configuration or attaches programs to a network device.
	os.WriteFile("/proc/sys/net/core/bpf_jit_enable", []byte("1"), 0644)
	command := exec.Command("/probe", "--kernel")
	command.Stdout, command.Stderr, command.Stdin = os.Stdout, os.Stderr, os.Stdin
	code := 0
	if err := command.Run(); err != nil {
		if status, ok := err.(*exec.ExitError); ok {
			code = status.ExitCode()
		} else {
			fmt.Println(err)
			code = 2
		}
	}
	fmt.Printf("NaiLinkKernelProbeResult=%d\n", code)
	for {
		time.Sleep(time.Hour)
	}
}
