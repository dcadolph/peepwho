// Package render writes the human-readable terminal form of whotoask answers to
// an io.Writer, coloring only when its Style says so.
package render

import (
	"fmt"
	"io"
	"os"
	"slices"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/dcadolph/whotoask/internal/eval"
	"github.com/dcadolph/whotoask/internal/feedback"
	"github.com/dcadolph/whotoask/internal/index"
	"github.com/dcadolph/whotoask/internal/recall"
	"github.com/dcadolph/whotoask/internal/resolve"
	"github.com/dcadolph/whotoask/internal/text"
	"github.com/dcadolph/whotoask/internal/util"
)

// IdentityView is the audit of inferred identity merges, grouped by person.
type IdentityView struct {
	// Merges is how many inferred joins the index holds.
	Merges int `json:"merges"`
	// People are the people with at least one inferred join, in name order.
	People []IdentityPerson `json:"people"`
}

// IdentityPerson is one person and the aliases inferred to be them.
type IdentityPerson struct {
	// ID is the person's canonical identifier.
	ID string `json:"id"`
	// Name is the person's display name.
	Name string `json:"name,omitempty"`
	// Email is the person's work email.
	Email string `json:"email,omitempty"`
	// Joins are the aliases inferred to be this person, in alias order.
	Joins []IdentityJoin `json:"joins"`
}

// IdentityJoin is one inferred alias with its confidence and evidence.
type IdentityJoin struct {
	// Alias is the identifier that was folded in, such as "github:kim-doe".
	Alias string `json:"alias"`
	// Confidence is how sure the merge is, from 0 to 1.
	Confidence float64 `json:"confidence"`
	// Reason names the evidence, such as "unique name match".
	Reason string `json:"reason"`
}

// UnlinkedView is the worklist of declared owners with nothing recorded
// against them: the people a source of record names but whose work whotoask
// cannot find, almost always because their handle was never tied to the address
// they commit under.
type UnlinkedView struct {
	// Unlinked is how many declared owners have no recorded work.
	Unlinked int `json:"unlinked"`
	// Declared is how many declared owners there are in total.
	Declared int `json:"declared"`
	// Owners are those without work, the ones owning most first.
	Owners []UnlinkedOwner `json:"owners"`
}

// UnlinkedOwner is one declared owner with nothing recorded against them.
type UnlinkedOwner struct {
	// ID is the identifier the source of record knows them by.
	ID string `json:"id"`
	// Name is their display name, when a source gave one.
	Name string `json:"name,omitempty"`
	// Owns are the areas they are the owner of record for.
	Owns []string `json:"owns"`
}

// rank formats a one-based position in a result list, right-aligned so single
// and double digits line up.
func rank(n int) string { return fmt.Sprintf("%2d", n) }

// nameWidth returns the column width to pad the names of rows to, capped so one
// long name does not push every row far to the right.
func nameWidth[T any](rows []T, name func(T) string) int {
	width := 0
	for _, r := range rows {
		if n := len([]rune(name(r))); n > width {
			width = n
		}
	}
	if width > 26 {
		width = 26
	}
	return width
}

// RenderAsk prints an answer as ranked, aligned rows: each match numbered, with
// a strength bar and the reasons it surfaced dimmed beneath. Every indexed
// string is escaped on its way to the terminal, since names and reasons come
// from sources third parties control.
func RenderAsk(w io.Writer, query string, view resolve.JSONAnswer, s Style) {
	fmt.Fprintf(w, "\n%s\n", s.Bold("Who knows about "+strconv.Quote(query)))
	if view.Summary != "" {
		fmt.Fprintf(w, "%s\n", s.Dim(EscapeControls(view.Summary)))
	}
	if len(view.People) == 0 && len(view.Departed) == 0 && len(view.Channels) == 0 && len(view.Teams) == 0 {
		fmt.Fprintf(w, "\n  %s\n\n", s.Dim(
			"Nothing matched. Try the words your team would use, or `whotoask directory` to see what is indexed."))
		return
	}
	// Each section ends on a blank line, so only the first one printed needs
	// its own to stand clear of the title.
	first := true
	section := func(title string) {
		if first {
			fmt.Fprintln(w)
			first = false
		}
		fmt.Fprintf(w, "  %s\n\n", s.Dim(title))
	}
	// The departed close the people list rather than open a section of their
	// own, so the ranking reads as one list with them at the bottom.
	if people := slices.Concat(view.People, view.Departed); len(people) > 0 {
		section("PEOPLE")
		width := nameWidth(people, func(p resolve.JSONPerson) string { return p.Name })
		for i, p := range people {
			name := EscapeControls(p.Name)
			role := EscapeControls(joinRole(p.Title, p.Team))
			if p.Deactivated {
				role = joinRole(role, "deactivated")
			}
			fmt.Fprintf(w, "  %s  %s  %s\n",
				s.Dim(rank(i+1)), pad(s.Bold(name), name, width), s.Dim(role))
			fmt.Fprintf(w, "      %s %s   %s\n",
				s.ConfBar(p.Strength), s.Accent(pct(p.Strength)),
				s.Dim(strings.Join(EscapeControlsList(p.Reasons), " · ")))
			fmt.Fprintln(w)
		}
	}
	if len(view.Channels) > 0 {
		section("CHANNELS")
		for i, ch := range view.Channels {
			fmt.Fprintf(w, "  %s  %s\n", s.Dim(rank(i+1)), s.Bold("#"+EscapeControls(ch.Name)))
			fmt.Fprintf(w, "      %s %s   %s\n",
				s.ConfBar(ch.Strength), s.Accent(pct(ch.Strength)),
				s.Dim(strings.Join(EscapeControlsList(ch.Reasons), " · ")))
			if ch.URL != "" {
				fmt.Fprintf(w, "      %s %s\n", s.Accent("\u2192"), s.Dim(EscapeControls(ch.URL)))
			}
			fmt.Fprintln(w)
		}
	}
	if len(view.Teams) > 0 {
		section("TEAMS (OWNERS OF RECORD)")
		width := nameWidth(view.Teams, func(t resolve.JSONTeam) string { return t.Name })
		for i, t := range view.Teams {
			name := EscapeControls(t.Name)
			fmt.Fprintf(w, "  %s  %s  %s\n", s.Dim(rank(i+1)), pad(s.Bold(name), name, width),
				s.Dim(EscapeControls(t.Team)))
			fmt.Fprintf(w, "      %s %s   %s\n",
				s.ConfBar(t.Strength), s.Accent(pct(t.Strength)),
				s.Dim(strings.Join(EscapeControlsList(t.Reasons), " · ")))
			fmt.Fprintln(w)
		}
	}
}

