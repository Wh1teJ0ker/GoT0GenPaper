package generator

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"GoT0GenPaper/internal/llm"
	"GoT0GenPaper/internal/models"
)

// english1TestSpec mirrors the official English I map: cloze 1-20, Part A 21-40,
// Part B 41-45 (七选五), translation 46-50, letter 51, essay 52.
func english1TestSpec() models.SpecTable {
	spec := models.SpecTable{Subject: "英语一", TotalScore: 100}
	for position := 1; position <= 52; position++ {
		points, _ := models.EnglishPointsPath("english1", position)
		slot, _ := models.EnglishSlotFor("english1", position)
		spec.Entries = append(spec.Entries, models.SpecEntry{
			ID: fmt.Sprintf("slot-%02d", position), Index: position,
			Type: slot.Type, Score: slot.Score, Points: points, DiffBand: models.BandMedium,
		})
	}
	return spec
}

func TestValidateEnglishSpecAcceptsEnglish1AndRejectsMislabeledPartB(t *testing.T) {
	spec := english1TestSpec()
	byPosition, err := validateEnglishSpec("english1", spec.Entries)
	if err != nil {
		t.Fatalf("valid English I spec rejected: %v", err)
	}
	if len(byPosition) != 52 {
		t.Fatalf("positions = %d, want 52", len(byPosition))
	}
	if got := byPosition[41].Points[3]; got != "七选五" {
		t.Errorf("Part B label = %q, want 七选五", got)
	}
	if got := byPosition[51].Score; got != 10 {
		t.Errorf("letter score = %.1f, want 10", got)
	}
	if got := byPosition[52].Score; got != 20 {
		t.Errorf("essay score = %.1f, want 20", got)
	}

	// The English II matching-task label must not pass for English I.
	broken := english1TestSpec()
	broken.Entries[40].Points = []string{"english1", "阅读理解", "Part B", "人物观点匹配", "题位41"}
	if _, err := validateEnglishSpec("english1", broken.Entries); err == nil {
		t.Fatal("English I Part B accepted the English II matching-task label")
	}

	// A wrong score in the translation group must be rejected too.
	broken = english1TestSpec()
	broken.Entries[45].Score = 15
	if _, err := validateEnglishSpec("english1", broken.Entries); err == nil {
		t.Fatal("English I accepted a 15-point translation item")
	}
}

func TestGenerateEnglish1OfflineSharesGroupsWithoutProvider(t *testing.T) {
	gen := New(nil, nil)
	questions, err := gen.generateEnglishAll(context.Background(), english1TestSpec())
	if err != nil {
		t.Fatalf("offline English I generation failed: %v", err)
	}
	if len(questions) != 52 {
		t.Fatalf("questions = %d, want 52", len(questions))
	}
	wantGroups := map[string]int{
		"english1-cloze": 20, "english1-reading-1": 5, "english1-reading-4": 5,
		"english1-part-b": 5, "english1-translation": 5,
	}
	gotGroups := map[string]int{}
	for _, q := range questions {
		if q.Status != "needs_review" {
			t.Errorf("%s status = %q, want needs_review offline", q.SpecID, q.Status)
		}
		gotGroups[q.GroupID]++
	}
	for group, want := range wantGroups {
		if gotGroups[group] != want {
			t.Errorf("group %s has %d questions, want %d", group, gotGroups[group], want)
		}
	}
	// The two writing tasks are standalone.
	if gotGroups[""] != 2 {
		t.Errorf("standalone questions = %d, want 2 (letter + essay)", gotGroups[""])
	}
}

