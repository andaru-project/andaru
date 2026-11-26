package config

import (
	"cmp"
	"context"
	"fmt"
	"net/netip"
	"slices"
	"strings"
	"unicode"
)

// AliasSearchQuery describes a similarity search over saved address or port aliases.
type AliasSearchQuery struct {
	Type          string // "addresses" or "ports"
	Query         string
	Kind          string // optional sub-kind (host/network/range/group) or protocol (tcp/udp/icmp/icmpv6/group)
	Family        string // optional "ipv4" or "ipv6" (addresses only)
	ExcludeGroups bool
	Exclude       string // optional alias ID to omit (for example, self when editing a group)
	Limit         int    // defaults to 20, clamped to 1..50
}

// TextSegment is one contiguous run of a label or summary, marked true when matched by the search query.
type TextSegment struct {
	Text  string `json:"text"`
	Match bool   `json:"match"`
}

// AliasSearchItem is one ranked alias match returned by SearchAliases.
type AliasSearchItem struct {
	ID                string        `json:"id"`
	Name              string        `json:"name"`
	Description       string        `json:"description,omitempty"`
	Kind              string        `json:"kind,omitempty"`
	Protocol          string        `json:"protocol,omitempty"`
	Summary           string        `json:"summary"`
	Score             int           `json:"score"`
	Highlights        []TextSegment `json:"highlights"`
	SummaryHighlights []TextSegment `json:"summaryHighlights,omitempty"`
}

// AliasSearchView is the API response for GET /api/v1/config/aliases/search.
type AliasSearchView struct {
	State        string            `json:"state"`
	SavedVersion Version           `json:"savedVersion"`
	Items        []AliasSearchItem `json:"items"`
}

// SearchAliases searches saved aliases by similarity and returns ranked matches with highlight segments.
// It copies the alias slices under the store read lock and runs similarity scoring after releasing it.
func (s *Store) SearchAliases(ctx context.Context, query AliasSearchQuery) (AliasSearchView, error) {
	var snapshot aliasSet
	var savedVersion Version
	if err := s.read(ctx, "", nil, func(state savedState, _ VersionInfo) error {
		savedVersion = state.Version
		if query.Type == "addresses" {
			snapshot.Addresses = slices.Clone(state.Config.Aliases.Addresses)
		} else if query.Type == "ports" {
			snapshot.Ports = slices.Clone(state.Config.Aliases.Ports)
		}
		return nil
	}); err != nil {
		return AliasSearchView{}, err
	}
	return searchAliasesInSet(ctx, &snapshot, savedVersion, query)
}

func searchAliasesInDeclaration(d *declaration, savedVersion Version, query AliasSearchQuery) AliasSearchView {
	res, _ := searchAliasesInSet(context.Background(), &d.Aliases, savedVersion, query)
	return res
}

func searchAliasesInSet(ctx context.Context, set *aliasSet, savedVersion Version, query AliasSearchQuery) (AliasSearchView, error) {
	limit := query.Limit
	if limit <= 0 {
		limit = 20
	} else if limit > 50 {
		limit = 50
	}
	trimmed := strings.TrimSpace(query.Query)
	qRunes := toLowerRunes(trimmed)

	items := make([]AliasSearchItem, 0)
	switch query.Type {
	case "addresses":
		byID := make(map[string]address, len(set.Addresses))
		for _, a := range set.Addresses {
			byID[a.ID] = a
		}
		for _, a := range set.Addresses {
			if err := ctx.Err(); err != nil {
				return AliasSearchView{}, unavailable()
			}
			if query.Exclude != "" && a.ID == query.Exclude {
				continue
			}
			if query.ExcludeGroups && a.Kind == "group" {
				continue
			}
			if query.Kind != "" && a.Kind != query.Kind {
				continue
			}
			if query.Family != "" && !addressMatchesFamily(byID, a.ID, query.Family == "ipv6", false) {
				continue
			}
			summary := addressSummary(a, byID)
			item, ok := matchAlias(a.ID, a.Name, a.Description, a.Kind, "", summary, qRunes)
			if ok {
				items = append(items, item)
			}
		}
	case "ports":
		byID := make(map[string]portAlias, len(set.Ports))
		for _, p := range set.Ports {
			byID[p.ID] = p
		}
		for _, p := range set.Ports {
			if err := ctx.Err(); err != nil {
				return AliasSearchView{}, unavailable()
			}
			if query.Exclude != "" && p.ID == query.Exclude {
				continue
			}
			if query.ExcludeGroups && p.Protocol == "group" {
				continue
			}
			if query.Kind != "" && p.Protocol != query.Kind {
				continue
			}
			summary := portSummary(p, byID)
			item, ok := matchAlias(p.ID, p.Name, p.Description, "", p.Protocol, summary, qRunes)
			if ok {
				items = append(items, item)
			}
		}
	}

	slices.SortFunc(items, func(a, b AliasSearchItem) int {
		if c := cmp.Compare(b.Score, a.Score); c != 0 {
			return c
		}
		if c := cmp.Compare(strings.ToLower(a.Name), strings.ToLower(b.Name)); c != 0 {
			return c
		}
		if c := cmp.Compare(a.Name, b.Name); c != 0 {
			return c
		}
		return cmp.Compare(a.ID, b.ID)
	})
	if len(items) > limit {
		items = items[:limit]
	}
	return AliasSearchView{
		State:        "saved",
		SavedVersion: savedVersion,
		Items:        items,
	}, nil
}

