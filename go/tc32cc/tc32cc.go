// Package tc32cc is tc32-cc: one command from C and assembly to a checked
// TC32 image, on a mainstream clang and ld.lld.
//
// It compiles and assembles with clang for ARMv4T Thumb-1 with the fixed code
// generation flags (CodegenFlags), links with ld.lld, re-encodes the linked
// ELF as TC32 (thumb2tc32), then checks every byte of the image against the
// ELF (imagecheck) and every instruction form against Telink's own use
// (formscheck). The runtime helpers clang calls and the evidence table are
// built into the program, so it needs nothing beyond an LLVM install: no
// Python and no checkout of this repository.
//
// SPDX-License-Identifier: Apache-2.0
package tc32cc

import (
	"bytes"
	_ "embed"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime/debug"
	"strings"

	"github.com/goyamamoto/tc32-devtools/go/formscheck"
	"github.com/goyamamoto/tc32-devtools/go/imagecheck"
	"github.com/goyamamoto/tc32-devtools/go/internal/llvmtool"
	"github.com/goyamamoto/tc32-devtools/go/tc32asm"
	"github.com/goyamamoto/tc32-devtools/go/thumb2tc32"
)

// Copies of ../../compiler/runtime and ../../compiler/vendor_forms.txt
// (checks/run_checks.sh compares them).
var (
	//go:embed embed/compiler_builtins.c
	builtinsC []byte
	//go:embed embed/aeabi_thumb.S
	aeabiS []byte
	//go:embed embed/vendor_forms.txt
	vendorForms []byte
)

// CodegenFlags are the clang flags every object is built with (README,
// "Flags"): ARMv4T Thumb-1, soft float, no jump tables (they read PC in a
// high-register add, which no Telink code does), no load/store optimizer
// (it emits ldm with the base in the list and no writeback, which Telink's
// compiler never emits).
var CodegenFlags = []string{"--target=thumbv4t-none-eabi", "-mcpu=arm7tdmi", "-mthumb", "-mfloat-abi=soft",
	"-fno-jump-tables", "-mllvm", "-arm-load-store-opt=false"}

// runtimeFlags are added for the runtime helpers, built the same way in every
// image.
var runtimeFlags = []string{"-ffreestanding", "-fno-builtin", "-Oz"}

// Version names this build of tc32-cc; set it with
// -ldflags "-X github.com/goyamamoto/tc32-devtools/go/tc32cc.Version=<revision>".
var Version = "unknown"

// VerifiedLLVM is the LLVM release the repository's evidence was made with.
const VerifiedLLVM = "23.1.2"

const usage = `usage: tc32-cc [options] <inputs>...

One command from C and assembly to a checked TC32 image:
  clang (ARMv4T Thumb-1) -> ld.lld -> thumb2tc32 -> image check -> forms check

Inputs: .c (C), .S (Thumb assembly with the preprocessor), .s (Thumb
assembly), .o and .a (to the linker). -x tc32-asm before files: assembly in
Telink's syntax (tloadr, tjl, tmcsr, ...), translated and assembled with the
preprocessor. -x c, -x assembler-with-cpp, -x assembler; -x none goes back
to the extension.

  -c, -S, -E        compile, assemble or preprocess only (one input: -o names
                    the output; otherwise <name>.o or <name>.s here, -E to
                    the standard output)
  -o <image>        the TC32 image (default a.bin); the ELF is written next
                    to it with the extension .elf
  -T <script>       the linker script (needed to link)
  -L <dir>, -l <name>, -Wl,<a>,<b>, -Xlinker <a>
                    to ld.lld
  --tlsr8278-divider
                    runtime helpers for the TLSR8278's hardware divider
  -nostdlib, --no-runtime
                    no runtime helpers
  --no-forms-check  skip the forms check (the image check always runs)
  -v                print each command
  --print-flags     print the code generation flags and exit
  --version         print the tools and their versions and exit
Other options (-O, -g, -D, -I, -std, -W, -f..., -M...) go to clang.

The code generation flags are fixed; options that would change them are
refused. A failed check removes the image and exits with status 1.
Environment: TC32_LLVM, TC32_LLD (directories with the LLVM tools).
`

