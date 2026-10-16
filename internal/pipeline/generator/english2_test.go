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

func english2TestSpec() models.SpecTable {
	spec := models.SpecTable{Subject: "英语二", TotalScore: 100}
	for position := 1; position <= 48; position++ {
		entry := models.SpecEntry{
			ID: fmt.Sprintf("slot-%02d", position), Index: position,
			Type: models.TypeChoice, Score: 2, DiffBand: models.BandMedium,
		}
		switch {
		case position <= 20:
			entry.Score = 0.5
			entry.Points = []string{"english2", "完形填空", "语篇词汇与语境", fmt.Sprintf("题位%d", position)}
		case position <= 40:
			text := (position-21)/5 + 1
			entry.Points = []string{"english2", "阅读理解", "Part A", fmt.Sprintf("Text %d", text), fmt.Sprintf("题位%d", position)}
		case position <= 45:
			entry.Points = []string{"english2", "阅读理解", "Part B", "人物观点匹配", fmt.Sprintf("题位%d", position)}
		case position == 46:
			entry.Type, entry.Score = models.TypeMajor, 15
			entry.Points = []string{"english2", "翻译", "英译汉", "题位46"}
		case position == 47:
			entry.Type, entry.Score = models.TypeMajor, 10
			entry.Points = []string{"english2", "写作", "应用文", "题位47"}
		case position == 48:
			entry.Type, entry.Score = models.TypeMajor, 15
			entry.Points = []string{"english2", "写作", "图表作文", "题位48"}
		}
		spec.Entries = append(spec.Entries, entry)
	}
	return spec
}

func TestGenerateEnglish2UsesNineSharedMaterialRequests(t *testing.T) {
	spec := english2TestSpec()
	calls, articleCalls := 0, 0
	lastArticle := ""
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		var request llm.ChatRequest
		if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
			t.Errorf("decode request: %v", err)
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		user, _ := request.Messages[len(request.Messages)-1].Content.(string)
		var content []byte
		if isArticleRequest(user) {
			// Phase 1: the passage the questions will be written from. Each call
			// returns a distinct article: the generator refuses to reuse a topic
			// or an opening already used in this paper.
			articleCalls++
			lastArticle = fixtureArticleVariant(articleCalls)
			content = marshalArticleResponse(lastArticle, articleCalls)
		} else if strings.Contains(user, "题组编号：") {
			groupID := strings.SplitN(strings.TrimPrefix(user[strings.Index(user, "题组编号："):], "题组编号："), "\n", 2)[0]
			content = marshalEnglish2GroupFixture(t, groupID, lastArticle)
		} else {
			stem := "Directions: Write an email based on the task."
			if strings.Contains(user, "题位46") {
				stem = strings.Repeat("Careful public discussion helps communities understand how institutions make difficult choices and how citizens can take part in decisions that affect their daily lives. ", 5)
			} else if strings.Contains(user, "题位48") {
				stem = "Directions: Write an essay based on the chart. Values are 55%, 42%, and 31%. Describe and interpret the chart and give your comments."
			}
			response, _ := json.Marshal(generationResponse{
				Stem:       stem,
				Answer:     "参考答案：这是一份完整示例文本。根据题目要求，先明确主题和要点，再组织连贯表达，并检查信息覆盖、逻辑关系与语言准确性。",
				Difficulty: 0.55,
			})
			content = response
		}
		writeOpenAIJSON(w, content)
	}))
	defer server.Close()
	gen := New(llm.New(server.URL, "test-key"), nil)
	gen.DefaultModel = "test-model"

	questions, err := gen.GenerateAll(context.Background(), spec)
	if err != nil {
		t.Fatal(err)
	}
	// Article-first flow: one passage request per composite group, then one item
	// request per group, plus the three standalone writing tasks.
	if articleCalls != 6 {
		t.Fatalf("made %d article requests, want 6 (one per group)", articleCalls)
	}
	if calls != 6+6+3 {
		t.Fatalf("made %d model requests, want 15 (6 articles + 6 item groups + 3 writing)", calls)
	}
	if len(questions) != 48 {
		t.Fatalf("generated %d questions, want 48", len(questions))
	}
	for start, groupID := range map[int]string{1: "english2-cloze", 21: "english2-reading-1", 26: "english2-reading-2", 31: "english2-reading-3", 36: "english2-reading-4", 41: "english2-part-b"} {
		first := questions[start-1]
		if first.GroupID != groupID || strings.TrimSpace(first.SharedMaterial) == "" {
			t.Fatalf("question at position %d not attached to %s: %+v", start, groupID, first)
		}
		end := start + 4
		if groupID == "english2-cloze" {
			end = 20
		}
		for position := start; position <= end; position++ {
			if questions[position-1].GroupID != groupID || questions[position-1].SharedMaterial != first.SharedMaterial {
				t.Fatalf("question %d does not share %s material", position, groupID)
			}
		}
	}
	for position := 41; position <= 45; position++ {
		question := questions[position-1]
		if len(question.Options) != 7 || len(question.SharedOptions) != 7 {
			t.Fatalf("Part B question %d has %d local and %d shared options", position, len(question.Options), len(question.SharedOptions))
		}
	}
}

