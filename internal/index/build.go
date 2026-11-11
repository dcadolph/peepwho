// Building the index: taking records in, folding increments, and turning one
// record into people, channels, subjects, and ties.

package index

import (
	"maps"
	"slices"
	"sort"
	"strings"

	"github.com/dcadolph/whotoask/internal/connector"
	"github.com/dcadolph/whotoask/internal/model"
	"github.com/dcadolph/whotoask/internal/text"
	"github.com/dcadolph/whotoask/internal/util"
)

// Build replaces the index contents with data derived from records, forgetting
// every source read before.
func (ix *Index) Build(records []connector.Record) {
	ix.sources = make(map[string][]connector.Record)
	ix.sourceCounts = make(map[string]int)
	ix.personVecs = make(map[model.ID][]float32)
	ix.channelVecs = make(map[model.ID][]float32)
	ix.topicVecs = make(map[model.ID][]float32)
	ix.take(scrubRecords(records))
	ix.rebuild()
}

// Add merges records into the current index. Each source named in records
// replaces whatever that source contributed before, so re-reading a source is
// idempotent: indexing Slack twice leaves the same index as indexing it once,
// and a scheduled merge does not inflate its own weights over time. Sources
// not named are left as they are. Person records merge by email or id, channel
// records by name. Embeddings are left alone; call Embed to refresh vectors.
func (ix *Index) Add(records []connector.Record) {
	ix.take(scrubRecords(records))
	ix.rebuild()
}

// scrubRecords strips credential-shaped substrings from every prose field
// before anything downstream sees it: the postings, the stored source
// records, and every answer derive from the scrubbed copy. People paste keys
// into chat and tickets, and a who-knows-what index must never double as a
// where-the-secrets-are index.
func scrubRecords(records []connector.Record) []connector.Record {
	out := make([]connector.Record, len(records))
	for i, rec := range records {
		rec.Text, _ = text.Scrub(rec.Text)
		rec.Title, _ = text.Scrub(rec.Title)
		out[i] = rec
	}
	return out
}

// take files records under the source that produced them, replacing that
// source's previous contribution. Records that name no source cannot be told
// apart from the ones already held, so they accumulate instead: every
// connector names itself, and refusing to guess keeps a hand-assembled record
// set from silently erasing another.
func (ix *Index) take(records []connector.Record) {
	if ix.sources == nil {
		ix.sources = make(map[string][]connector.Record)
	}
	incoming := make(map[string][]connector.Record)
	for _, rec := range records {
		incoming[rec.Source] = append(incoming[rec.Source], rec)
	}
	for name, recs := range incoming {
		if name == "" {
			ix.sources[name] = append(ix.sources[name], recs...)
			continue
		}
		ix.sources[name] = recs
	}
	if ix.sourceCounts == nil {
		ix.sourceCounts = make(map[string]int)
	}
	for name := range incoming {
		ix.sourceCounts[name] = len(ix.sources[name])
	}
}

// redactedSources returns a copy of the stored records with readable Text
// replaced by its stemmed Terms, so a saved index holds a search index rather
// than the messages themselves. A record already carrying Terms and no Text,
// which is one read back from a saved index, is passed through untouched.
func redactedSources(sources map[string][]connector.Record) map[string][]connector.Record {
	if sources == nil {
		return nil
	}
	out := make(map[string][]connector.Record, len(sources))
	for name, recs := range sources {
		clean := make([]connector.Record, len(recs))
		for i, rec := range recs {
			clean[i] = redactRecord(rec)
		}
		out[name] = clean
	}
	return out
}

// redactRecord replaces a record's readable Text with its sorted stemmed Terms,
// so a stored or merged record holds a search index rather than the message
// text. A record already carrying only Terms is returned unchanged.
func redactRecord(rec connector.Record) connector.Record {
	if rec.Text != "" {
		// Sort the stemmed terms so word order is destroyed: the stored set
		// reveals no more than the inverted index already does, and cannot be
		// read back as a sentence.
		terms := text.Terms(rec.Text)
		sort.Strings(terms)
		rec.Terms = terms
		rec.Text = ""
	}
	return rec
}

