package leaks

import (
	"context"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/FRIKKern/noo-noo/internal/core"
	"github.com/FRIKKern/noo-noo/internal/modules"
	"github.com/FRIKKern/noo-noo/internal/sizer"
)

// familyKey identifies one sibling family: the shared prefix and the
// separator that preceded the random suffix ("" for Go's MkdirTemp, which
// appends decimal digits straight onto the prefix).
type familyKey struct {
	prefix string
	sep    string
}

// family is one discovered sibling family under root. members are
// absolute, sorted paths; newest is the newest mtime across every member
// (filled by inspectFamily, zero on a handle-parsed family).
type family struct {
	root    string
	key     familyKey
	members []string
}

// handle is the family's report path: <root>/<prefix><sep>* — the shape a
// human would type to name the family, matching the signature's leaf glob
// (root/*) without naming any real entry.
func (f family) handle() string {
	return filepath.Join(f.root, f.key.prefix+f.key.sep+"*")
}

var (
	alnumRun       = regexp.MustCompile(`^[A-Za-z0-9]{6,}$`)
	lowerWord      = regexp.MustCompile(`^[a-z]+$`)
	upperWord      = regexp.MustCompile(`^[A-Z]+$`)
	trailingDigits = regexp.MustCompile(`^(.*[^0-9])([0-9]{6,})$`)
)

// familyKeyOf parses a child name into its family key by stripping one
// trailing random suffix. Two shapes are recognised:
//
//   - <prefix>[-_.]<alnum{6,}>  — mktemp/MkdirTemp("prefix-") style. The
//     suffix must look RANDOM (not a pure lower- or upper-case word):
//     "bd-migration-restore" is a named dir, "bd-migration-restore-7zOyaH"
//     is its sibling family. A random 6-char alnum string is all-lowercase
//     letters with p≈0.5%, so this costs almost no recall.
//   - <prefix><digits{6,}>      — MkdirTemp("prefix") with no separator.
//
// ok=false means the name carries no random suffix and joins no family.
func familyKeyOf(name string) (familyKey, bool) {
	// Digits first: "go-build123456789" is MkdirTemp("go-build"), not a
	// "go" family with suffix "build123456789".
	if mm := trailingDigits.FindStringSubmatch(name); mm != nil {
		prefix := mm[1]
		if n := len(prefix); n > 1 && strings.ContainsAny(prefix[n-1:], "-_.") {
			return familyKey{prefix: prefix[:n-1], sep: prefix[n-1:]}, true
		}
		return familyKey{prefix: prefix, sep: ""}, true
	}
	if i := strings.LastIndexAny(name, "-_."); i > 0 {
		tail := name[i+1:]
		if alnumRun.MatchString(tail) && !lowerWord.MatchString(tail) && !upperWord.MatchString(tail) {
			return familyKey{prefix: name[:i], sep: name[i : i+1]}, true
		}
	}
	return familyKey{}, false
}

// parseFamilyHandle inverts family.handle: root, key, ok.
func parseFamilyHandle(handle string) (string, familyKey, bool) {
	base := filepath.Base(handle)
	if !strings.HasSuffix(base, "*") || len(base) < 2 {
		return "", familyKey{}, false
	}
	stem := strings.TrimSuffix(base, "*")
	key := familyKey{prefix: stem}
	if n := len(stem); n > 0 && strings.ContainsAny(stem[n-1:], "-_.") {
		key = familyKey{prefix: stem[:n-1], sep: stem[n-1:]}
	}
	if key.prefix == "" {
		return "", familyKey{}, false
	}
	return filepath.Dir(handle), key, true
}

// listFamilies reads root's direct children and groups the uid-owned,
// non-symlink dirs and regular files by family key. skip drops entries by
// absolute path (already claimed by an earlier signature).
func listFamilies(root string, rule *FamilyRule, uid int, skip map[string]bool) []family {
	entries, err := os.ReadDir(root)
	if err != nil {
		return nil
	}
	byKey := map[familyKey]*family{}
	for _, e := range entries {
		name := e.Name()
		if excludedName(name, rule) {
			continue
		}
		key, ok := familyKeyOf(name)
		if !ok {
			continue
		}
		p := filepath.Join(root, name)
		if skip[p] {
			continue
		}
		fi, err := os.Lstat(p)
		if err != nil || fi.Mode()&os.ModeSymlink != 0 {
			continue
		}
		if !fi.IsDir() && !fi.Mode().IsRegular() {
			continue
		}
		st, ok := fi.Sys().(*syscall.Stat_t)
		if !ok || int(st.Uid) != uid {
			continue
		}
		f := byKey[key]
		if f == nil {
			f = &family{root: root, key: key}
			byKey[key] = f
		}
		f.members = append(f.members, p)
	}
	out := make([]family, 0, len(byKey))
	for _, f := range byKey {
		sort.Strings(f.members)
		out = append(out, *f)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].handle() < out[j].handle() })
	return out
}