// RenderNear prints who works near a person as a ranked list, each with the
// shared teams and topics that put them close.
func RenderNear(w io.Writer, name string, near []index.Adjacent, s Style) {
	fmt.Fprintf(w, "\n%s\n", s.Bold("Near "+EscapeControls(name)))
	if len(near) == 0 {
		fmt.Fprintf(w, "\n  %s\n\n", s.Dim("No one indexed shares a team or topic with them yet."))
		return
	}
	fmt.Fprintln(w)
	width := 0
	for _, a := range near {
		if n := len([]rune(a.Name)); n > width {
			width = n
		}
	}
	if width > 26 {
		width = 26
	}
	for i, a := range near {
		aName := EscapeControls(a.Name)
		fmt.Fprintf(w, "  %s  %s  %s\n",
			s.Dim(rank(i+1)), pad(s.Bold(aName), aName, width),
			s.Dim(strings.Join(EscapeControlsList(a.Reasons), " · ")))
	}
	fmt.Fprintln(w)
}

// RenderStatus prints the index summary as a clean labeled block, with a
// friendly build time and the index path shortened to the home directory.
func RenderStatus(w io.Writer, v map[string]any, s Style) {
	geti := func(k string) int { n, _ := v[k].(int); return n }
	gets := func(k string) string { str, _ := v[k].(string); return str }
	line := func(label, val string) { fmt.Fprintf(w, "  %s  %s\n", pad(s.Dim(label), label, 8), val) }

	fmt.Fprintf(w, "\n%s\n\n", s.Bold("whotoask index"))
	people := s.Bold(fmt.Sprintf("%d", geti("people")))
	if n := geti("deactivated"); n > 0 {
		people += s.Dim(fmt.Sprintf(" including %d deactivated", n))
	}
	line("People", people+"  "+
		s.Dim(fmt.Sprintf("%d teams · %d subjects · %d channels",
			geti("teams"), geti("subjects"), geti("channels"))))
	if bt := gets("built_at"); bt != "" {
		when := bt
		if t, err := time.Parse(time.RFC3339, bt); err == nil {
			when = t.Format("Jan 2, 3:04 PM")
		}
		if age := gets("age"); age != "" {
			when += "  (" + humanAge(age) + ")"
		}
		line("Built", s.Dim(when))
	} else if age := gets("age"); age != "" {
		line("Built", s.Dim(humanAge(age)))
	}
	if src, ok := v["sources"].(map[string]int); ok && len(src) > 0 {
		parts := make([]string, 0, len(src))
		for name, n := range src {
			parts = append(parts, fmt.Sprintf("%s (%d)", name, n))
		}
		sort.Strings(parts)
		line("Sources", strings.Join(parts, ", "))
	}
	enc, emb := "off", "off"
	if e, _ := v["encryption_key_configured"].(bool); e {
		enc = "on"
	}
	if e, _ := v["embeddings"].(bool); e {
		emb = "on"
	}
	line("At rest", s.Dim("encryption "+enc+" · embeddings "+emb))
	if idx := gets("index"); idx != "" {
		if home, err := os.UserHomeDir(); err == nil && home != "" && strings.HasPrefix(idx, home) {
			idx = "~" + idx[len(home):]
		}
		line("File", s.Dim(idx))
	}
	fmt.Fprintln(w)
}

// humanAge turns a Go duration string such as "1h30m0s" into a phrase such as "1h 30m ago", or
// "just now" under a minute. A string that does not parse is shown as is with "ago".
func humanAge(d string) string {
	dur, err := time.ParseDuration(d)
	if err != nil {
		return d + " ago"
	}
	switch {
	case dur < time.Minute:
		return "just now"
	case dur < time.Hour:
		return fmt.Sprintf("%dm ago", int(dur.Minutes()))
	case dur < 24*time.Hour:
		h, m := int(dur.Hours()), int(dur.Minutes())%60
		if m == 0 {
			return fmt.Sprintf("%dh ago", h)
		}
		return fmt.Sprintf("%dh %dm ago", h, m)
	default:
		return fmt.Sprintf("%dd ago", int(dur.Hours()/24))
	}
}