func TestGenerateEnglish2RejectsWrongSpecBeforeCallingModel(t *testing.T) {
	calls := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		writeOpenAIJSON(w, []byte(`{}`))
	}))
	defer server.Close()
	spec := english2TestSpec()
	spec.Entries[40].Points[2] = "Part A"
	gen := New(llm.New(server.URL, "test-key"), nil)
	if _, err := gen.GenerateAll(context.Background(), spec); err == nil || !strings.Contains(err.Error(), "题位 41") {
		t.Fatalf("invalid Part B spec accepted: %v", err)
	}
	if calls != 0 {
		t.Fatalf("made %d model calls for invalid 2026 structure", calls)
	}
}

func TestEnglish2GroupValidationRejectsMalformedCompositeStructures(t *testing.T) {
	t.Run("cloze marker boundaries", func(t *testing.T) {
		entries := english2TestSpec().Entries[:20]
		var response english2GroupResponse
		if err := json.Unmarshal(marshalEnglish2GroupFixture(t, "english2-cloze", fixtureArticleVariant(1)), &response); err != nil {
			t.Fatal(err)
		}
		response.SharedMaterial = strings.Replace(response.SharedMaterial, "[1]", "[10]", 1)
		if issue := validateEnglish2GroupResponse("english2-cloze", entries, response); issue == "" {
			t.Fatal("accepted an out-of-range/duplicate cloze marker")
		}
	})

	t.Run("Part B must use named viewpoints", func(t *testing.T) {
		entries := english2TestSpec().Entries[40:45]
		var response english2GroupResponse
		if err := json.Unmarshal(marshalEnglish2GroupFixture(t, "english2-part-b", fixtureArticleVariant(1)), &response); err != nil {
			t.Fatal(err)
		}
		response.Items[0].Stem = "Paragraph 1"
		if issue := validateEnglish2GroupResponse("english2-part-b", entries, response); issue == "" {
			t.Fatal("accepted numbered-paragraph matching instead of named-viewpoint matching")
		}
	})

	t.Run("Part B answers must be one-to-one", func(t *testing.T) {
		entries := english2TestSpec().Entries[40:45]
		var response english2GroupResponse
		if err := json.Unmarshal(marshalEnglish2GroupFixture(t, "english2-part-b", fixtureArticleVariant(1)), &response); err != nil {
			t.Fatal(err)
		}
		response.Items[1].Answer = response.Items[0].Answer
		if issue := validateEnglish2GroupResponse("english2-part-b", entries, response); issue == "" {
			t.Fatal("accepted duplicate Part B heading answer")
		}
	})

	t.Run("Part B headings are not repeated per item", func(t *testing.T) {
		entries := english2TestSpec().Entries[40:45]
		var response english2GroupResponse
		if err := json.Unmarshal(marshalEnglish2GroupFixture(t, "english2-part-b", fixtureArticleVariant(1)), &response); err != nil {
			t.Fatal(err)
		}
		response.Items[0].Options = append([]string(nil), response.SharedOptions...)
		if issue := validateEnglish2GroupResponse("english2-part-b", entries, response); issue == "" {
			t.Fatal("accepted options duplicated on every Part B question")
		}
	})
}