func addressMatchesFamily(byID map[string]address, id string, six, nested bool) bool {
	a, ok := byID[id]
	if !ok {
		return false
	}
	if a.Kind == "group" {
		return !nested && slices.ContainsFunc(a.Members, func(m string) bool {
			return addressMatchesFamily(byID, m, six, true)
		})
	}
	var v netip.Addr
	var err error
	switch a.Kind {
	case "network":
		var p netip.Prefix
		p, err = netip.ParsePrefix(a.Prefix)
		v = p.Addr()
	case "range":
		v, err = netip.ParseAddr(a.First)
	default:
		v, err = netip.ParseAddr(a.Address)
	}
	return err == nil && ((six && v.Is6() && !v.Is4In6()) || (!six && v.Is4()))
}

func addressSummary(a address, byID map[string]address) string {
	switch a.Kind {
	case "host":
		return a.Address
	case "network":
		return a.Prefix
	case "range":
		return a.First + "–" + a.Last
	case "group":
		return groupMemberSummary(a.Members, func(id string) string {
			if m, ok := byID[id]; ok {
				return m.Name
			}
			return id
		})
	default:
		return ""
	}
}

func portSummary(p portAlias, byID map[string]portAlias) string {
	switch p.Protocol {
	case "tcp", "udp":
		return strings.ToUpper(p.Protocol) + " " + strings.Join(p.Ports, ", ")
	case "icmp":
		if len(p.Types) == 0 {
			return "ICMP (any type)"
		}
		return "ICMP " + strings.Join(p.Types, ", ")
	case "icmpv6":
		if len(p.Types) == 0 {
			return "ICMPv6 (any type)"
		}
		return "ICMPv6 " + strings.Join(p.Types, ", ")
	case "group":
		return groupMemberSummary(p.Members, func(id string) string {
			if m, ok := byID[id]; ok {
				return m.Name
			}
			return id
		})
	default:
		return ""
	}
}

func groupMemberSummary(members []string, nameOf func(string) string) string {
	if len(members) == 0 {
		return "Empty group"
	}
	if len(members) <= 3 {
		names := make([]string, len(members))
		for i, id := range members {
			names[i] = nameOf(id)
		}
		return "Group: " + strings.Join(names, ", ")
	}
	return fmt.Sprintf("Group (%d aliases)", len(members))
}