func excludedName(name string, rule *FamilyRule) bool {
	for _, ex := range rule.ExcludePrefixes {
		if strings.HasPrefix(name, ex) {
			return true
		}
	}
	return false
}

// familyForHandle re-discovers one family from its handle at apply time.
func familyForHandle(handle string, rule *FamilyRule, uid int) (family, bool) {
	root, key, ok := parseFamilyHandle(handle)
	if !ok {
		return family{}, false
	}
	for _, f := range listFamilies(root, rule, uid, nil) {
		if f.key == key {
			return f, true
		}
	}
	return family{}, false
}

// treeFacts walks path and returns the newest mtime in the tree plus the
// first special file (socket, FIFO, device) found — a tree carrying one is
// not plain scratch and is never a family candidate. Any walk error makes
// the facts unprovable.
func treeFacts(path string) (newest time.Time, special string, err error) {
	err = filepath.WalkDir(path, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		fi, err := d.Info()
		if err != nil {
			return err
		}
		if mt := fi.ModTime(); mt.After(newest) {
			newest = mt
		}
		if special == "" && fi.Mode()&(os.ModeSocket|os.ModeNamedPipe|os.ModeDevice|os.ModeCharDevice) != 0 {
			special = p
		}
		return nil
	})
	if err != nil {
		return time.Time{}, "", err
	}
	return newest, special, nil
}

// scanFamilies discovers every family under the signature's roots and
// reports one item per family of at least MinMembers. Members (and the
// family handle) are marked in seen so no later glob can double-count them.
func (m *Module) scanFamilies(ctx context.Context, sig Signature, seen map[string]bool, rep *modules.Report) error {
	var open *openSet // batched lsof listing, fetched at most once per scan
	for _, rg := range sig.Family.Roots {
		roots, err := filepath.Glob(rg)
		if err != nil {
			continue
		}
		for _, root := range roots {
			for _, fam := range listFamilies(root, sig.Family, m.uid, seen) {
				if err := ctx.Err(); err != nil {
					return err
				}
				if len(fam.members) < sig.Family.MinMembers {
					continue
				}
				item := m.inspectFamily(ctx, sig, fam, &open)
				seen[item.Path] = true
				for _, mem := range fam.members {
					seen[mem] = true
				}
				rep.Items = append(rep.Items, item)
				rep.Total += item.Size
			}
		}
	}
	return nil
}

// openSet is the parsed batched lsof listing shared across one scan.
type openSet struct {
	paths     []string
	failProof string
	ok        bool
}

// openMember returns the first (open path, member) pair, if any member of
// fam has a file held open per the listing.
func (o *openSet) openMember(fam family) (string, string, bool) {
	for _, mem := range fam.members {
		pre := mem + "/"
		for _, p := range o.paths {
			if p == mem || strings.HasPrefix(p, pre) {
				return p, mem, true
			}
		}
	}
	return "", "", false
}

// inspectFamily classifies and truth-sizes one family. The family is the
// unit: one age verdict (newest mtime across all members), one liveness
// verdict (any member open = whole family LIVE), one UniqueAllocated walk
// over all members (clone-aware ACROSS siblings — a harness that cp -c's
// its fixtures would otherwise du-report N copies).
func (m *Module) inspectFamily(ctx context.Context, sig Signature, fam family, open **openSet) modules.Item {
	n := len(fam.members)
	ev := map[string]string{
		"signature":      sig.ID,
		"staleness_rule": sig.Staleness.String(),
		"workaround":     sig.Workaround,
		"family_prefix":  fam.key.prefix,
		"family_root":    fam.root,
		"family_members": strconv.Itoa(n),
		"members_sample": sampleNames(fam.members, 3),
	}
	label := fmt.Sprintf("family %s%s* (%d members)", fam.key.prefix, fam.key.sep, n)

	stale := m.classifyFamily(ctx, sig, fam, label, ev, open)
	if stale {
		ev["staleness"] = "stale"
	} else {
		ev["staleness"] = "live"
	}

	var uniq, blocks int64
	if ts, err := sizer.UniqueAllocated(fam.members...); err == nil {
		uniq, blocks = ts.UniqueAllocated, ts.Blocks
		ev["unique_allocated_bytes"] = strconv.FormatInt(uniq, 10)
		ev["blocks_bytes"] = strconv.FormatInt(blocks, 10)
		if fiction := blocks - uniq; fiction > 0 {
			ev["du_fiction_bytes"] = strconv.FormatInt(fiction, 10)
		}
	} else {
		ev["unique_allocated_error"] = err.Error()
	}
	return modules.Item{Path: fam.handle(), Size: core.Bytes(uniq), Evidence: ev}
}