// MergeIncremental folds records from an incremental fetch into a source's
// existing records instead of replacing them, so a fetch that returns only what
// changed since the last run updates the graph without dropping the people and
// topics it did not re-read. A record for an identity already held is summed
// into it; an identity not in the fetch is left untouched. Call LoadSources
// first so the prior records are present to fold into. Both the held and the
// incoming records are reduced to stemmed Terms, so no readable message text is
// kept, and the folded set stays one record per identity, bounded across runs.
//
// Folding matches what a full read produces: a connector's full run already
// emits one record per person carrying that person's whole activity at their
// most recent time, which is exactly what summing the batches and taking the
// latest time yields here. An item edited after the watermark is counted in both
// the held record and the delta; the double count is bounded harmless because
// topic and term weight saturate, and a periodic full re-index recompacts.
func (ix *Index) MergeIncremental(records []connector.Record) {
	if ix.sources == nil {
		ix.sources = make(map[string][]connector.Record)
	}
	incoming := make(map[string][]connector.Record)
	for _, rec := range scrubRecords(records) {
		incoming[rec.Source] = append(incoming[rec.Source], redactRecord(rec))
	}
	if ix.sourceCounts == nil {
		ix.sourceCounts = make(map[string]int)
	}
	for name, recs := range incoming {
		if name == "" {
			// A record that names no source cannot be matched to prior held
			// records, so it accumulates rather than risk erasing another set,
			// exactly as take treats the same case.
			ix.sources[name] = append(ix.sources[name], recs...)
		} else {
			ix.sources[name] = foldRecords(redactSlice(ix.sources[name]), recs)
		}
		ix.sourceCounts[name] = len(ix.sources[name])
	}
	ix.rebuild()
}

// redactSlice reduces every record in recs to stemmed Terms, so a held set
// loaded with readable Text still folds without keeping that text.
func redactSlice(recs []connector.Record) []connector.Record {
	out := make([]connector.Record, len(recs))
	for i, rec := range recs {
		out[i] = redactRecord(rec)
	}
	return out
}

// foldRecords folds delta records into base by identity, summing the affinity of
// an identity already present and appending a genuinely new one. It keeps one
// record per identity so a source stays bounded across incremental runs.
func foldRecords(base, delta []connector.Record) []connector.Record {
	at := make(map[string]int, len(base))
	out := make([]connector.Record, len(base))
	copy(out, base)
	for i, rec := range out {
		at[foldKey(rec)] = i
	}
	for _, rec := range delta {
		key := foldKey(rec)
		if i, ok := at[key]; ok {
			out[i] = foldRecord(out[i], rec)
			continue
		}
		at[key] = len(out)
		out = append(out, rec)
	}
	return out
}

// foldLinks merges two sets of subject ties, keeping the strongest claim for
// each. Ties are not counts and must not accumulate: how much of the time two
// subjects move together is already a share, and adding two shares together
// would climb past one on nothing more than being re-read.
func foldLinks(base, add []connector.TopicLink) []connector.TopicLink {
	if len(add) == 0 {
		return base
	}
	at := make(map[string]int, len(base)+len(add))
	for i, l := range base {
		at[l.To] = i
	}
	for _, l := range add {
		if i, ok := at[l.To]; ok {
			if l.Weight > base[i].Weight {
				// The whole claim moves together. Keeping the stronger weight
				// beside an older witness count would describe a tie nobody
				// observed.
				base[i] = l
			}
			continue
		}
		at[l.To] = len(base)
		base = append(base, l)
	}
	return base
}

// foldKey identifies the record a later record accumulates into: a person by
// their per-source id, a channel by its slug.
func foldKey(rec connector.Record) string {
	switch rec.Kind {
	case connector.KindChannel:
		return "c\x00" + slug(rec.Name)
	case connector.KindTopic:
		// A subject is keyed as a subject. Folded as a person it would key on
		// the same name slug people do, so a subject called billing and a
		// person whose identity resolves to billing would accumulate into each
		// other.
		return "t\x00" + slug(rec.Name)
	default:
		return "p\x00" + string(personID(rec))
	}
}