// RenderRecall prints the conversations a person took part in, each card led by
// its source in the source's own color, then where it happened, who was there,
// any retained solution, and a link back.
func RenderRecall(w io.Writer, ans recall.Answer, s Style) {
	fmt.Fprintf(w, "\n%s\n", s.Bold("Your recall"))
	if ans.Query != "" {
		fmt.Fprintf(w, "%s\n", s.Dim("matching "+strconv.Quote(ans.Query)))
	}
	if len(ans.Episodes) == 0 {
		fmt.Fprintf(w, "\n  %s\n", s.Dim("Nothing found in the conversations you took part in."))
		if ans.Scope.Note != "" {
			fmt.Fprintf(w, "  %s\n", s.Dim(ans.Scope.Note))
		}
		fmt.Fprintln(w)
		return
	}
	fmt.Fprintln(w)
	for _, ep := range ans.Episodes {
		label, code := sourceColor(ep.Source)
		date := ""
		if !ep.When.IsZero() {
			date = ep.When.Format("Jan 2, 2006")
		}
		fmt.Fprintf(w, "  %s   %s\n", s.Color256(code, "● "+strings.ToUpper(label)), s.Dim(date))
		place := EscapeControls(ep.Place)
		if place != "" && (ep.Kind == "thread" || ep.Kind == "window") {
			place = "#" + place
		}
		if place != "" {
			fmt.Fprintf(w, "  %s\n", s.Bold(place))
		}
		fmt.Fprintf(w, "  %s\n", s.Dim(EscapeControls(recallWith(ep.People))))
		// Every other answer whotoask gives says why it surfaced, and a recalled
		// conversation needs it most: without the matched words, a thread from
		// an unrelated-looking channel reads as a random result rather than the
		// one place the asked-about word was actually said.
		if words := recallMatchedWords(ans.Query, ep.Matched); len(words) > 0 {
			fmt.Fprintf(w, "  %s\n", s.Dim("matched "+strings.Join(EscapeControlsList(words), ", ")))
		}
		if ep.Solution != nil {
			if ep.Solution.Summary != "" {
				fmt.Fprintf(w, "  %s\n", EscapeControls(ep.Solution.Summary))
			}
			for _, n := range ep.Solution.Notes {
				fmt.Fprintf(w, "    %s %s\n",
					s.Dim(EscapeControls(n.Author)+":"), EscapeControls(n.Text))
			}
		}
		if ep.Permalink != "" {
			fmt.Fprintf(w, "  %s %s\n",
				s.Color256(code, "→ open in "+label), s.Dim(EscapeControls(ep.Permalink)))
		}
		if ep.LinkMayHaveExpired {
			fmt.Fprintf(w, "  %s\n", s.Dim("link may be stale"))
		}
		fmt.Fprintln(w)
	}
	if ans.Scope.Note != "" {
		fmt.Fprintf(w, "  %s\n\n", s.Dim(ans.Scope.Note))
	}
}

// recallMatchedWords turns the matched search keys back into the words the
// person typed. Matching runs on stems, so the raw keys read as misspellings:
// asking about "vacation" and being told the match was "vacat" looks like a
// bug rather than an explanation. A key with no word behind it is shown as it
// is rather than dropped, since a partial explanation beats none.
func recallMatchedWords(query string, matched []string) []string {
	if len(matched) == 0 {
		return nil
	}
	byStem := make(map[string]string)
	for _, word := range strings.Fields(query) {
		word = strings.Trim(strings.ToLower(word), `"'.,?!`)
		if word == "" {
			continue
		}
		if stem := text.Stem(word); stem != "" {
			if _, seen := byStem[stem]; !seen {
				byStem[stem] = word
			}
		}
	}
	words := make([]string, 0, len(matched))
	for _, key := range matched {
		word := byStem[key]
		if word == "" {
			word = key
		}
		words = append(words, word)
	}
	// Several stems can lead back to one word, which is worth saying once.
	return util.Distinct(words, func(w string) string { return w })
}

// recallWith names who else was in a conversation.
func recallWith(people []recall.Person) string {
	names := make([]string, 0, len(people))
	for _, p := range people {
		switch {
		case p.Name != "":
			names = append(names, p.Name)
		case p.Email != "":
			names = append(names, p.Email)
		}
	}
	switch {
	case len(names) == 0:
		return "on your own"
	case len(names) == 1:
		return "with " + names[0]
	case len(names) <= recallNamesShown+1:
		return "with " + strings.Join(names[:len(names)-1], ", ") + " and " + names[len(names)-1]
	default:
		// A busy channel puts everyone who spoke in the room, and printing all
		// of them buries the conversation under a wall of names. The first few
		// are who a person would recognize; the rest is a count.
		return fmt.Sprintf("with %s and %d others",
			strings.Join(names[:recallNamesShown], ", "), len(names)-recallNamesShown)
	}
}

// recallNamesShown is how many people a recalled conversation names before it
// counts the rest.
const recallNamesShown = 3