func TestGenerateEnglish1GroupRequestsSharedMaterial(t *testing.T) {
	spec := english1TestSpec()
	groupCalls := map[string]int{}
	articleCalls := 0
	lastArticle := ""
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var request llm.ChatRequest
		if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		user, _ := request.Messages[len(request.Messages)-1].Content.(string)
		var content []byte
		if isArticleRequest(user) {
			articleCalls++
			lastArticle = fixtureArticleVariant(articleCalls)
			content = marshalArticleResponse(lastArticle, articleCalls)
		} else if strings.Contains(user, "题组编号：") {
			groupID := strings.SplitN(strings.TrimPrefix(user[strings.Index(user, "题组编号："):], "题组编号："), "\n", 2)[0]
			groupCalls[groupID]++
			content = marshalEnglish1GroupFixture(t, groupID, lastArticle)
		} else {
			response, _ := json.Marshal(generationResponse{
				Stem:       "Directions: Write an email to the library about extending the opening hours of the reading room.",
				Answer:     "答案：Dear Sir or Madam, I am writing to suggest extending the opening hours. First, students need the room in the evening. Second, the library has staff available. Thank you for your consideration. Yours sincerely, Li Ming.",
				Difficulty: 0.55,
			})
			content = response
		}
		writeOpenAIJSON(w, content)
	}))
	defer server.Close()
	gen := New(llm.New(server.URL, "test-key"), nil)
	questions, err := gen.generateEnglishAll(context.Background(), spec)
	if err != nil {
		t.Fatalf("English I generation failed: %v", err)
	}
	if len(questions) != 52 {
		t.Fatalf("questions = %d, want 52", len(questions))
	}
	for _, group := range []string{"english1-cloze", "english1-reading-1", "english1-reading-2", "english1-reading-3", "english1-reading-4", "english1-part-b", "english1-translation"} {
		if groupCalls[group] == 0 {
			t.Errorf("group %s was never requested", group)
		}
	}
	// Article-first: exactly one passage request per composite group.
	if articleCalls != len(groupCalls) {
		t.Errorf("article requests = %d, want %d (one per group)", articleCalls, len(groupCalls))
	}
	// Cloze: one shared passage referenced by 20 blanks.
	cloze := questions[0]
	if cloze.SharedMaterial == "" || cloze.GroupID != "english1-cloze" {
		t.Errorf("cloze question carries no shared passage: group=%q material=%d chars", cloze.GroupID, len(cloze.SharedMaterial))
	}
	// Translation: five items over one passage, no per-item options.
	for position := 46; position <= 50; position++ {
		q := questions[position-1]
		if q.SharedMaterial == "" || q.GroupID != "english1-translation" {
			t.Errorf("translation %d did not share the passage", position)
		}
		if len(q.Options) != 0 {
			t.Errorf("translation %d carries options", position)
		}
	}
	// Part B: shared sentence list, answers A-G distinct. The shared list is
	// carried as both SharedOptions and Options (the assembler prints it once
	// and suppresses the per-question copy), matching the English II contract.
	partB := questions[40:45]
	if len(partB[0].SharedOptions) != 7 {
		t.Fatalf("Part B shared options = %d, want 7", len(partB[0].SharedOptions))
	}
	seen := map[string]bool{}
	for _, q := range partB {
		if len(q.Options) != len(q.SharedOptions) {
			t.Errorf("Part B %s options = %d, want the shared list (%d)", q.SpecID, len(q.Options), len(q.SharedOptions))
		}
		answer := strings.TrimSpace(q.Answer)
		if !strings.HasPrefix(answer, "答案：") || len([]rune(answer)) < 4 {
			t.Errorf("Part B %s answer = %q", q.SpecID, answer)
		}
		letter := strings.TrimSpace(strings.TrimPrefix(answer, "答案："))[:1]
		if seen[letter] {
			t.Errorf("Part B answer %s reused", letter)
		}
		seen[letter] = true
	}
}