// foldRecord sums add into base: it concatenates the topic and term lists that
// rebuild turns into weight, unions channel members, advances the time to the
// most recent activity, and fills any identity field base is missing.
func foldRecord(base, add connector.Record) connector.Record {
	base.Topics = append(base.Topics, add.Topics...)
	base.RecentTopics = append(base.RecentTopics, add.RecentTopics...)
	base.Terms = append(base.Terms, add.Terms...)
	base.Links = foldLinks(base.Links, add.Links)
	for _, m := range add.Members {
		if !slices.Contains(base.Members, m) {
			base.Members = append(base.Members, m)
		}
	}
	for _, a := range add.AltIDs {
		if !slices.Contains(base.AltIDs, a) {
			base.AltIDs = append(base.AltIDs, a)
		}
	}
	if add.Time.After(base.Time) {
		base.Time = add.Time
	}
	if base.Link == "" {
		base.Link = add.Link
	}
	// A newer listing of the same account says what it is now: an account
	// deactivated since the last run is deactivated, not joined with its past.
	if add.Account != model.AccountUnknown {
		base.Account = add.Account
	}
	// Keep the better display name rather than always taking the delta's, so an
	// incremental record carrying a handle placeholder never overwrites a real
	// name already held, matching how a full read resolves identity.
	if betterName(base.Name, add.Name) {
		base.Name = add.Name
	}
	base.Email = firstNonEmpty(base.Email, add.Email)
	base.Title = firstNonEmpty(add.Title, base.Title)
	base.Team = firstNonEmpty(add.Team, base.Team)
	base.Org = firstNonEmpty(add.Org, base.Org)
	base.Manager = firstNonEmpty(add.Manager, base.Manager)
	base.PersonID = firstNonEmpty(base.PersonID, add.PersonID)
	if add.Weight > base.Weight {
		base.Weight = add.Weight
	}
	return base
}

// firstNonEmpty returns a when it is non-empty, otherwise b.
func firstNonEmpty(a, b string) string {
	if a != "" {
		return a
	}
	return b
}

// rebuild derives the graph, postings, and texts from every retained record.
// Sources are replayed in name order so the same set of records always
// produces the same index, whatever order the runs happened in.
func (ix *Index) rebuild() {
	ix.Graph = model.NewGraph()
	ix.postings = make(map[string]map[model.ID]float64)
	ix.texts = make(map[model.ID]*personText)
	ix.channelPostings = make(map[string]map[model.ID]float64)
	ix.channelTexts = make(map[model.ID]*channelText)
	for _, name := range slices.Sorted(maps.Keys(ix.sources)) {
		for _, rec := range ix.sources[name] {
			switch rec.Kind {
			case connector.KindChannel:
				ix.buildChannel(rec)
			case connector.KindTopic:
				ix.buildTopicLinks(rec)
			default:
				ix.buildPerson(rec)
			}
		}
	}
	ix.refreshStats()
}

