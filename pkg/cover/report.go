// Copyright 2018 syzkaller project authors. All rights reserved.
// Use of this source code is governed by Apache 2 LICENSE that can be found in the LICENSE file.

package cover

import (
	"fmt"
	"sort"

	"github.com/google/syzkaller/pkg/cover/backend"
	"github.com/google/syzkaller/pkg/host"
	"github.com/google/syzkaller/pkg/mgrconfig"
	"github.com/google/syzkaller/sys/targets"
)

type ReportGenerator struct {
	target          *targets.Target
	srcDir          string
	buildDir        string
	subsystem       []mgrconfig.Subsystem
	rawCoverEnabled bool
	*backend.Impl
}

type Prog struct {
	Sig  string
	Data string
	PCs  []uint64
}

var RestorePC = backend.RestorePC

func MakeReportGenerator(cfg *mgrconfig.Config, subsystem []mgrconfig.Subsystem,
	modules []host.KernelModule, rawCover bool) (*ReportGenerator, error) {
	impl, err := backend.Make(cfg.SysTarget, cfg.Type, cfg.KernelObj,
		cfg.KernelSrc, cfg.KernelBuildSrc, cfg.AndroidSplitBuild, cfg.ModuleObj, modules)
	if err != nil {
		return nil, err
	}
	subsystem = append(subsystem, mgrconfig.Subsystem{
		Name:  "all",
		Paths: []string{""},
	})
	rg := &ReportGenerator{
		target:          cfg.SysTarget,
		srcDir:          cfg.KernelSrc,
		buildDir:        cfg.KernelBuildSrc,
		subsystem:       subsystem,
		rawCoverEnabled: rawCover,
		Impl:            impl,
	}
	return rg, nil
}

type file struct {
	module     string
	filename   string
	lines      map[int]line
	functions  []*function
	covered    []backend.Range
	uncovered  []backend.Range
	totalPCs   int
	coveredPCs int
}

type function struct {
	name    string
	pcs     int
	covered int
}

type line struct {
	progCount map[int]bool // program indices that cover this line
	progIndex int          // example program index that covers this line
}

func coverageCallbackMismatch(debug bool, numPCs int, unmatchedProgPCs map[uint64]bool) error {
	debugStr := ""
	if debug {
		debugStr += "\n\nUnmatched PCs:\n"
		for pc := range unmatchedProgPCs {
			debugStr += fmt.Sprintf("%x\n", pc)
		}
	}
	// nolint: lll
	return fmt.Errorf("%d out of %d PCs returned by kcov do not have matching coverage callbacks. Check the discoverModules() code.%s",
		len(unmatchedProgPCs), numPCs, debugStr)
}

type fileMap map[string]*file

func (rg *ReportGenerator) prepareFileMap(progs []Prog, debug bool) (fileMap, error) {
	if err := rg.lazySymbolize(progs); err != nil {
		return nil, err
	}
	files := make(fileMap)
	for _, unit := range rg.Units {
		files[unit.Name] = &file{
			module:   unit.Module.Name,
			filename: unit.Path,
			lines:    make(map[int]line),
			totalPCs: len(unit.PCs),
		}
	}
	progPCs := make(map[uint64]map[int]bool)
	unmatchedProgPCs := make(map[uint64]bool)
	verifyCoverPoints := (len(rg.CoverPoints) > 0)
	for i, prog := range progs {
		for _, pc := range prog.PCs {
			if progPCs[pc] == nil {
				progPCs[pc] = make(map[int]bool)
			}
			progPCs[pc][i] = true
			if verifyCoverPoints && !rg.CoverPoints[pc] {
				unmatchedProgPCs[pc] = true
			}
		}
	}
	matchedPC := false
	for _, frame := range rg.Frames {
		f := getFile(files, frame.Name, frame.Path, frame.Module.Name)
		ln := f.lines[frame.StartLine]
		coveredBy := progPCs[frame.PC]
		if len(coveredBy) == 0 {
			f.uncovered = append(f.uncovered, frame.Range)
			continue
		}
		// Covered frame.
		f.covered = append(f.covered, frame.Range)
		matchedPC = true
		if ln.progCount == nil {
			ln.progCount = make(map[int]bool)
			ln.progIndex = -1
		}
		for progIndex := range coveredBy {
			ln.progCount[progIndex] = true
			if ln.progIndex == -1 || len(progs[progIndex].Data) < len(progs[ln.progIndex].Data) {
				ln.progIndex = progIndex
			}
		}
		f.lines[frame.StartLine] = ln
	}
	if !matchedPC {
		return nil, fmt.Errorf("coverage doesn't match any coverage callbacks")
	}
	// If the backend provided coverage callback locations for the binaries, use them to
	// verify data returned by kcov.
	if verifyCoverPoints && (len(unmatchedProgPCs) > 0) {
		return nil, coverageCallbackMismatch(debug, len(progPCs), unmatchedProgPCs)
	}
	for _, unit := range rg.Units {
		f := files[unit.Name]
		for _, pc := range unit.PCs {
			if progPCs[pc] != nil {
				f.coveredPCs++
			}
		}
	}
	for _, s := range rg.Symbols {
		fun := &function{
			name: s.Name,
			pcs:  len(s.PCs),
		}
		for _, pc := range s.PCs {
			if progPCs[pc] != nil {
				fun.covered++
			}
		}
		f := files[s.Unit.Name]
		f.functions = append(f.functions, fun)
	}
	for _, f := range files {
		sort.Slice(f.functions, func(i, j int) bool {
			return f.functions[i].name < f.functions[j].name
		})
	}
	return files, nil
}

func (rg *ReportGenerator) lazySymbolize(progs []Prog) error {
	if len(rg.Symbols) == 0 {
		return nil
	}
	symbolize := make(map[*backend.Symbol]bool)
	uniquePCs := make(map[uint64]bool)
	pcs := make(map[*backend.Module][]uint64)
	for _, prog := range progs {
		for _, pc := range prog.PCs {
			if uniquePCs[pc] {
				continue
			}
			uniquePCs[pc] = true
			sym := rg.findSymbol(pc)
			if sym == nil || (sym.Symbolized || symbolize[sym]) {
				continue
			}
			symbolize[sym] = true
			pcs[sym.Module] = append(pcs[sym.Module], sym.PCs...)
		}
	}
	if len(uniquePCs) == 0 {
		return fmt.Errorf("no coverage collected so far")
	}
	frames, err := rg.Symbolize(pcs)
	if err != nil {
		return err
	}
	rg.Frames = append(rg.Frames, frames...)
	for sym := range symbolize {
		sym.Symbolized = true
	}
	return nil
}

func getFile(files map[string]*file, name, path, module string) *file {
	f := files[name]
	if f == nil {
		f = &file{
			module:   module,
			filename: path,
			lines:    make(map[int]line),
			// Special mark for header files, if a file does not have coverage at all it is not shown.
			totalPCs:   1,
			coveredPCs: 1,
		}
		files[name] = f
	}
	return f
}

func (rg *ReportGenerator) findSymbol(pc uint64) *backend.Symbol {
	idx := sort.Search(len(rg.Symbols), func(i int) bool {
		return pc < rg.Symbols[i].End
	})
	if idx == len(rg.Symbols) {
		return nil
	}
	s := rg.Symbols[idx]
	if pc < s.Start || pc > s.End {
		return nil
	}
	return s
}