// RenderDirectory prints the requested directory section, or every section when
// none was named, each as a titled, counted list.
func RenderDirectory(w io.Writer, dir resolve.Directory, section string, s Style) {
	show := func(name string) bool { return section == "" || section == name }
	any := false
	head := func(title string, n int) {
		any = true
		fmt.Fprintf(w, "\n%s  %s\n\n", s.Bold(title), s.Dim(strconv.Itoa(n)))
	}
	if show("people") && len(dir.People) > 0 {
		head("People", len(dir.People))
		width := 0
		for _, p := range dir.People {
			if n := len([]rune(p.Name)); n > width {
				width = n
			}
		}
		if width > 26 {
			width = 26
		}
		for _, p := range dir.People {
			name := EscapeControls(p.Name)
			role := EscapeControls(joinRole(p.Title, p.Team))
			if p.Deactivated {
				role = joinRole(role, "deactivated")
			}
			fmt.Fprintf(w, "  %s  %s\n", pad(s.Bold(name), name, width), s.Dim(role))
		}
	}
	if show("channels") && len(dir.Channels) > 0 {
		head("Channels", len(dir.Channels))
		for _, ch := range dir.Channels {
			meta := peopleCount(ch.Members)
			if ch.Topic != "" {
				meta = EscapeControls(ch.Topic) + " · " + meta
			}
			fmt.Fprintf(w, "  %s  %s\n", s.Bold("#"+EscapeControls(ch.Name)), s.Dim(meta))
		}
	}
	if show("teams") && len(dir.Teams) > 0 {
		head("Teams", len(dir.Teams))
		width := teamWidth(dir.Teams)
		for _, t := range dir.Teams {
			name := EscapeControls(t.Name)
			fmt.Fprintf(w, "  %s  %s\n", pad(s.Bold(name), name, width), s.Dim(peopleCount(t.People)))
		}
	}
	if show("topics") && len(dir.Topics) > 0 {
		head("Topics", len(dir.Topics))
		width := topicWidth(dir.Topics)
		for _, t := range dir.Topics {
			name := EscapeControls(t.Name)
			fmt.Fprintf(w, "  %s  %s\n", pad(s.Bold(name), name, width), s.Dim(peopleCount(t.People)))
		}
	}
	if !any {
		fmt.Fprintf(w, "\n  %s\n", s.Dim("Nothing indexed yet. Run `whotoask index` against a source."))
	}
	fmt.Fprintln(w)
}

// teamWidth returns the padding width for a list of team names.
func teamWidth(teams []resolve.DirectoryTeam) int {
	w := 0
	for _, t := range teams {
		if n := len([]rune(t.Name)); n > w {
			w = n
		}
	}
	if w > 26 {
		w = 26
	}
	return w
}

// topicWidth returns the padding width for a list of topic names.
func topicWidth(topics []resolve.DirectoryTopic) int {
	w := 0
	for _, t := range topics {
		if n := len([]rune(t.Name)); n > w {
			w = n
		}
	}
	if w > 26 {
		w = 26
	}
	return w
}

// peopleCount labels a count of people, singular or plural.
func peopleCount(n int) string {
	if n == 1 {
		return "1 person"
	}
	return fmt.Sprintf("%d people", n)
}

// RenderFeedback prints recorded votes, each marked helpful or wrong, with the
// question it was cast on and any note.
func RenderFeedback(w io.Writer, entries []feedback.Entry, s Style) {
	fmt.Fprintf(w, "\n%s  %s\n", s.Bold("Feedback"), s.Dim(strconv.Itoa(len(entries))))
	if len(entries) == 0 {
		fmt.Fprintf(w, "\n  %s\n\n", s.Dim("No votes recorded yet."))
		return
	}
	fmt.Fprintln(w)
	for _, e := range entries {
		mark := s.Accent("✓ helpful")
		if e.Vote < 0 {
			mark = s.Bad("✗ wrong")
		}
		target := EscapeControls(e.Person)
		if e.Channel != "" {
			target = "#" + EscapeControls(e.Channel)
		}
		fmt.Fprintf(w, "  %s  %s  %s\n", mark, s.Bold(target), s.Dim("on "+strconv.Quote(e.Query)))
		if e.Comment != "" {
			// The comment arrives through the web feedback endpoint, so it is
			// user-supplied text like any indexed field.
			fmt.Fprintf(w, "      %s\n", s.Dim("“"+EscapeControls(e.Comment)+"”"))
		}
	}
	fmt.Fprintln(w)
}

// RenderIdentity prints the inferred identity merges grouped by person, each
// alias with a confidence bar, the confidence, and the evidence.
func RenderIdentity(w io.Writer, v IdentityView, s Style) {
	if v.Merges == 0 {
		fmt.Fprintln(w, s.Dim(
			"No inferred identity merges. Joins by email or provider id are certain and are not shown here."))
		return
	}
	fmt.Fprintln(w, s.Bold(fmt.Sprintf("Identity joins  %d inferred across %d people", v.Merges, len(v.People))))
	width := aliasWidth(v.People)
	for _, p := range v.People {
		who := s.Bold(EscapeControls(p.Name))
		if p.Name == "" {
			who = s.Bold(EscapeControls(p.ID))
		}
		if p.Email != "" {
			who += "  " + s.Dim(EscapeControls(p.Email))
		}
		fmt.Fprintf(w, "\n  %s\n", who)
		for _, j := range p.Joins {
			_, code := sourceColor(handleSource(j.Alias))
			jAlias := EscapeControls(j.Alias)
			alias := pad(s.Color256(code, jAlias), jAlias, width)
			fmt.Fprintf(w, "    %s %s  %s  %s\n",
				s.ConfBar(j.Confidence), pct(j.Confidence), alias, s.Dim(EscapeControls(j.Reason)))
		}
	}
}

// aliasWidth is the longest alias across all joins, for column alignment.
func aliasWidth(people []IdentityPerson) int {
	w := 0
	for _, p := range people {
		for _, j := range p.Joins {
			if n := len([]rune(j.Alias)); n > w {
				w = n
			}
		}
	}
	return w
}