func matchAlias(id, name, description, kind, protocol, summary string, qRunes []rune) (AliasSearchItem, bool) {
	item := AliasSearchItem{
		ID:          id,
		Name:        name,
		Description: description,
		Kind:        kind,
		Protocol:    protocol,
		Summary:     summary,
	}
	if len(qRunes) == 0 {
		item.Highlights = []TextSegment{{Text: name, Match: false}}
		if summary != "" {
			item.SummaryHighlights = []TextSegment{{Text: summary, Match: false}}
		}
		return item, true
	}

	nameScore, nameMask := scoreName(name, qRunes)
	sumScore, sumMask := scoreSubstring(summary, qRunes)
	score := nameScore
	if score == 0 {
		if sumScore > 0 {
			score = sumScore
		} else if descScore, _ := scoreSubstring(description, qRunes); descScore > 0 {
			score = descScore - 30
			if score < 40 {
				score = 40
			}
		}
	}
	if score <= 0 {
		return AliasSearchItem{}, false
	}
	item.Score = score
	item.Highlights = buildSegments([]rune(name), nameMask)
	if summary != "" {
		item.SummaryHighlights = buildSegments([]rune(summary), sumMask)
	}
	return item, true
}

func toLowerRunes(s string) []rune {
	out := []rune(s)
	for i, r := range out {
		out[i] = unicode.ToLower(r)
	}
	return out
}

func isSeparator(r rune) bool {
	return r == '-' || r == '_' || r == '/' || r == '.' || r == ':' || unicode.IsSpace(r)
}

func isBoundary(orig []rune, idx int) bool {
	if idx == 0 {
		return true
	}
	prev := orig[idx-1]
	if isSeparator(prev) {
		return true
	}
	return unicode.IsLower(prev) && unicode.IsUpper(orig[idx])
}

func indexRunes(haystack, needle []rune) int {
	if len(needle) == 0 {
		return 0
	}
	if len(needle) > len(haystack) {
		return -1
	}
	for i := 0; i <= len(haystack)-len(needle); i++ {
		if slices.Equal(haystack[i:i+len(needle)], needle) {
			return i
		}
	}
	return -1
}

func scoreSubstring(text string, qRunes []rune) (int, []bool) {
	if text == "" || len(qRunes) == 0 {
		return 0, nil
	}
	orig := []rune(text)
	lower := toLowerRunes(text)
	idx := indexRunes(lower, qRunes)
	if idx < 0 {
		return 0, nil
	}
	mask := make([]bool, len(orig))
	for i := idx; i < idx+len(qRunes); i++ {
		mask[i] = true
	}
	if idx == 0 {
		return 105, mask
	}
	return 90, mask
}

func scoreName(name string, qRunes []rune) (int, []bool) {
	orig := []rune(name)
	n := len(orig)
	m := len(qRunes)
	if n == 0 || m == 0 {
		return 0, nil
	}
	lower := toLowerRunes(name)

	// 1. Exact match.
	if n == m && slices.Equal(lower, qRunes) {
		mask := make([]bool, n)
		for i := range mask {
			mask[i] = true
		}
		return 1000, mask
	}

	// 2. Prefix match.
	if n > m && slices.Equal(lower[:m], qRunes) {
		mask := make([]bool, n)
		for i := 0; i < m; i++ {
			mask[i] = true
		}
		return 850 + clampInt(50-(n-m), 0, 50), mask
	}

	// 3. Word-boundary substring match.
	for i := 0; i <= n-m; i++ {
		if isBoundary(orig, i) && slices.Equal(lower[i:i+m], qRunes) {
			mask := make([]bool, n)
			for j := i; j < i+m; j++ {
				mask[j] = true
			}
			return 700 + clampInt(40-i, 0, 40) + clampInt(30-(n-m), 0, 30), mask
		}
	}

	// 4. General contiguous substring match.
	if idx := indexRunes(lower, qRunes); idx >= 0 {
		mask := make([]bool, n)
		for j := idx; j < idx+m; j++ {
			mask[j] = true
		}
		return 550 + clampInt(40-idx, 0, 40) + clampInt(30-(n-m), 0, 30), mask
	}

	// Cap fuzzy and typo tiers to keep scoring linear-fast even on long pasted strings.
	if m <= 64 {
		// 5. Optimal fuzzy subsequence match (O(m*n) with O(m+n) pre-check).
		if subScore, subMask, ok := scoreSubsequence(orig, lower, qRunes); ok {
			return subScore, subMask
		}

		// 6. Typo-tolerant fallback (Damerau-Levenshtein) for queries of 3..64 characters.
		if m >= 3 {
			if typoScore, typoMask, ok := scoreTypo(orig, lower, qRunes); ok {
				return typoScore, typoMask
			}
		}
	}

	return 0, nil
}