func TestValidateEnglish2SpecRequiresExact2026LayoutAndPoints(t *testing.T) {
	spec := english2TestSpec()
	spec.Entries[40].Points[3] = "普通四选一"
	if _, err := validateEnglish2Spec(spec.Entries); err == nil {
		t.Fatal("accepted generic Part B spec tag")
	}
	spec = english2TestSpec()
	spec.Entries[47].Score = 10
	if _, err := validateEnglish2Spec(spec.Entries); err == nil {
		t.Fatal("accepted wrong score at question 48")
	}
}

// TestGenerateEnglish2OfflineFallsBackToPlaceholders covers the no-LLM path:
// the offline pipeline must still produce a complete, correctly positioned
// 48-question paper instead of aborting on a missing model client.
func TestGenerateEnglish2OfflineFallsBackToPlaceholders(t *testing.T) {
	spec := english2TestSpec()
	questions, err := New(nil, nil).GenerateAll(context.Background(), spec)
	if err != nil {
		t.Fatalf("offline english2 generation failed: %v", err)
	}
	if len(questions) != 48 {
		t.Fatalf("generated %d questions, want 48", len(questions))
	}
	for i, question := range questions {
		if position, ok := models.EnglishQuestionPosition(question.Points); !ok || position != i+1 {
			t.Errorf("question %d carries 题位 %v (ok=%v)", i+1, position, ok)
		}
		if question.Status != "needs_review" || len(question.Warnings) == 0 {
			t.Errorf("question %d should be flagged as a placeholder, got status=%q warnings=%v", i+1, question.Status, question.Warnings)
		}
		if i < 45 && question.GroupID == "" {
			t.Errorf("question %d lost its composite group id", i+1)
		}
		if i >= 45 && question.GroupID != "" {
			t.Errorf("question %d (translation/writing) must not carry a group id", i+1)
		}
	}
}

// fixtureArticle is the deterministic passage the stub model "writes" in phase
// one. The item fixture derives its material from this same text, which is what
// the article-drift invariant requires of a real response.
func fixtureArticle() string {
	sentences := []string{
		"People often learn from careful observation, shared evidence, and conversations that connect research with daily experience.",
		"A long study followed residents in forty towns and recorded how they changed their habits over a decade.",
		"Researchers compared the results with local records and found that steady practice mattered more than initial talent.",
		"The team also interviewed teachers, nurses, and engineers who had adopted the same routines at work.",
		"Those interviews showed that small adjustments, repeated patiently, produced the clearest improvements.",
		"Ramni Jannadass identifies the shortage of planting material as a major bottleneck for restoration projects.",
		"Christopher Kettle explains why infrastructure for collecting and storing seeds is essential to long-term success.",
		"John Stanturf warns that fast-multiplying forest creatures can threaten the very restoration they are meant to support.",
		"Pedro Brancalion compares global restoration goals with the practical needs of local communities.",
		"Robin Chazdon stresses that restoration is about people as well as trees.",
	}
	var builder strings.Builder
	for i := 0; i < 3; i++ {
		for _, sentence := range sentences {
			builder.WriteString(sentence)
			builder.WriteString(" ")
		}
	}
	return strings.TrimSpace(builder.String())
}