// handleSource returns the source prefix of an id like "github:kim-doe", or the
// empty string when there is no prefix.
func handleSource(id string) string {
	if i := strings.IndexByte(id, ':'); i > 0 {
		return id[:i]
	}
	return ""
}

// RenderSearch prints ranked search results: people in bold, channels prefixed
// with a hash, each with a context line and the fields the query matched.
func RenderSearch(w io.Writer, query string, results []resolve.SearchResult, s Style) {
	if len(results) == 0 {
		fmt.Fprintln(w, s.Dim(
			"No people or channels match “"+query+"”. Try `whotoask directory` to see what is indexed."))
		return
	}
	fmt.Fprintln(w, s.Bold(fmt.Sprintf("%d result%s for “%s”", len(results), Plural(len(results)), query)))
	width := searchNameWidth(results)
	for i, r := range results {
		name := EscapeControls(r.Name)
		raw, label := name, s.Bold(name)
		if r.Kind == "channel" {
			raw = "#" + name
			label = s.Color256(197, raw)
		}
		fmt.Fprintf(w, "  %s  %s  %s",
			s.Dim(rank(i+1)), pad(label, raw, width), s.Dim(EscapeControls(searchContext(r))))
		if len(r.Matched) > 0 {
			fmt.Fprintf(w, "  %s", s.Dim("· "+strings.Join(EscapeControlsList(r.Matched), ", ")))
		}
		fmt.Fprintln(w)
	}
}

// searchContext is the muted line after a result's name: a person's role or
// email, or a channel's topic.
func searchContext(r resolve.SearchResult) string {
	if r.Kind == "channel" {
		if r.Team != "" {
			return "channel · " + r.Team
		}
		return "channel"
	}
	if role := joinRole(r.Title, r.Team); role != "" {
		return role
	}
	return r.Email
}

// searchNameWidth is the column width to pad result names to, capped so one long
// name does not push every row far right.
func searchNameWidth(results []resolve.SearchResult) int {
	width := 0
	for _, r := range results {
		n := len([]rune(r.Name))
		if r.Kind == "channel" {
			n++
		}
		if n > width {
			width = n
		}
	}
	if width > 32 {
		width = 32
	}
	return width
}

// Plural returns "s" unless n is one.
func Plural(n int) string { return Plural2(n, "", "s") }

// RenderRisk prints the knowledge-concentration report, most at-risk first, with
// each topic's bus factor and the people who hold it.
func RenderRisk(w io.Writer, topics []resolve.TopicRisk, s Style) {
	if len(topics) == 0 {
		fmt.Fprintln(w, s.Dim("No topics scored yet. Index a source with expertise signal first."))
		return
	}
	crit, gone := 0, 0
	for _, t := range topics {
		if t.Level == "critical" {
			crit++
		}
		// A subject resting on somebody who has already stopped working on it
		// is the sharpest thing in this report, and counting it in the headline
		// is the difference between a reader noticing and not.
		if len(t.Experts) > 0 && t.Experts[0].Quiet {
			gone++
		}
	}
	head := fmt.Sprintf("Knowledge risk  %d topics scored, %d critical", len(topics), crit)
	if gone > 0 {
		head += fmt.Sprintf(", %d whose strongest expert has moved on", gone)
	}
	fmt.Fprintln(w, s.Bold(head))
	width := riskTopicWidth(topics)
	last := ""
	for _, t := range topics {
		if t.Level != last {
			fmt.Fprintf(w, "\n%s\n", riskLevelLabel(t.Level, s))
			last = t.Level
		}
		topic := EscapeControls(t.Topic)
		fmt.Fprintf(w, "  %s  %s  %s\n",
			pad(s.Bold(topic), topic, width), pct(t.Concentration), s.Dim(fmt.Sprintf("bus factor %d", t.BusFactor)))
		if len(t.Includes) > 0 {
			fmt.Fprintf(w, "      %s\n", s.Dim("also called "+strings.Join(EscapeControlsList(t.Includes), ", ")))
		}
		for _, e := range t.Experts {
			// Whether the person holding a subject is still in it changes what
			// the finding means, so it is said on the same line as their share.
			note := ""
			if e.Quiet {
				note = s.Dim("  not lately")
			}
			fmt.Fprintf(w, "      %s %s%s\n", pct(e.Share), s.Dim(EscapeControls(e.Name)), note)
		}
	}
}

// riskLevelLabel colors a risk level for its section header.
func riskLevelLabel(level string, s Style) string {
	switch level {
	case "critical":
		return s.Bad("CRITICAL")
	case "elevated":
		return s.Warn("ELEVATED")
	default:
		return s.Dim("OK")
	}
}

// riskTopicWidth is the column width for topic names, capped.
func riskTopicWidth(topics []resolve.TopicRisk) int {
	w := 0
	for _, t := range topics {
		if n := len([]rune(t.Topic)); n > w {
			w = n
		}
	}
	if w > 28 {
		w = 28
	}
	return w
}