func isSubsequenceRunes(haystack, needle []rune) bool {
	if len(needle) > len(haystack) {
		return false
	}
	i := 0
	for j := 0; j < len(haystack) && i < len(needle); j++ {
		if haystack[j] == needle[i] {
			i++
		}
	}
	return i == len(needle)
}

func scoreSubsequence(orig, lower, qRunes []rune) (int, []bool, bool) {
	n := len(lower)
	m := len(qRunes)
	if !isSubsequenceRunes(lower, qRunes) {
		return 0, nil, false
	}
	const negInf = -1 << 28

	// O(m*n) DP: dp[i*n+j] is the best score matching qRunes[0..i] ending at lower[j].
	// For each row i, track running best over k < j-1 with a flat gap penalty (-4) plus
	// exact consecutive transition from k == j-1 (+22), avoiding any O(n) inner loop.
	dp := make([]int, m*n)
	prev := make([]int, m*n)
	for i := range dp {
		dp[i] = negInf
		prev[i] = -1
	}

	for j := 0; j < n; j++ {
		if lower[j] == qRunes[0] {
			s := 10
			if j == 0 {
				s += 20
			} else if isBoundary(orig, j) {
				s += 18
			} else {
				s -= min(j, 10)
			}
			dp[j] = s
		}
	}

	for i := 1; i < m; i++ {
		bestGap := negInf
		bestGapIdx := -1
		for j := i; j < n; j++ {
			// Advance the running non-adjacent best (k <= j-2), applying a linear gap decay (-2 per skipped char).
			if bestGap != negInf {
				bestGap -= 2
			}
			if j-2 >= i-1 {
				candGap := dp[(i-1)*n+(j-2)]
				if candGap != negInf {
					candGap -= 2 // gap of 1 skipped rune (j-1)
					if candGap > bestGap {
						bestGap = candGap
						bestGapIdx = j - 2
					}
				}
			}
			if lower[j] != qRunes[i] {
				continue
			}
			best := negInf
			bestK := -1
			if adj := dp[(i-1)*n+(j-1)]; adj != negInf {
				best = adj + 22
				bestK = j - 1
			}
			if bestGap > best {
				best = bestGap
				bestK = bestGapIdx
			}
			if best == negInf {
				continue
			}
			score := best + 10
			if isBoundary(orig, j) {
				score += 18
			}
			dp[i*n+j] = score
			prev[i*n+j] = bestK
		}
	}

	bestEnd := -1
	bestVal := negInf
	for j := m - 1; j < n; j++ {
		v := dp[(m-1)*n+j]
		if v == negInf {
			continue
		}
		adj := v - min(n-1-j, 15)
		if adj > bestVal {
			bestVal = adj
			bestEnd = j
		}
	}
	if bestEnd < 0 {
		return 0, nil, false
	}

	mask := make([]bool, n)
	pos := bestEnd
	for i := m - 1; i >= 0; i-- {
		mask[pos] = true
		pos = prev[i*n+pos]
	}

	maxPossible := m * 50
	if maxPossible <= 0 {
		maxPossible = 1
	}
	normalized := 250 + ((bestVal+m*10)*249)/(maxPossible+m*10)
	return clampInt(normalized, 250, 499), mask, true
}