// clangArg are the clang options whose value is the next argument.
var clangArg = map[string]bool{"-I": true, "-D": true, "-U": true, "-include": true, "-imacros": true,
	"-isystem": true, "-iquote": true, "-idirafter": true, "-isysroot": true, "-MF": true, "-MT": true,
	"-MQ": true, "-Xclang": true, "-Xassembler": true, "-mllvm": true}

type input struct {
	path string
	lang string // "" (by the extension), c, assembler-with-cpp, assembler, tc32-asm
}

type config struct {
	stage       string // "", -c, -S, -E
	out         string
	script      string
	clangOpts   []string
	linkOpts    []string // -L and -Wl options
	link        []string // objects, archives and -l in their order (sources put in place by Main)
	inputs      []input
	inputAt     []int // the position in link of each input
	noRuntime   bool
	divider     bool
	noForms     bool
	verbose     bool
	printFlags  bool
	showVersion bool
	showHelp    bool
}

type driver struct {
	w, ew io.Writer
	cfg   config
	n     int // translated files so far
}

type failure struct {
	code int
	msg  string
}

func (d *driver) fail(code int, format string, a ...any) {
	panic(failure{code, fmt.Sprintf(format, a...)})
}

// Main runs tc32-cc with argv (the arguments after the program name), writing
// its report to w and its errors to ew; it returns the exit status.
func Main(argv []string, w, ew io.Writer) (code int) {
	d := &driver{w: w, ew: ew}
	defer func() {
		if e := recover(); e != nil {
			f, ok := e.(failure)
			if !ok {
				panic(e)
			}
			fmt.Fprintln(ew, "tc32-cc: "+f.msg)
			code = f.code
		}
	}()
	d.parse(argv)
	c := &d.cfg
	switch {
	case c.printFlags:
		fmt.Fprintln(w, strings.Join(CodegenFlags, " "))
		return 0
	case c.showVersion:
		d.version()
		return 0
	case c.showHelp:
		fmt.Fprint(w, usage)
		return 0
	case len(c.inputs) == 0 && len(c.link) == 0:
		fmt.Fprint(ew, usage)
		return 2
	case c.stage != "":
		d.compileOnly()
		return 0
	}
	return d.linkImage()
}

// refusal says why an option cannot be used, or "" when it can.
func refusal(opt, next string) string {
	fixed := "the code generation flags are fixed (tc32-cc --print-flags)"
	switch {
	case opt == "--target=thumbv4t-none-eabi", opt == "-mcpu=arm7tdmi", opt == "-mthumb",
		opt == "-mfloat-abi=soft", opt == "-fno-jump-tables":
		return ""
	case strings.HasPrefix(opt, "--target="), opt == "-target", strings.HasPrefix(opt, "-march="),
		strings.HasPrefix(opt, "-mcpu="), strings.HasPrefix(opt, "-mtune="), opt == "-marm", opt == "-mno-thumb",
		opt == "-mbig-endian", strings.HasPrefix(opt, "-mfloat-abi="),
		strings.HasPrefix(opt, "-mfpu=") && opt != "-mfpu=none":
		return opt + ": " + fixed
	case opt == "-fjump-tables":
		return opt + ": jump tables read PC in a high-register add, which no Telink code does"
	case opt == "-mllvm" && strings.HasPrefix(next, "-arm-load-store-opt") && next != "-arm-load-store-opt=false":
		return "-mllvm " + next + ": the load/store optimizer emits ldm forms Telink's compiler never emits"
	case opt == "-flto" || strings.HasPrefix(opt, "-flto="):
		return opt + ": link-time code generation would run without the fixed backend options"
	}
	return ""
}