// RenderDeparture prints the knowledge that would leave with a person.
func RenderDeparture(w io.Writer, query string, imp resolve.DepartureImpact, s Style) {
	if imp.Person == "" {
		fmt.Fprintln(w, s.Dim("No one matches “"+query+"”."))
		return
	}
	name := imp.Name
	if name == "" {
		name = imp.Person
	}
	fmt.Fprintln(w, s.Bold("If "+EscapeControls(name)+" leaves"))
	if len(imp.Sole) == 0 && len(imp.Top) == 0 && len(imp.Regions) == 0 {
		fmt.Fprintln(w, s.Dim("  No topic depends on them as the top expert."))
		return
	}
	// The joined work goes first because it is the heaviest thing they take
	// with them: a region has to be picked up whole, not a subject at a time.
	for _, r := range imp.Regions {
		fmt.Fprintf(w, "\n  %s\n", s.Bad(fmt.Sprintf(
			"A joined body of work goes with them (%d subjects)", r.Size())))
		fmt.Fprintf(w, "    %s\n", strings.Join(EscapeControlsList(r.Topics), ", "))
	}
	if len(imp.Sole) > 0 {
		fmt.Fprintf(w, "\n  %s\n", s.Bad(fmt.Sprintf("Sole expert, nobody else knows (%d)", len(imp.Sole))))
		for _, t := range imp.Sole {
			fmt.Fprintf(w, "    %s %s\n", s.Bad("•"), EscapeControls(t))
		}
	}
	if len(imp.Top) > 0 {
		fmt.Fprintf(w, "\n  %s\n", s.Warn(fmt.Sprintf("Strongest expert, others remain (%d)", len(imp.Top))))
		for _, t := range imp.Top {
			fmt.Fprintf(w, "    %s %s\n", s.Warn("•"), EscapeControls(t))
		}
	}
}

// RenderRelated prints the topics that share a topic's experts.
func RenderRelated(w io.Writer, topic string, rel []resolve.TopicRelation, s Style) {
	if len(rel) == 0 {
		fmt.Fprintln(w, s.Dim(
			"No related topics found. Either the topic is not indexed, or nobody holds it alongside another."))
		return
	}
	together := 0
	for _, r := range rel {
		if r.Together > 0 {
			together++
		}
	}
	fmt.Fprintln(w, s.Bold(fmt.Sprintf(
		"Related to %s  %d topic%s, %d of them worked on alongside it",
		EscapeControls(topic), len(rel), Plural(len(rel)), together)))
	width := 0
	for _, r := range rel {
		if n := len([]rune(r.Topic)); n > width {
			width = n
		}
	}
	if width > 24 {
		width = 24
	}
	for _, r := range rel {
		// Not narrower means held by about as many people, which is a subject
		// of comparable size. It does not mean the two are the same subject,
		// which is what this said before and was plainly wrong on the page:
		// kafka-lag is not billing-retries.
		kind := "peer subject"
		if r.Narrower {
			kind = "specialty"
		}
		// Which of the two kinds of evidence is speaking matters: subjects
		// changed together are related whoever changed them, while subjects
		// with the same experts may only share a person.
		strength := fmt.Sprintf("%3.0f%% shared", r.Overlap*100)
		if r.Together > 0 {
			strength = fmt.Sprintf("%3.0f%% together", r.Together*100)
		}
		why := kind + ", " + EscapeControls(r.Because)
		// A connection only one person has ever made is worth saying out loud:
		// both subjects may be well covered while the fact that they belong
		// together has nobody but them.
		if r.Spanned == 1 && r.SoleName != "" {
			why = kind + ", only " + EscapeControls(r.SoleName) + " has worked across both"
		}
		rTopic := EscapeControls(r.Topic)
		fmt.Fprintf(w, "  %s  %s  %s\n",
			pad(s.Bold(rTopic), rTopic, width),
			s.Accent(strength),
			s.Dim(why))
	}
}

// RenderSpans lists the connections between subjects that rest on one person.
// Counting experts per subject cannot find these: both areas can be well
// covered while the work crossing between them has only ever been done once, by
// one person, and what rests on them is the knowledge that the two belong
// together at all.
func RenderSpans(w io.Writer, spans []resolve.Span, s Style) {
	if len(spans) == 0 {
		return
	}
	fmt.Fprintln(w)
	fmt.Fprintln(w, s.Bold(fmt.Sprintf(
		"One-person connections  %d %s only one person has done focused work across",
		len(spans), Plural2(len(spans), "crossing", "crossings"))))
	fmt.Fprintf(w, "  %s\n\n", s.Dim(
		"The subjects have their own experts. The links between them do not."))
	for _, sp := range spans {
		// Two subjects is one crossing and reads as a pair. More than two means
		// the crossings join up and the whole group rests on one person, which
		// is the heavier finding and is said as one.
		fmt.Fprintf(w, "  %s  %s\n",
			s.Bold(strings.Join(EscapeControlsList(sp.Topics), s.Dim(" + "))),
			s.Dim(fmt.Sprintf("only %s, across %d people holding %s",
				EscapeControls(sp.Person), sp.Experts,
				Plural2(sp.Size(), "the two", "them"))))
	}
}