// fixtureArticleVariant returns a distinct passage for the n-th article request:
// the generator rejects a repeat of a topic or of an opening already used in the
// same paper, exactly as a real model response would be rejected.
func fixtureArticleVariant(index int) string {
	openings := []string{
		"Urban beekeepers have quietly changed how city rooftops are used across several European capitals.",
		"Long-distance night trains are returning to routes that closed decades ago, and passengers are noticing.",
		"Community libraries in small towns now lend tools and sewing machines alongside books.",
		"Vertical farms inside former warehouses supply salad leaves to supermarkets within a day.",
		"Old industrial canals are being reopened for freight, which reduces pressure on motorways.",
		"School playgrounds are being redesigned so that children spend more of the day moving.",
		"Public drinking fountains are reappearing in city centres as single-use bottles fall out of favour.",
		"Apprentice schemes in rural workshops are training a new generation of furniture restorers.",
	}
	opening := openings[(index-1)%len(openings)]
	return opening + " " + fixtureArticle()
}

// isArticleRequest reports whether a prompt asks for the passage (phase one)
// rather than for questions (phase two).
func isArticleRequest(user string) bool {
	return strings.Contains(user, `"topic"`) && !strings.Contains(user, "题组编号：")
}

// marshalArticleResponse encodes the phase-one JSON payload with a topic that is
// unique per call, mirroring how a real author would vary the subject.
func marshalArticleResponse(text string, index int) []byte {
	content, _ := json.Marshal(map[string]string{
		"topic":   fmt.Sprintf("fixture topic %d", index),
		"article": text,
	})
	return content
}

func marshalEnglish2GroupFixture(t *testing.T, groupID, article string) []byte {
	t.Helper()
	start, end := 1, 20
	if groupID == "english2-part-b" {
		start, end = 41, 45
	} else if strings.HasPrefix(groupID, "english2-reading-") {
		textNumber := int(groupID[len("english2-reading-")] - '0')
		start, end = 21+(textNumber-1)*5, 25+(textNumber-1)*5
	}
	material := article
	if groupID == "english2-cloze" {
		// Cloze: insert the numbered blanks without touching the prose.
		var markers strings.Builder
		for number := 1; number <= 20; number++ {
			fmt.Fprintf(&markers, " [%d]", number)
		}
		material += markers.String()
	}
	response := english2GroupResponse{SharedMaterial: material}
	if groupID == "english2-part-b" {
		response.SharedOptions = []string{
			"Learning through shared experience", "A new role for local institutions",
			"The limits of individual action", "Making public information useful",
			"Cooperation across generations", "Measuring long-term outcomes",
			"Adapting services to changing needs",
		}
	}
	partBTargets := []string{"Ramni Jannadass", "Christopher Kettle", "John Stanturf", "Pedro Brancalion", "Robin Chazdon"}
	for position := start; position <= end; position++ {
		item := english2GroupItem{
			SpecID:     fmt.Sprintf("slot-%02d", position),
			Stem:       fmt.Sprintf("Choose the best answer for question %d.", position),
			Answer:     "答案：A。根据共同材料中的核心信息，A 项符合题意；B 项、C 项和 D 项与该信息不符。",
			Difficulty: 0.55,
		}
		if groupID == "english2-cloze" {
			item.Stem = fmt.Sprintf("Blank [%d]", position)
		}
		if groupID != "english2-part-b" {
			item.Options = []string{
				fmt.Sprintf("Choice A for item %d", position), fmt.Sprintf("Choice B for item %d", position),
				fmt.Sprintf("Choice C for item %d", position), fmt.Sprintf("Choice D for item %d", position),
			}
		} else {
			item.Stem = partBTargets[position-start]
			item.Answer = fmt.Sprintf("答案：%c。该人物的观点明确支持对应陈述。结合共同材料中的具体表述，选项 %c 能准确概括其立场，其余选项与该人物观点不一致。", 'A'+rune(position-start), 'A'+rune(position-start))
		}
		response.Items = append(response.Items, item)
	}
	encoded, err := json.Marshal(response)
	if err != nil {
		t.Fatal(err)
	}
	return encoded
}

func writeOpenAIJSON(w http.ResponseWriter, content []byte) {
	w.Header().Set("Content-Type", "application/json")
	body, _ := json.Marshal(map[string]any{"choices": []any{map[string]any{"message": map[string]string{"content": string(content)}}}})
	_, _ = w.Write(body)
}