// buildPerson merges one person record into the graph and postings.
func (ix *Index) buildPerson(rec connector.Record) {
	g, postings, texts, r := ix.Graph, ix.postings, ix.texts, ix.identityResolver()
	raw := personID(rec)
	if raw == "" {
		return
	}
	if rec.PersonID != "" && rec.Email != "" && !util.IsRoleEmail(rec.Email) {
		r.Union(model.ID(util.NormalizeEmail(rec.Email)), model.ID(strings.ToLower(strings.TrimSpace(rec.PersonID))))
	}
	for _, alt := range rec.AltIDs {
		if key := identityKey(alt); key != "" && model.ID(key) != raw {
			r.Union(model.ID(key), raw)
		}
	}
	pid := r.Canonical(raw)
	w := rec.Weight
	// Zero means the source never said, so it counts as one observation. A
	// negative weight is corrupt input, not a small weight: written through, it
	// produces negative postings and topic affinities, which nothing in the
	// ranking rules is defined over, so it is refused and floored the same way.
	if w <= 0 {
		w = 1
	}
	// base survives without the recency discount. The discount dates a person's
	// whole record by their newest commit anywhere, which is right for "who do
	// I ask" and wrong for "whose area is this": an author quiet for nine
	// months everywhere had six changes inside an area counted as three, and
	// lost it to a passer-by with two fresh ones. Ownership is judged flat over
	// the indexed window; the window itself is the recency bound.
	base := w
	w *= ix.decay(rec.Time)
	p := g.People[pid]
	if p == nil {
		p = &model.Person{ID: pid, Topics: make(map[model.ID]float64)}
		g.People[pid] = p
	}
	if raw != pid && !slices.Contains(p.Identities, raw) {
		p.Identities = append(p.Identities, raw)
	}
	fillIdentity(p, rec)
	if rec.Link != "" {
		addLinks(p, rec.Link)
	}
	if p.ManagerID != "" {
		p.ManagerID = r.Canonical(p.ManagerID)
	}
	linkOrg(g, p, rec)

	pt := texts[pid]
	if pt == nil {
		pt = &personText{}
		texts[pid] = pt
	}
	addKey := func(key string, fieldWeight float64) {
		if postings[key] == nil {
			postings[key] = make(map[model.ID]float64)
		}
		postings[key][pid] += fieldWeight * w
	}
	add := func(text string, fieldWeight float64) {
		for _, tok := range tokenize(text) {
			addKey(stem(tok), fieldWeight)
		}
	}
	if rec.Title != "" {
		pt.Titles = appendUnique(pt.Titles, strings.ToLower(rec.Title))
		add(rec.Title, weightTitle)
	}
	if rec.Team != "" {
		pt.Teams = appendUnique(pt.Teams, strings.ToLower(rec.Team))
		add(rec.Team, weightTeam)
	}
	addTopic := func(top string, curated bool) {
		tid := topicID(top)
		if tid == "" {
			return
		}
		noteTopic(g, tid, top, rec.Source, curated)
		p.Topics[tid] += weightTopic * w
		if statesOwnership(rec.Source) {
			if p.Stated == nil {
				p.Stated = make(map[model.ID]float64)
			}
			p.Stated[tid] += weightTopic * w
		}
		pt.Topics = append(pt.Topics, strings.ToLower(top))
		add(top, weightTopic)
		if curated && rec.Source == "codeowners" && !slices.Contains(p.Owns, tid) {
			p.Owns = append(p.Owns, tid)
		}
	}
	for _, top := range rec.Topics {
		addTopic(top, true)
	}
	for _, top := range rec.WeakTopics {
		addTopic(top, false)
	}
	// What they have worked on lately, which is a subset of what they know.
	// Kept apart so a subject somebody knows best but has stopped touching can
	// be told from one they are still in.
	for _, top := range rec.RecentTopics {
		tid := topicID(top)
		if tid == "" {
			continue
		}
		if p.Recent == nil {
			p.Recent = make(map[model.ID]float64)
		}
		p.Recent[tid] += weightTopic * w
	}
	// Work done inside the area itself, as against a file elsewhere carrying
	// its name. Ownership reads this; search reads the whole of Topics, because
	// somebody who touched voip/assist_satellite.py really has met the subject.
	for _, top := range rec.DirectTopics {
		tid := topicID(top)
		if tid == "" {
			continue
		}
		if p.Direct == nil {
			p.Direct = make(map[model.ID]float64)
		}
		p.Direct[tid] += weightTopic * base
	}
	// Fresh ingest carries readable Text, kept in memory for embedding and
	// merge and tokenized into postings. A record rebuilt from a saved index
	// carries only the stemmed Terms, which reproduce the same postings without
	// any readable text having touched disk.
	if rec.Text != "" {
		pt.Text = strings.TrimSpace(pt.Text + " " + strings.ToLower(rec.Text))
		add(rec.Text, weightText)
	} else {
		for _, term := range rec.Terms {
			addKey(term, weightText)
		}
	}
}