func (d *driver) parse(argv []string) {
	c := &d.cfg
	lang := ""
	next := func(i *int, opt string) string {
		if *i+1 >= len(argv) {
			d.fail(2, "%s needs a value", opt)
		}
		*i++
		return argv[*i]
	}
	for i := 0; i < len(argv); i++ {
		a := argv[i]
		nx := ""
		if i+1 < len(argv) {
			nx = argv[i+1]
		}
		if why := refusal(a, nx); why != "" {
			d.fail(2, "%s", why)
		}
		switch {
		case a == "-c" || a == "-S" || a == "-E":
			if c.stage != "" && c.stage != a {
				d.fail(2, "%s and %s together", c.stage, a)
			}
			c.stage = a
		case a == "-o":
			c.out = next(&i, a)
		case a == "-x":
			lang = next(&i, a)
			switch lang {
			case "none":
				lang = ""
			case "c", "assembler-with-cpp", "assembler", "tc32-asm":
			default:
				d.fail(2, "-x %s: not a language tc32-cc builds (c, assembler-with-cpp, assembler, tc32-asm, none)", lang)
			}
		case a == "-T":
			c.script = next(&i, a)
		case strings.HasPrefix(a, "-T") && len(a) > 2:
			c.script = a[2:]
		case a == "-L":
			c.linkOpts = append(c.linkOpts, "-L", next(&i, a))
		case strings.HasPrefix(a, "-L"):
			c.linkOpts = append(c.linkOpts, a)
		case a == "-l":
			c.link = append(c.link, "-l"+next(&i, a))
		case strings.HasPrefix(a, "-l"):
			c.link = append(c.link, a)
		case strings.HasPrefix(a, "-Wl,"):
			c.linkOpts = append(c.linkOpts, strings.Split(a[4:], ",")...)
		case a == "-Xlinker":
			c.linkOpts = append(c.linkOpts, next(&i, a))
		case a == "-nostdlib" || a == "--no-runtime":
			c.noRuntime = true
		case a == "--tlsr8278-divider":
			c.divider = true
		case a == "--no-forms-check":
			c.noForms = true
		case a == "-v":
			c.verbose = true
		case a == "--print-flags":
			c.printFlags = true
		case a == "--version":
			c.showVersion = true
		case a == "--help" || a == "-h":
			c.showHelp = true
		case clangArg[a]:
			c.clangOpts = append(c.clangOpts, a, next(&i, a))
		case strings.HasPrefix(a, "-") && a != "-":
			c.clangOpts = append(c.clangOpts, a)
		default:
			ext := strings.ToLower(filepath.Ext(a))
			if lang == "" && (ext == ".o" || ext == ".a" || ext == ".obj") {
				c.link = append(c.link, a)
				continue
			}
			c.inputAt = append(c.inputAt, len(c.link))
			c.link = append(c.link, "") // the object, once compiled
			c.inputs = append(c.inputs, input{a, lang})
		}
	}
}

// run runs a tool; its output goes to the standard error, as a compiler's.
func (d *driver) run(name string, args ...string) { d.runTo(d.ew, name, args...) }

// runTo runs a tool with its standard output to out.
func (d *driver) runTo(out io.Writer, name string, args ...string) {
	if d.cfg.verbose {
		fmt.Fprintln(d.ew, name+" "+strings.Join(args, " "))
	}
	cmd := exec.Command(name, args...)
	cmd.Stdout, cmd.Stderr = out, d.ew
	if err := cmd.Run(); err != nil {
		if _, ok := err.(*exec.ExitError); ok {
			d.fail(1, "%s failed", filepath.Base(name))
		}
		d.fail(1, "%s: %v (install LLVM's clang and lld, or set TC32_LLVM / TC32_LLD)", name, err)
	}
}

