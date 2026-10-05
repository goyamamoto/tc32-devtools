// Package llvmtool finds the LLVM tools the way ../../common/toolchain.py does:
// TC32_LLVM (a directory), then /opt/homebrew/opt/llvm/bin, then PATH;
// ld.lld also in TC32_LLD and /opt/homebrew/opt/lld/bin.
//
// SPDX-License-Identifier: Apache-2.0
package llvmtool

import (
	"os"
	"os/exec"
	"path/filepath"
)

const (
	defaultLLVM = "/opt/homebrew/opt/llvm/bin"
	defaultLLD  = "/opt/homebrew/opt/lld/bin"
)

func exists(p string) bool {
	st, err := os.Stat(p)
	return err == nil && !st.IsDir()
}

// Tool returns the path of an LLVM tool, or its bare name if nothing better
// is found.
func Tool(name string) string {
	dirs := []string{os.Getenv("TC32_LLVM"), defaultLLVM}
	if name == "ld.lld" || name == "lld" {
		dirs = append([]string{os.Getenv("TC32_LLD")}, append(dirs, defaultLLD)...)
	}
	for _, d := range dirs {
		if d != "" && exists(filepath.Join(d, name)) {
			return filepath.Join(d, name)
		}
	}
	if p, err := exec.LookPath(name); err == nil {
		return p
	}
	return name
}