// addLinks adds each link to the person's deep links once, keeping them sorted.
func addLinks(p *model.Person, links ...string) {
	for _, l := range links {
		if l != "" && !slices.Contains(p.Links, l) {
			p.Links = append(p.Links, l)
		}
	}
	slices.Sort(p.Links)
}

// buildChannel merges one channel record into the graph and channel postings.
func (ix *Index) buildChannel(rec connector.Record) {
	g, postings, texts, r := ix.Graph, ix.channelPostings, ix.channelTexts, ix.identityResolver()
	cid := model.ID(slug(rec.Name))
	if cid == "" {
		return
	}
	d := ix.decay(rec.Time)
	ch := g.Channels[cid]
	if ch == nil {
		ch = &model.Channel{ID: cid, Name: rec.Name, Topics: make(map[model.ID]float64)}
		g.Channels[cid] = ch
	}
	if rec.Link != "" && ch.URL == "" {
		ch.URL = rec.Link
	}
	if rec.Title != "" {
		ch.Topic = rec.Title
	}
	for _, m := range rec.Members {
		mid := r.Canonical(model.ID(normalizeRef(m)))
		if !slices.Contains(ch.Members, mid) {
			ch.Members = append(ch.Members, mid)
		}
	}

	ct := texts[cid]
	if ct == nil {
		ct = &channelText{Name: strings.ToLower(rec.Name)}
		texts[cid] = ct
	}
	if rec.Title != "" {
		ct.Topic = strings.ToLower(rec.Title)
	}
	addKey := func(key string, fieldWeight float64) {
		if postings[key] == nil {
			postings[key] = make(map[model.ID]float64)
		}
		postings[key][cid] += fieldWeight * d
	}
	add := func(text string, fieldWeight float64) {
		for _, tok := range tokenize(text) {
			addKey(stem(tok), fieldWeight)
		}
	}
	add(rec.Name, weightChannelName)
	if rec.Title != "" {
		add(rec.Title, weightTopic)
	}
	addTopic := func(top string, curated bool) {
		tid := topicID(top)
		if tid == "" {
			return
		}
		noteTopic(g, tid, top, rec.Source, curated)
		ch.Topics[tid] += weightTopic * d
		ct.Topics = append(ct.Topics, strings.ToLower(top))
		add(top, weightTopic)
	}
	for _, top := range rec.Topics {
		addTopic(top, true)
	}
	for _, top := range rec.WeakTopics {
		addTopic(top, false)
	}
	// Fresh ingest tokenizes readable Text; a rebuilt record replays its
	// stored stemmed Terms, so a channel's sampled chatter never reaches disk.
	if rec.Text != "" {
		add(rec.Text, weightText)
	} else {
		for _, term := range rec.Terms {
			addKey(term, weightText)
		}
	}
}

// personID resolves a stable identifier for a record, preferring an explicit
// id, then email, then a slug of the name.
// identityKey normalizes an alternate identifier for the resolver: an email is
// folded like any other email, a role mailbox is dropped, and anything else is
// lowercased and trimmed.
func identityKey(s string) string {
	s = strings.TrimSpace(s)
	if s == "" {
		return ""
	}
	if strings.Contains(s, "@") {
		if util.IsRoleEmail(s) {
			return ""
		}
		return util.NormalizeEmail(s)
	}
	return strings.ToLower(s)
}

func personID(rec connector.Record) model.ID {
	switch {
	case rec.PersonID != "":
		return model.ID(strings.ToLower(strings.TrimSpace(rec.PersonID)))
	case rec.Email != "":
		return model.ID(util.NormalizeEmail(rec.Email))
	case rec.Name != "":
		return model.ID(slug(rec.Name))
	default:
		return ""
	}
}

// topicID returns the canonical identifier for a topic name, folding synonyms
// and abbreviations onto one topic so the same concept is not split across forms.
func topicID(name string) model.ID {
	return model.ID(canonicalTopic(name))
}