// classifyFamily runs the family rule: special files → LIVE; newest mtime
// across members younger than MinAge → LIVE (harness may still be minting
// siblings); any member held open per the batched listing → LIVE; else
// stale. Unprovable anything → LIVE.
func (m *Module) classifyFamily(ctx context.Context, sig Signature, fam family, label string, ev map[string]string, open **openSet) bool {
	var newest time.Time
	for _, mem := range fam.members {
		t, special, err := treeFacts(mem)
		if err != nil {
			ev["age"] = label + ": unprovable (" + err.Error() + ") — treating as LIVE (fail-safe)"
			return false
		}
		if special != "" {
			ev["age"] = fmt.Sprintf("%s: member holds a special file %s — not plain scratch, LIVE", label, special)
			return false
		}
		if t.After(newest) {
			newest = t
		}
	}
	age := m.now().Sub(newest)
	ev["newest_mtime"] = newest.UTC().Format(time.RFC3339)
	ev["min_age"] = sig.MinAge.String()
	if age < sig.MinAge {
		ev["age"] = fmt.Sprintf("%s: newest mtime %s old < min age %s — harness may still be running", label, age.Round(time.Second), sig.MinAge)
		return false
	}
	ev["age"] = fmt.Sprintf("%s: newest mtime %s old ≥ min age %s", label, age.Round(time.Second), sig.MinAge)

	if *open == nil {
		paths, failProof, ok := m.openPaths(ctx)
		*open = &openSet{paths: paths, failProof: failProof, ok: ok}
	}
	if !(*open).ok {
		ev["lsof"] = (*open).failProof
		return false
	}
	if p, mem, held := (*open).openMember(fam); held {
		ev["lsof"] = fmt.Sprintf("lsof: %s is held open under member %s — whole family LIVE", p, mem)
		return false
	}
	ev["lsof"] = fmt.Sprintf("lsof: no process holds any file open under any of the %d members", len(fam.members))
	return true
}

// memberRefusal re-proves ONE member's staleness at delete time: still a
// family of MinMembers, plain files only, this member's own newest mtime
// past MinAge, and an inode-accurate `lsof +D` on the member. "" = stale.
func (m *Module) memberRefusal(ctx context.Context, sig Signature, member string) string {
	fi, err := os.Lstat(member)
	if err != nil {
		return "gone: " + err.Error()
	}
	if fi.Mode()&os.ModeSymlink != 0 {
		return "symlink — refusing"
	}
	newest, special, err := treeFacts(member)
	if err != nil {
		return "age unprovable (" + err.Error() + ") — LIVE (fail-safe)"
	}
	if special != "" {
		return "holds special file " + special + " — LIVE"
	}
	if age := m.now().Sub(newest); age < sig.MinAge {
		return fmt.Sprintf("newest mtime %s old < min age %s — LIVE", age.Round(time.Second), sig.MinAge)
	}
	if live, proof := m.probe(ctx, member); live {
		return proof
	}
	return ""
}

// applyFamily deletes the stale members of one family, re-checking each
// member at delete time (TOCTOU guard per member, the chrome-clone way) and
// skipping the rest. Freed bytes are statfs-measured over the whole batch.
func (m *Module) applyFamily(ctx context.Context, sig Signature, a modules.Action) (modules.Result, error) {
	res := modules.Result{Action: a}
	fam, ok := familyForHandle(a.Target, sig.Family, m.uid)
	if !ok {
		res.Err = fmt.Errorf("leaks: %q names no current family under its root — refusing", a.Target)
		return res, res.Err
	}
	if n := len(fam.members); n < sig.Family.MinMembers {
		res.Err = fmt.Errorf("leaks: %q shrank to %d member(s) < %d — no longer a family, refusing", a.Target, n, sig.Family.MinMembers)
		return res, res.Err
	}
	var deleted int
	var refusals []string
	freed, loopErr := sizer.FreedByDelete(fam.root, func() error {
		for _, mem := range fam.members {
			if err := ctx.Err(); err != nil {
				return err
			}
			if why := m.memberRefusal(ctx, sig, mem); why != "" {
				refusals = append(refusals, filepath.Base(mem)+": "+why)
				continue
			}
			if err := m.safety.CanDeleteLeakTarget(mem, sig.Globs); err != nil {
				refusals = append(refusals, filepath.Base(mem)+": "+err.Error())
				continue
			}
			if err := os.RemoveAll(mem); err != nil {
				refusals = append(refusals, filepath.Base(mem)+": "+err.Error())
				continue
			}
			deleted++
		}
		return nil
	})
	res.BytesFreed = core.Bytes(freed)
	if loopErr != nil {
		res.Err = loopErr
		return res, loopErr
	}
	if deleted == 0 {
		res.Err = fmt.Errorf("leaks: no member of %q was deletable at apply time (%s)", a.Target, sampleStrings(refusals, 3))
		return res, res.Err
	}
	return res, nil
}

func sampleNames(paths []string, n int) string {
	names := make([]string, 0, n)
	for i, p := range paths {
		if i >= n {
			break
		}
		names = append(names, filepath.Base(p))
	}
	return strings.Join(names, ",")
}

func sampleStrings(ss []string, n int) string {
	if len(ss) > n {
		return strings.Join(ss[:n], "; ") + fmt.Sprintf("; +%d more", len(ss)-n)
	}
	return strings.Join(ss, "; ")
}