// RenderUnlinked prints the declared owners with nothing recorded against them.
// It is a worklist rather than a finding: a source of record names people by
// handle and an activity source names them by address, so an owner whose two
// were never tied together is indistinguishable from one who has left, and
// every area they own reads as drifted until somebody says which is which.
func RenderUnlinked(w io.Writer, view UnlinkedView, s Style) {
	if view.Declared == 0 {
		fmt.Fprintln(w, s.Dim(
			"No declared ownership indexed. Add a CODEOWNERS source to compare it against the work."))
		return
	}
	if view.Unlinked == 0 {
		fmt.Fprintf(w, "%s\n", s.Dim(fmt.Sprintf(
			"All %d declared owners have work recorded against them.", view.Declared)))
		return
	}
	fmt.Fprintln(w, s.Bold(fmt.Sprintf(
		"Unlinked owners  %d of %d have no work recorded against them",
		view.Unlinked, view.Declared)))
	fmt.Fprintf(w, "  %s\n\n", s.Dim(
		"Each may have left, or may simply commit under an address this handle was never tied to."))
	width := 0
	for _, o := range view.Owners {
		if n := len([]rune(o.ID)); n > width {
			width = n
		}
	}
	if width > 30 {
		width = 30
	}
	for _, o := range view.Owners {
		owns := o.Owns
		const most = 5
		tail := ""
		if len(owns) > most {
			tail = fmt.Sprintf(" and %d more", len(owns)-most)
			owns = owns[:most]
		}
		id := EscapeControls(o.ID)
		fmt.Fprintf(w, "  %s  %s%s\n",
			pad(s.Bold(id), id, width),
			strings.Join(EscapeControlsList(owns), ", "), s.Dim(tail))
	}
	fmt.Fprintf(w, "\n  %s\n", s.Dim(
		"Tie one to its person by adding \"person@example.com\": [\"the-handle\"] to the alias file, then re-index."))
}

// RenderRegions lists the connected bodies of work that rest on one person.
// This is a different finding from a concentrated subject and a worse one: ten
// unrelated subjects held by one person are ten small risks, while ten that are
// changed together and led by the same person are one large one, because
// whoever picks the work up has to learn the whole region at once.
func RenderRegions(w io.Writer, regions []resolve.Region, s Style) {
	if len(regions) == 0 {
		return
	}
	fmt.Fprintln(w)
	fmt.Fprintln(w, s.Bold(fmt.Sprintf(
		"Joined work  %d %s where subjects worked on together all rest on one person",
		len(regions), Plural2(len(regions), "body", "bodies"))))
	for _, r := range regions {
		shown := r.Topics
		const most = 6
		tail := ""
		if len(shown) > most {
			tail = fmt.Sprintf(" and %d more", len(shown)-most)
			shown = shown[:most]
		}
		fmt.Fprintf(w, "  %s  %s\n", s.Bold(EscapeControls(r.Lead)),
			s.Dim(fmt.Sprintf("%d joined subjects", r.Size())))
		fmt.Fprintf(w, "      %s%s\n", strings.Join(EscapeControlsList(shown), ", "), s.Dim(tail))
	}
}

// Plural2 picks between two spellings of a word.
func Plural2(n int, one, many string) string {
	if n == 1 {
		return one
	}
	return many
}

// RenderOwnership prints where declared ownership has drifted from the person
// who actually does the work.
func RenderOwnership(w io.Writer, report resolve.OwnershipReport, s Style) {
	drift := report.Drift
	if report.Declared == 0 {
		fmt.Fprintln(w, s.Dim(
			"No declared ownership indexed. Add a CODEOWNERS source to compare it against the work."))
		return
	}
	if len(drift) == 0 {
		fmt.Fprintln(w, s.Dim(fmt.Sprintf(
			"All %d declared area%s is led by its owner of record.", report.Declared, Plural(report.Declared))))
		return
	}
	// The share is the finding. A list on its own reads as a handful of
	// exceptions, and it never is one.
	fmt.Fprintln(w, s.Bold(fmt.Sprintf(
		"Ownership drift  %d of %d judged areas (%.0f%%) are not led by their owner of record",
		len(drift), report.Declared-report.GroupOwned, 100*report.Share())))
	if report.GroupOwned > 0 {
		fmt.Fprintf(w, "  %s\n", s.Dim(fmt.Sprintf(
			"%d more area%s owned only by teams or automation, which drift cannot judge",
			report.GroupOwned, Plural(report.GroupOwned))))
	}
	// The three are different problems: somebody who has left, somebody who
	// owns an area on paper only, and somebody merely out-worked in their own
	// area. Only the last is a judgement call.
	fmt.Fprintf(w, "  %s\n\n", s.Dim(fmt.Sprintf(
		"%d with no recorded work at all, %d who work elsewhere but never here, %d out-worked in their own area",
		report.Silent, report.Unworked, report.Trailing)))
	width := 0
	for _, d := range drift {
		if n := len([]rune(d.Topic)); n > width {
			width = n
		}
	}
	if width > 24 {
		width = 24
	}
	for _, d := range drift {
		topic := EscapeControls(d.Topic)
		fmt.Fprintf(w, "  %s  %s %s  %s %s\n",
			pad(s.Bold(topic), topic, width),
			s.Dim("declared"), s.Warn(strings.Join(EscapeControlsList(d.Declared), ", ")),
			s.Dim("→ actual"), s.Accent(EscapeControls(d.Actual)))
	}
}