// betterName reports whether name should replace current. A handle-like
// placeholder ("@login", "jira:accountid") never replaces a real name.
func betterName(current, name string) bool {
	if name == "" {
		return false
	}
	if current == "" {
		return true
	}
	return !strings.HasPrefix(name, "@") && !strings.Contains(name, ":")
}

// fillIdentity copies non-empty identity fields from rec onto p.
func fillIdentity(p *model.Person, rec connector.Record) {
	if betterName(p.Name, rec.Name) {
		p.Name = rec.Name
	}
	if rec.Email != "" {
		p.Email = rec.Email
	}
	if rec.Title != "" {
		p.Title = rec.Title
	}
	if rec.Manager != "" {
		p.ManagerID = model.ID(normalizeRef(rec.Manager))
	}
	p.Account = p.Account.Join(rec.Account)
}

// normalizeRef folds a reference to a person found inside another record, such
// as a channel member or a manager, the same way person identities are keyed:
// an email is normalized as an email, anything else is lowercased. Without the
// email fold, a member listed as alice+ci@x.com never resolves to the person
// keyed alice@x.com, so they silently drop out of membership, and a manager
// written with a plus tag dangles. Unlike identityKey, a role mailbox is kept,
// because a reference silently vanishing from a member list is data loss.
func normalizeRef(s string) string {
	s = strings.TrimSpace(s)
	if strings.Contains(s, "@") {
		return util.NormalizeEmail(s)
	}
	return strings.ToLower(s)
}

// statesOwnership reports whether a source assigns subjects to people by
// declaration rather than by evidence of work. A CODEOWNERS file and an org
// chart's topics column both say who is responsible for an area; neither says
// anybody has touched it.
func statesOwnership(source string) bool {
	return source == "codeowners" || source == "org-csv"
}

// buildTopicLinks records which subjects a source saw worked on together. The
// subject itself is not established by this: being changed alongside something
// is not the same as somebody declaring it, so a subject that appears only here
// stays unsalient until a person holds it.
func (ix *Index) buildTopicLinks(rec connector.Record) {
	tid := topicID(rec.Name)
	if tid == "" || len(rec.Links) == 0 {
		return
	}
	g := ix.Graph
	topic := g.Topics[tid]
	if topic == nil {
		topic = &model.Topic{ID: tid, Name: strings.ToLower(rec.Name)}
		g.Topics[tid] = topic
	}
	if topic.Near == nil {
		topic.Near = make(map[model.ID]model.Tie)
	}
	for _, link := range rec.Links {
		to := topicID(link.To)
		if to == "" || to == tid || link.Weight <= 0 {
			continue
		}
		// Sources disagree about how strongly two subjects are tied; keep the
		// strongest claim rather than letting a weak one dilute it.
		if link.Weight > topic.Near[to].Weight {
			// A source names the person by whatever address the work carried,
			// which is not always how the graph keys them: a provider's
			// no-reply address encodes a login the connector keys people by.
			// Normalized here, the tie points at somebody the graph can name.
			sole := model.ID("")
			if link.Sole != "" {
				sole = model.ID(util.NormalizeEmail(link.Sole))
			}
			topic.Near[to] = model.Tie{
				Weight:    link.Weight,
				Witnesses: link.Witnesses,
				Sole:      sole,
			}
		}
	}
}

// linkOrg attaches the person to their team and organization, creating those
// entities in the graph when first seen.
func linkOrg(g *model.Graph, p *model.Person, rec connector.Record) {
	var orgID model.ID
	if rec.Org != "" {
		orgID = model.ID(slug(rec.Org))
		if g.Orgs[orgID] == nil {
			g.Orgs[orgID] = &model.Org{ID: orgID, Name: rec.Org}
		}
		p.OrgID = orgID
	}
	if rec.Team != "" {
		teamID := model.ID(slug(rec.Team))
		if g.Teams[teamID] == nil {
			g.Teams[teamID] = &model.Team{ID: teamID, Name: rec.Team}
		}
		if orgID != "" {
			g.Teams[teamID].OrgID = orgID
		}
		p.TeamID = teamID
	}
}