// compile builds one input into out with clang (stage -c, -S or -E).
func (d *driver) compile(in input, stage, out, tmp string) {
	args := append([]string{}, CodegenFlags...)
	args = append(args, d.cfg.clangOpts...)
	src := in.path
	if in.lang == "tc32-asm" {
		if stage == "-S" {
			d.fail(2, "%s: -S has nothing to do for assembly", in.path)
		}
		text, err := os.ReadFile(in.path)
		if err != nil {
			d.fail(1, "%v", err)
		}
		lines, err := tc32asm.Translate(tc32asm.SplitLines(string(text)))
		if err != nil {
			d.fail(1, "%s: %v", in.path, err)
		}
		d.n++
		src = filepath.Join(tmp, fmt.Sprintf("tc32asm%d-%s", d.n, filepath.Base(in.path)))
		if err := os.WriteFile(src, []byte(tc32asm.Header(filepath.Base(in.path))+strings.Join(lines, "")), 0o644); err != nil {
			d.fail(1, "%v", err)
		}
		// #include "..." still finds what sits next to the original.
		args = append(args, "-iquote", filepath.Dir(in.path), "-x", "assembler-with-cpp")
	} else if in.lang != "" {
		args = append(args, "-x", in.lang)
	}
	args = append(args, stage, src)
	if out != "" {
		args = append(args, "-o", out)
	}
	d.runTo(d.w, llvmtool.Tool("clang"), args...)
}

func (d *driver) tmpDir() string {
	tmp, err := os.MkdirTemp("", "tc32-cc")
	if err != nil {
		d.fail(1, "%v", err)
	}
	return tmp
}

func (d *driver) compileOnly() {
	c := &d.cfg
	if len(c.link) != len(c.inputs) {
		d.fail(2, "%s: objects and libraries are only for linking", c.stage)
	}
	if c.out != "" && len(c.inputs) > 1 {
		d.fail(2, "-o with %s names one output, and there are %d inputs", c.stage, len(c.inputs))
	}
	tmp := d.tmpDir()
	defer os.RemoveAll(tmp)
	ext := map[string]string{"-c": ".o", "-S": ".s"}[c.stage]
	for _, in := range c.inputs {
		out := c.out
		if out == "" && ext != "" {
			out = strings.TrimSuffix(filepath.Base(in.path), filepath.Ext(in.path)) + ext
		}
		d.compile(in, c.stage, out, tmp)
	}
}

// toolVersion is the version a tool's --version reports first, or "".
func toolVersion(path string) string {
	out, err := exec.Command(path, "--version").Output()
	if err != nil {
		return ""
	}
	m := regexp.MustCompile(`(?:version|LLD) (\d+\.\d+\.\d+)`).FindSubmatch(out)
	if m == nil {
		return ""
	}
	return string(m[1])
}

func (d *driver) version() {
	rev := Version
	if bi, ok := debug.ReadBuildInfo(); ok {
		for _, s := range bi.Settings {
			if s.Key == "vcs.revision" && len(s.Value) >= 12 {
				rev = s.Value[:12]
			}
		}
	}
	fmt.Fprintf(d.w, "tc32-cc (TC32-devtools %s); the checks were made with LLVM %s\n", rev, VerifiedLLVM)
	for _, t := range []string{"clang", "ld.lld", "llvm-objcopy", "llvm-readelf"} {
		p := llvmtool.Tool(t)
		v := toolVersion(p)
		if v == "" {
			v = "not found"
		}
		fmt.Fprintf(d.w, "%-13s %s (%s)\n", t, v, p)
	}
}

// imagePaths are the image's and the ELF's names for -o.
func imagePaths(out string) (string, string, error) {
	if out == "" {
		out = "a.bin"
	}
	if strings.EqualFold(filepath.Ext(out), ".elf") {
		return "", "", fmt.Errorf("-o %s: -o names the TC32 image; the ELF is written next to it", out)
	}
	return out, strings.TrimSuffix(out, filepath.Ext(out)) + ".elf", nil
}