// RenderEval prints a measurement: the score first, then the numbers that say
// how much to trust it.
func RenderEval(w io.Writer, r eval.Result, s Style) {
	if r.Declared == 0 {
		fmt.Fprintln(w, s.Dim(
			"Nothing declares ownership in this index, so there is nothing to score against."))
		fmt.Fprintln(w, s.Dim(
			"Add a CODEOWNERS source, or a source of record that assigns areas to people."))
	} else {
		// Ranked leads because it is the number a change to ranking moves.
		// Agreement is mostly a statement about how much of the organization
		// was indexed, and reading it as a score has misled every time.
		fmt.Fprintln(w, s.Bold(fmt.Sprintf(
			"Ranked     %.1f%%   %d of %d areas where the declared owner has work here at all",
			100*r.Ranked, r.Held, r.Answerable)))
		fmt.Fprintln(w, s.Dim(fmt.Sprintf(
			"Agreement  %.1f%%   %d of %d declared areas, counting owners who never appear in what was indexed",
			100*r.Agreement, r.Held, r.Declared)))
		// The three buckets are three different problems, and only the last is
		// a judgement call about ranking.
		fmt.Fprintf(w, "  %s\n", s.Dim(fmt.Sprintf(
			"of the %d that disagree: %d have no recorded work at all, %d work elsewhere but never here, %d are out-worked in their own area",
			r.Declared-r.Held, r.Silent, r.Unworked, r.Trailing)))
	}

	fmt.Fprintf(w, "\n%s\n", s.Bold("Identity"))
	fmt.Fprintf(w, "  %d records resolve to %d people, %d of them by inference\n", r.Records, r.People, r.Joins)
	if r.Unlinked > 0 {
		// This is the ceiling on the score above, not a footnote: work under an
		// unjoined handle is attributed to nobody, and its owner cannot agree.
		fmt.Fprintf(w, "  %s\n", s.Warn(fmt.Sprintf(
			"%d handles never found a person, so their work counts for no one", r.Unlinked)))
	}

	fmt.Fprintf(w, "\n%s\n", s.Bold("Coverage"))
	fmt.Fprintf(w, "  %d subjects, %d of them real; %d connections between them\n", r.Topics, r.Salient, r.Ties)
	fmt.Fprintf(w, "  %d connection%s rest on one person, in %d bod%s of work led by one person\n",
		r.Spans, Plural(r.Spans), r.Regions, map[bool]string{true: "y", false: "ies"}[r.Regions == 1])
	if len(r.Sources) > 0 {
		fmt.Fprintf(w, "  %s\n", s.Dim("from "+strings.Join(r.Sources, ", ")))
	}
}

// RenderEvalChanges prints what moved since a saved measurement, regressions
// first. A change that bought one number by quietly spending another is the
// thing this exists to catch, so nothing is summarized away.
func RenderEvalChanges(w io.Writer, changes []eval.Change, comparable bool, s Style) {
	fmt.Fprintf(w, "\n%s\n", s.Bold("Against the baseline"))
	if !comparable {
		fmt.Fprintf(w, "  %s\n", s.Warn(
			"Different sources were indexed, so these two runs are not comparable."))
		return
	}
	if len(changes) == 0 {
		fmt.Fprintf(w, "  %s\n", s.Dim("Nothing moved."))
		return
	}
	for _, c := range changes {
		mark, paint := "·", s.Dim
		if c.Scored {
			if c.Better {
				mark, paint = "+", s.Accent
			} else {
				mark, paint = "-", s.Bad
			}
		}
		delta := c.Delta()
		sign := "+"
		if delta < 0 {
			sign = ""
		}
		var shown string
		// Both scores are rates between zero and one. Printing one of them as a
		// count rounded it to "1 -> 1 (+0)" and hid a real movement entirely.
		if c.Name == "agreement" || c.Name == "ranked" {
			shown = fmt.Sprintf("%.1f%% -> %.1f%% (%s%.1f)", 100*c.Before, 100*c.After, sign, 100*delta)
		} else {
			shown = fmt.Sprintf("%.0f -> %.0f (%s%.0f)", c.Before, c.After, sign, delta)
		}
		fmt.Fprintf(w, "  %s %-10s %s\n", paint(mark), c.Name, shown)
	}
}

// RenderEvalHead prints the comparison over the areas both runs could answer,
// which is the one that survives a difference in coverage. It leads, because
// the overall scores below it move for reasons that are not about quality.
func RenderEvalHead(w io.Writer, h eval.Head, ok bool, s Style) {
	fmt.Fprintf(w, "\n%s\n", s.Bold("On the same questions"))
	if !ok {
		fmt.Fprintf(w, "  %s\n", s.Dim(
			"The baseline recorded no per-area verdicts, so only the totals below can be compared."))
		return
	}
	delta := h.AfterHeld - h.BeforeHeld
	sign, paint := "+", s.Accent
	switch {
	case delta < 0:
		sign, paint = "", s.Bad
	case delta == 0:
		paint = s.Dim
	}
	fmt.Fprintf(w, "  %s   %d of %d correct, was %d (%s%d)\n",
		paint(fmt.Sprintf("%.1f%%", 100*h.Rate())), h.AfterHeld, h.Areas, h.BeforeHeld,
		sign, delta)
	fmt.Fprintf(w, "  %s\n", s.Dim(fmt.Sprintf("was %.1f%% over the same %d areas", 100*h.Was(), h.Areas)))
	// Which areas moved matters more than the count. A change that wins ten and
	// loses ten has not improved anything, and the total alone hides it.
	if len(h.Won) > 0 {
		fmt.Fprintf(w, "  %s %s\n", s.Accent("won "), evalSample(h.Won))
	}
	if len(h.Lost) > 0 {
		fmt.Fprintf(w, "  %s %s\n", s.Bad("lost"), evalSample(h.Lost))
	}
}

// evalSample names a few areas and says how many more there were, so a long
// list stays readable without pretending it was short.
func evalSample(areas []string) string {
	const show = 6
	areas = EscapeControlsList(areas)
	if len(areas) <= show {
		return strings.Join(areas, ", ")
	}
	return fmt.Sprintf("%s and %d more", strings.Join(areas[:show], ", "), len(areas)-show)
}