// marshalEnglish1GroupFixture builds one valid item response per group id. The
// material and the translation stems come from fixtureArticle(), which is the
// passage phase one returned, so the article-drift invariant holds.
func marshalEnglish1GroupFixture(t *testing.T, groupID, article string) []byte {
	t.Helper()
	start, end := 1, 20
	switch {
	case strings.HasSuffix(groupID, "-part-b"):
		start, end = 41, 45
	case strings.HasSuffix(groupID, "-translation"):
		start, end = 46, 50
	case strings.Contains(groupID, "-reading-"):
		textNumber := int(groupID[len(groupID)-1] - '0')
		start, end = 21+(textNumber-1)*5, 25+(textNumber-1)*5
	}
	sentences := splitArticleSentences(article)
	material := article
	response := english2GroupResponse{SharedMaterial: material}
	switch {
	case strings.HasSuffix(groupID, "-cloze"):
		var markers strings.Builder
		for number := 1; number <= 20; number++ {
			fmt.Fprintf(&markers, " [%d]", number)
		}
		response.SharedMaterial += markers.String()
	case strings.HasSuffix(groupID, "-part-b"):
		var gaps strings.Builder
		for number := 41; number <= 45; number++ {
			fmt.Fprintf(&gaps, " [%d]", number)
		}
		response.SharedMaterial += gaps.String()
		response.SharedOptions = []string{
			"Local knowledge often fills gaps that national statistics cannot cover.",
			"Critics argue that the process can still exclude the least powerful voices.",
			"Supporters reply that gradual reform is more durable than sudden rupture.",
			"Transparent reporting matters more than any single result.",
			"Steady practice matters more than initial talent.",
			"Shared routines spread through everyday conversation.",
			"Careful observation is the beginning of every durable habit.",
		}
	}
	for position := start; position <= end; position++ {
		item := english2GroupItem{
			SpecID:     fmt.Sprintf("slot-%02d", position),
			Stem:       fmt.Sprintf("Choose the best answer for question %d.", position),
			Answer:     "答案：A。A 项与文章中的表述一致，符合上下文语境与搭配；B 项、C 项和 D 项都与原文信息不符，可以排除。",
			Difficulty: 0.55,
		}
		switch {
		case strings.HasSuffix(groupID, "-cloze"):
			item.Stem = fmt.Sprintf("Blank [%d]", position)
			item.Options = []string{"maintain", "ignore", "delay", "reduce"}
		case strings.HasSuffix(groupID, "-part-b"):
			item.Stem = fmt.Sprintf("Blank [%d]", position)
			letter := string(rune('A' + (position - 41)))
			item.Answer = fmt.Sprintf("答案：%s。该句在语法与语义上衔接第 %d 个空位，逻辑与上下文一致；其余备选句与原文衔接不符，可以排除。", letter, position)
		case strings.HasSuffix(groupID, "-translation"):
			// Reuse real sentences from the passage so the stem is verbatim.
			item.Stem = sentences[position-46]
			item.Answer = fmt.Sprintf("答案：这是文章的译文示例（第 %d 句）。翻译要点：注意从句结构与指代关系，保持译文通顺准确。", position-45)
		default:
			item.Options = []string{"maintain", "ignore", "delay", "reduce"}
		}
		response.Items = append(response.Items, item)
	}
	encoded, err := json.Marshal(response)
	if err != nil {
		t.Fatalf("marshal fixture: %v", err)
	}
	return encoded
}

func TestGroupRepairReplacesPlaceholderMaterialWithAFreshArticle(t *testing.T) {
	// A group that degraded to placeholders stores a "% TODO" stand-in. Repair
	// must not treat it as the source article.
	entries := english1TestSpec().Entries[20:25] // Part A Text 1
	placeholder := englishPlaceholders("english1", "english1-reading-1", entries)
	articleCalls := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var request llm.ChatRequest
		if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		user, _ := request.Messages[len(request.Messages)-1].Content.(string)
		if isArticleRequest(user) {
			articleCalls++
			writeOpenAIJSON(w, marshalArticleResponse(fixtureArticleVariant(1), 1))
			return
		}
		writeOpenAIJSON(w, marshalEnglish1GroupFixture(t, "english1-reading-1", fixtureArticleVariant(1)))
	}))
	defer server.Close()
	gen := New(llm.New(server.URL, "test-key"), nil)
	gen.DefaultModel = "test-model"
	questions, err := gen.RegenerateEnglishGroup(context.Background(), "英语一", "english1-reading-1", entries, articleForGroup(placeholder))
	if err != nil {
		t.Fatalf("repair failed: %v", err)
	}
	if articleCalls == 0 {
		t.Fatal("repair reused the placeholder instead of acquiring an article")
	}
	for _, q := range questions {
		if q.Status != "ready" || strings.Contains(q.SharedMaterial, "TODO") {
			t.Errorf("%s still broken: status=%q material=%q", q.SpecID, q.Status, q.SharedMaterial[:40])
		}
	}
}