func (d *driver) linkImage() int {
	c := &d.cfg
	if c.script == "" {
		d.fail(2, "linking needs a linker script (-T <script>)")
	}
	image, elfPath, err := imagePaths(c.out)
	if err != nil {
		d.fail(2, "%v", err)
	}
	clang, lld := llvmtool.Tool("clang"), llvmtool.Tool("ld.lld")
	if v := toolVersion(clang); v != VerifiedLLVM {
		if v == "" {
			v = "of unknown version"
		}
		fmt.Fprintf(d.ew, "tc32-cc: warning: clang %s (%s): the checks of this compiler path were made with LLVM %s\n",
			v, clang, VerifiedLLVM)
	}
	tmp := d.tmpDir()
	defer os.RemoveAll(tmp)

	// Sources, in their places among the objects.
	for k, in := range c.inputs {
		obj := filepath.Join(tmp, fmt.Sprintf("%d-%s.o", k, strings.TrimSuffix(filepath.Base(in.path), filepath.Ext(in.path))))
		d.compile(in, "-c", obj, tmp)
		c.link[c.inputAt[k]] = obj
	}
	objs := append([]string{}, c.link...)
	if !c.noRuntime {
		bc, as := filepath.Join(tmp, "rt-compiler_builtins.c"), filepath.Join(tmp, "rt-aeabi_thumb.S")
		if err := os.WriteFile(bc, builtinsC, 0o644); err != nil {
			d.fail(1, "%v", err)
		}
		if err := os.WriteFile(as, aeabiS, 0o644); err != nil {
			d.fail(1, "%v", err)
		}
		args := append(append([]string{}, CodegenFlags...), runtimeFlags...)
		if c.divider {
			args = append(args, "-DTC32_TLSR8278_DIVIDER=1")
		}
		d.run(clang, append(args, "-c", bc, "-o", bc+".o")...)
		d.run(clang, append(append([]string{}, CodegenFlags...), "-c", as, "-o", as+".o")...)
		objs = append(objs, bc+".o", as+".o")
	}
	args := append([]string{"-T", c.script}, c.linkOpts...)
	args = append(append(args, objs...), "-o", elfPath)
	d.run(lld, args...)

	// TC32 machine code, then the checks; a failed check leaves no image.
	data, err := os.ReadFile(elfPath)
	if err != nil {
		d.fail(1, "%v", err)
	}
	img, n, err := thumb2tc32.Convert(data)
	if err != nil {
		d.fail(1, "thumb2tc32: %s: %v", elfPath, err)
	}
	if err := os.WriteFile(image, img, 0o644); err != nil {
		d.fail(1, "%v", err)
	}
	fmt.Fprintf(d.w, "%s: %d bytes, %d instructions re-encoded (%d TC32-only, %d lsls #0 as adds #0, %d udf as b ., "+
		"%d zero fill kept), %d bytes of data in code sections left as they are\n",
		image, len(img), n.Code, n.TC32Only, n.Movs, n.Udf, n.Zero, n.Data)
	failed := func(what string) int {
		os.Remove(image)
		fmt.Fprintf(d.ew, "tc32-cc: %s failed: %s removed (the ELF %s stays)\n", what, image, elfPath)
		return 1
	}
	if imagecheck.Main([]string{elfPath, image, "--objcopy", llvmtool.Tool("llvm-objcopy"),
		"--readelf", llvmtool.Tool("llvm-readelf")}, d.w, d.ew) != 0 {
		return failed("image check")
	}
	if c.noForms {
		fmt.Fprintln(d.w, "forms check skipped (--no-forms-check)")
		return 0
	}
	ev, err := formscheck.ParseEvidence(bytes.NewReader(vendorForms))
	if err != nil {
		d.fail(1, "the evidence table: %v", err)
	}
	var report bytes.Buffer
	nbad, err := formscheck.Check(&report, data, true, ev)
	if err != nil {
		d.fail(1, "forms check: %v", err)
	}
	if nbad > 0 {
		d.w.Write(report.Bytes())
		return failed("forms check")
	}
	lines := strings.Split(strings.TrimSpace(report.String()), "\n")
	fmt.Fprintln(d.w, lines[len(lines)-1])
	return 0
}
