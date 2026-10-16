package generator

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"GoT0GenPaper/internal/llm"
	"GoT0GenPaper/internal/models"
)

func TestArticleDriftAllowsBlanksAndRemovals(t *testing.T) {
	article := "City councils across Europe are turning empty offices into apartments. " +
		"The change is slow because conversions require new plumbing and fire safety work. " +
		"Tenants say the results are worth the wait, especially near public transport."

	// Cloze: replacing words with numbered blanks is the allowed edit.
	cloze := strings.Replace(article, "empty offices", "[1]", 1)
	cloze = strings.Replace(cloze, "slow", "[2]", 1)
	if drifted := articleDrift(article, cloze); len(drifted) > 0 {
		t.Fatalf("blank insertion reported as drift: %v", drifted)
	}

	// Part B: dropping a whole sentence is allowed too.
	partB := strings.Replace(article, "The change is slow because conversions require new plumbing and fire safety work. ", "", 1)
	if drifted := articleDrift(article, partB); len(drifted) > 0 {
		t.Fatalf("whole-sentence removal reported as drift: %v", drifted)
	}

	// Rewriting the prose is exactly what must be rejected.
	rewritten := strings.Replace(article, "The change is slow because conversions require new plumbing and fire safety work.",
		"Officials admit progress has stalled for years and residents are frustrated.", 1)
	if drifted := articleDrift(article, rewritten); len(drifted) == 0 {
		t.Fatal("rewritten sentence was accepted as article-derived")
	}
}

func TestArticleValidationRequiresUsableLength(t *testing.T) {
	request := englishArticleRequest{section: "阅读理解 Part A", minWords: 280, maxWords: 420}
	if issue := validateArticle("Too short.", request); issue == "" {
		t.Fatal("short article accepted")
	}
	long := strings.Repeat("Careful observation and steady practice improve outcomes over time. ", 40)
	if issue := validateArticle(long, request); issue != "" {
		t.Fatalf("usable article rejected: %s", issue)
	}
	if issue := validateArticle(long+" [1]", request); issue == "" {
		t.Fatal("article carrying blank markers was accepted")
	}
}

func TestArticleSourceKindFallsBackToWriting(t *testing.T) {
	// No search endpoint configured: the article is written, even when the mode
	// asks for websearch, so an offline machine can still produce a paper.
	gen := New(nil, nil)
	gen.ArticleSource = ArticleSourceOptions{Kind: ArticleSourceWebSearch}
	request := englishArticleRequest{subject: "英语一", section: "阅读理解 Part A", groupID: "english1-reading-1", minWords: 280, maxWords: 420}
	if _, err := gen.acquireArticle(context.Background(), request); err == nil {
		t.Fatal("acquireArticle without a model client should fail")
	}
	// With the search endpoint unreachable the same holds: the caller sees the
	// error instead of a silently empty passage.
	gen.ArticleSource.SearchURL = "http://127.0.0.1:1/search?q={query}"
	if _, err := gen.acquireArticle(context.Background(), request); err == nil {
		t.Fatal("unreachable search endpoint should not yield an article without a client")
	}
}

func TestExtractArticleLinksFromJSONAndHTML(t *testing.T) {
	jsonBody := []byte(`{"web":{"results":[{"url":"https://example.org/a"},{"url":"https://example.org/b.pdf"},{"url":"https://example.org/c"}]}}`)
	links := extractArticleLinks(jsonBody, 3)
	if len(links) != 2 || links[0] != "https://example.org/a" || links[1] != "https://example.org/c" {
		t.Fatalf("json links = %v, want the two non-pdf urls", links)
	}
	htmlBody := []byte(`<html><body><a href="https://news.example.com/story-1">Story</a></body></html>`)
	if links := extractArticleLinks(htmlBody, 2); len(links) != 1 || links[0] != "https://news.example.com/story-1" {
		t.Fatalf("html links = %v", links)
	}
}

func TestEnglishSearchQueryMentionsSection(t *testing.T) {
	cloze := englishSearchQuery(englishArticleRequest{groupID: "english1-cloze"})
	if !strings.Contains(cloze, "passage") {
		t.Errorf("cloze query = %q", cloze)
	}
	reading := englishSearchQuery(englishArticleRequest{groupID: "english2-reading-3", textIndex: 3})
	if !strings.Contains(reading, "text 3") {
		t.Errorf("reading query = %q", reading)
	}
}

func TestEnglishSectionLabelReplacesKnowledgePoints(t *testing.T) {
	spec := english1TestSpec()
	cases := map[int]string{
		3:  "完形填空",
		26: "Part A",
		42: "Part B",
		47: "翻译 Part C",
		52: "写作 Part B",
	}
	for position, want := range cases {
		label := englishSectionLabel("英语一", spec.Entries[position-1])
		if !strings.Contains(label, want) {
			t.Errorf("position %d label = %q, want it to mention %q", position, label, want)
		}
	}
	// Position 26 is the second Part A text; the label follows the official map.
	label := englishSectionLabel("英语一", models.SpecEntry{Points: []string{"english1", "阅读理解", "Part A", "Text 2", "题位26"}})
	if !strings.Contains(label, "Text 2") {
		t.Errorf("Part A label lost the text number: %q", label)
	}
}

