package trend

import "strings"

// sparkLevels are the eight unicode block elements, lowest to highest.
var sparkLevels = []rune("▁▂▃▄▅▆▇█")

// Sparkline renders values as a min-max-normalized unicode sparkline. An
// empty series renders as "". A flat series (all values equal) renders at
// the mid level — flat is honest, not empty.
func Sparkline(values []int64) string {
	if len(values) == 0 {
		return ""
	}
	minV, maxV := values[0], values[0]
	for _, v := range values[1:] {
		if v < minV {
			minV = v
		}
		if v > maxV {
			maxV = v
		}
	}
	var b strings.Builder
	if maxV == minV {
		for range values {
			b.WriteRune(sparkLevels[3])
		}
		return b.String()
	}
	span := float64(maxV - minV)
	top := len(sparkLevels) - 1
	for _, v := range values {
		idx := int(float64(v-minV) / span * float64(top))
		if idx < 0 {
			idx = 0
		}
		if idx > top {
			idx = top
		}
		b.WriteRune(sparkLevels[idx])
	}
	return b.String()
}
