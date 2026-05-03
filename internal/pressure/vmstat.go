package pressure

import (
	"fmt"
	"os/exec"
	"regexp"
	"strconv"
)

// VMStatSampler invokes /usr/bin/vm_stat each Sample() and parses the output.
type VMStatSampler struct {
	bin string
}

// NewVMStatSampler returns a sampler using the standard system vm_stat.
func NewVMStatSampler() *VMStatSampler { return &VMStatSampler{bin: "/usr/bin/vm_stat"} }

var (
	rePageSize = regexp.MustCompile(`page size of (\d+) bytes`)
	reFree     = regexp.MustCompile(`Pages free:\s+(\d+)\.?`)
	reActive   = regexp.MustCompile(`Pages active:\s+(\d+)\.?`)
	reInactive = regexp.MustCompile(`Pages inactive:\s+(\d+)\.?`)
	reWired    = regexp.MustCompile(`Pages wired down:\s+(\d+)\.?`)
)

// Sample executes vm_stat once and returns the parsed memory pressure ratio.
func (v *VMStatSampler) Sample() (Reading, error) {
	out, err := exec.Command(v.bin).Output()
	if err != nil {
		return Reading{}, fmt.Errorf("vm_stat exec: %w", err)
	}
	return parseVMStat(string(out))
}

func parseVMStat(s string) (Reading, error) {
	grab := func(re *regexp.Regexp) (uint64, error) {
		m := re.FindStringSubmatch(s)
		if m == nil {
			return 0, fmt.Errorf("regex %q: no match", re.String())
		}
		return strconv.ParseUint(m[1], 10, 64)
	}
	pageSize, err := grab(rePageSize)
	if err != nil {
		return Reading{}, err
	}
	free, err := grab(reFree)
	if err != nil {
		return Reading{}, err
	}
	active, err := grab(reActive)
	if err != nil {
		return Reading{}, err
	}
	inactive, err := grab(reInactive)
	if err != nil {
		return Reading{}, err
	}
	wired, err := grab(reWired)
	if err != nil {
		return Reading{}, err
	}
	_ = pageSize // ratio doesn't depend on page size; we only need the proportions

	used := active + wired
	total := used + free + inactive
	if total == 0 {
		return Reading{}, fmt.Errorf("vm_stat: total pages = 0")
	}
	return Reading{MemRatio: float64(used) / float64(total)}, nil
}