func TestFetchArticleUsesSearchResults(t *testing.T) {
	// A page the stub search API points at: the passage must come from here.
	page := "<html><head><style>p{color:red}</style></head><body><script>var x=1;</script>" +
		"<p>" + strings.Repeat("Urban planners now treat street trees as infrastructure rather than decoration. ", 12) + "</p></body></html>"
	pageServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(page))
	}))
	defer pageServer.Close()
	searchServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if got := r.URL.Query().Get("q"); got == "" {
			t.Errorf("search request without a query")
		}
		_, _ = w.Write([]byte(`{"web":{"results":[{"url":"` + pageServer.URL + `/article"}]}}`))
	}))
	defer searchServer.Close()

	gen := New(nil, nil)
	gen.ArticleSource = ArticleSourceOptions{
		Kind:       ArticleSourceWebSearch,
		SearchURL:  searchServer.URL + "/search?q={query}",
		MaxResults: 3,
	}
	request := englishArticleRequest{subject: "英语一", section: "阅读理解 Part A", groupID: "english1-reading-1", minWords: 120, maxWords: 400}
	fetched, err := gen.acquireArticle(context.Background(), request)
	if err != nil {
		t.Fatalf("acquireArticle: %v", err)
	}
	if !strings.Contains(fetched.Source, "websearch:") {
		t.Errorf("source = %q, want a websearch source", fetched.Source)
	}
	if strings.Contains(fetched.Text, "var x=1") || strings.Contains(fetched.Text, "color:red") {
		t.Errorf("page scripts/styles leaked into the article: %q", fetched.Text[:80])
	}
	if words := len(englishGroupWordPattern.FindAllString(fetched.Text, -1)); words < 100 {
		t.Errorf("fetched article only %d words", words)
	}
}

func TestFetchArticleFallsBackToWritingWhenSearchFails(t *testing.T) {
	// The search endpoint 500s; the model client is a stub that writes an article.
	searchServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer searchServer.Close()
	modelServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		content, _ := json.Marshal(map[string]string{
			"topic":   "why repair cafés spread",
			"article": strings.Repeat("Repair cafés give broken appliances a second life and teach visitors how to fix them. ", 12),
		})
		writeOpenAIJSON(w, content)
	}))
	defer modelServer.Close()

	gen := New(llm.New(modelServer.URL, "test-key"), nil)
	gen.DefaultModel = "test-model"
	gen.ArticleSource = ArticleSourceOptions{Kind: ArticleSourceWebSearch, SearchURL: searchServer.URL + "/search?q={query}"}
	request := englishArticleRequest{subject: "英语一", section: "阅读理解 Part A", groupID: "english1-reading-1", minWords: 100, maxWords: 400}
	written, err := gen.acquireArticle(context.Background(), request)
	if err != nil {
		t.Fatalf("acquireArticle: %v", err)
	}
	if !strings.HasPrefix(written.Source, "write") || !strings.Contains(written.Source, "websearch failed") {
		t.Errorf("source = %q, want the write fallback recording the failed search", written.Source)
	}
	if !strings.Contains(written.Text, "Repair cafés") {
		t.Errorf("fallback article not used: %q", written.Text[:80])
	}
}

func TestArticleConflictDetectsRepeatedTopicsAndOpenings(t *testing.T) {
	gen := New(nil, nil)
	first := article{
		Topic: "Why repair cafés are spreading in European cities",
		Text:  "On a Saturday morning in Amsterdam, volunteers at a community centre begin fixing toasters and lamps. The queue stretches out of the door every week.",
	}
	gen.rememberArticle(first)

	sameTopic := article{Topic: "Why repair cafés are spreading in European cities", Text: "A very different opening sentence introduces a wholly different story about mountain railways."}
	if conflict := gen.articleConflict(sameTopic); conflict == "" {
		t.Error("repeated topic was accepted")
	}
	sameOpening := article{Topic: "A history of public libraries", Text: "On a Saturday morning in Amsterdam, volunteers at a community centre begin repairing bicycles instead of appliances."}
	if conflict := gen.articleConflict(sameOpening); conflict == "" {
		t.Error("repeated opening was accepted")
	}
	fresh := article{Topic: "How urban beekeepers changed city rooftops", Text: "Rooftop hives now supply honey to bakeries in several European capitals, and the trend keeps growing."}
	if conflict := gen.articleConflict(fresh); conflict != "" {
		t.Errorf("distinct article rejected: %s", conflict)
	}
	if used := gen.usedTopicList(); !strings.Contains(used, "repair cafés") {
		t.Errorf("used topics = %q", used)
	}
	gen.ResetArticleHistory()
	if conflict := gen.articleConflict(sameTopic); conflict != "" {
		t.Errorf("history was not reset: %s", conflict)
	}
}

func TestPartBArticleRequestMatchesSubjectTask(t *testing.T) {
	english1 := englishArticleRequestFor("英语一", "english1-part-b", nil)
	if english1.tag != "english1" || !english1.removeGap {
		t.Errorf("english1 part-b request = %+v", english1)
	}
	english2 := englishArticleRequestFor("英语二", "english2-part-b", nil)
	if english2.tag != "english2" || !english2.removeGap {
		t.Errorf("english2 part-b request = %+v", english2)
	}
	// The subject tag drives the phase-one style hint (English II needs five
	// named viewpoints in the passage, English I needs removable sentences).
	gen := New(nil, nil)
	_, rules := englishGroupInstructions("english2", "part-b", "english2-part-b")
	if !strings.Contains(rules, "人物") {
		t.Errorf("english2 part-b rules lost the named-viewpoint task: %q", rules[:60])
	}
	_, rules1 := englishGroupInstructions("english1", "part-b", "english1-part-b")
	if !strings.Contains(rules1, "七选五") {
		t.Errorf("english1 part-b rules lost the sentence-insertion task")
	}
	_ = gen
}