func scoreTypo(orig, lower, qRunes []rune) (int, []bool, bool) {
	n := len(lower)
	m := len(qRunes)
	maxDist := 1
	if m >= 6 {
		maxDist = 2
	}

	type window struct {
		start, end int
		full       bool
	}
	candidates := make([]window, 0, 10)
	candidates = append(candidates, window{0, n, true})
	// Prefix windows across {-maxDist..+maxDist} so typo prefixes like "pritn" or "dtbase" match.
	for delta := -maxDist; delta <= maxDist; delta++ {
		end := m + delta
		if end >= 3 && end < n {
			candidates = append(candidates, window{0, end, false})
		}
	}
	// Individual word tokens inside the name.
	tokStart := 0
	for i := 0; i <= n; i++ {
		if i == n || isSeparator(orig[i]) {
			if i-tokStart >= 3 {
				candidates = append(candidates, window{tokStart, i, false})
			}
			tokStart = i + 1
		}
	}

	bestScore := 0
	var bestWin window
	for _, w := range candidates {
		wLen := w.end - w.start
		if absInt(wLen-m) > maxDist {
			continue
		}
		dist := osaDistance(qRunes, lower[w.start:w.end], maxDist)
		if dist > maxDist {
			continue
		}
		denom := max(m, wLen)
		sim := 1.0 - float64(dist)/float64(denom)
		if sim < 0.60 {
			continue
		}
		score := 120 + int(sim*90)
		if w.full {
			score += 10
		}
		if w.start == 0 {
			score += 5
		}
		if score > bestScore {
			bestScore = score
			bestWin = w
		}
	}
	if bestScore == 0 {
		return 0, nil, false
	}

	mask := make([]bool, n)
	lcsMask := lcsHighlightMask(qRunes, lower[bestWin.start:bestWin.end])
	for i, hit := range lcsMask {
		if hit {
			mask[bestWin.start+i] = true
		}
	}
	return clampInt(bestScore, 120, 235), mask, true
}

// osaDistance computes restricted Damerau-Levenshtein distance (insertions, deletions,
// substitutions, and adjacent transpositions), returning maxDist+1 when exceeded.
func osaDistance(a, b []rune, maxDist int) int {
	la, lb := len(a), len(b)
	if absInt(la-lb) > maxDist {
		return maxDist + 1
	}
	cols := lb + 1
	d := make([]int, (la+1)*cols)
	for i := 0; i <= la; i++ {
		d[i*cols] = i
	}
	for j := 0; j <= lb; j++ {
		d[j] = j
	}
	for i := 1; i <= la; i++ {
		rowMin := maxDist + 1
		for j := 1; j <= lb; j++ {
			cost := 1
			if a[i-1] == b[j-1] {
				cost = 0
			}
			v := min(
				d[(i-1)*cols+j]+1,
				min(d[i*cols+(j-1)]+1, d[(i-1)*cols+(j-1)]+cost),
			)
			if i > 1 && j > 1 && a[i-1] == b[j-2] && a[i-2] == b[j-1] {
				v = min(v, d[(i-2)*cols+(j-2)]+1)
			}
			d[i*cols+j] = v
			if v < rowMin {
				rowMin = v
			}
		}
		if rowMin > maxDist {
			return maxDist + 1
		}
	}
	return d[la*cols+lb]
}

// lcsHighlightMask marks runes in target that belong to a longest common subsequence with query.
func lcsHighlightMask(query, target []rune) []bool {
	m, n := len(query), len(target)
	cols := n + 1
	dp := make([]int, (m+1)*cols)
	for i := m - 1; i >= 0; i-- {
		for j := n - 1; j >= 0; j-- {
			if query[i] == target[j] {
				dp[i*cols+j] = 1 + dp[(i+1)*cols+(j+1)]
			} else {
				dp[i*cols+j] = max(dp[(i+1)*cols+j], dp[i*cols+(j+1)])
			}
		}
	}
	mask := make([]bool, n)
	i, j := 0, 0
	for i < m && j < n {
		if query[i] == target[j] {
			mask[j] = true
			i++
			j++
		} else if dp[(i+1)*cols+j] >= dp[i*cols+(j+1)] {
			i++
		} else {
			j++
		}
	}
	return mask
}

func buildSegments(orig []rune, mask []bool) []TextSegment {
	if len(orig) == 0 {
		return nil
	}
	if len(mask) != len(orig) {
		return []TextSegment{{Text: string(orig), Match: false}}
	}
	out := make([]TextSegment, 0, 4)
	curMatch := mask[0]
	start := 0
	for i := 1; i < len(orig); i++ {
		if mask[i] != curMatch {
			out = append(out, TextSegment{Text: string(orig[start:i]), Match: curMatch})
			start = i
			curMatch = mask[i]
		}
	}
	out = append(out, TextSegment{Text: string(orig[start:]), Match: curMatch})
	return out
}

func clampInt(v, lo, hi int) int {
	if v < lo {
		return lo
	}
	if v > hi {
		return hi
	}
	return v
}

func absInt(v int) int {
	if v < 0 {
		return -v
	}
	return v
}
