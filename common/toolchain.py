"""Where the LLVM tools are.

TC32_LLVM   a directory with clang, ld.lld, llvm-objcopy, llvm-readelf, llvm-nm
            and llvm-objdump. Default: /opt/homebrew/opt/llvm/bin when it
            exists, otherwise whatever PATH finds.
TC32_LLD    a directory with ld.lld when it is not in TC32_LLVM (Homebrew
            keeps lld in its own keg, /opt/homebrew/opt/lld/bin).

The tools here only need a mainstream clang that targets thumbv4t-none-eabi
(any LLVM 16 or later; the checks were run with Homebrew LLVM 23.1.2 and
lld 23.1.2).

SPDX-License-Identifier: Apache-2.0
"""
import os
import shutil

_DEFAULT_LLVM = "/opt/homebrew/opt/llvm/bin"
_DEFAULT_LLD = "/opt/homebrew/opt/lld/bin"


def llvm_bin():
    d = os.environ.get("TC32_LLVM")
    if d:
        return d
    if os.path.isdir(_DEFAULT_LLVM):
        return _DEFAULT_LLVM
    found = shutil.which("clang")
    return os.path.dirname(found) if found else ""


def tool(name):
    """The path of an LLVM tool, or its bare name if nothing better is found."""
    if name in ("ld.lld", "lld"):
        for d in (os.environ.get("TC32_LLD"), llvm_bin(), _DEFAULT_LLD):
            if d and os.path.exists(os.path.join(d, name)):
                return os.path.join(d, name)
    p = os.path.join(llvm_bin(), name) if llvm_bin() else ""
    if p and os.path.exists(p):
        return p
    return shutil.which(name) or name
