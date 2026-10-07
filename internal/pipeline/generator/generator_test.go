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

func choiceSpec(kind models.QuestionType) models.SpecEntry {
	return models.SpecEntry{ID: "slot-1", Type: kind, Score: 2, DiffBand: models.BandMedium}
}

func TestFinishValidatesChoiceAnswerShape(t *testing.T) {
	g := New(nil, nil)
	q, err := g.finish(choiceSpec(models.TypeChoice), generationResponse{
		Stem:       "题面",
		Options:    []string{"甲", "乙", "丙", "丁"},
		Answer:     "答案为 B。根据题设中的已知条件，代入对应公式逐步计算可得该结果，因此应选择 B。",
		Difficulty: .5,
	})
	if err != nil || q.Status != "ready" || len(q.Warnings) != 0 {
		t.Fatalf("valid choice = %+v, err=%v", q, err)
	}
}

func TestFinishMarksMalformedChoiceForReview(t *testing.T) {
	g := New(nil, nil)
	q, err := g.finish(choiceSpec(models.TypeChoice), generationResponse{
		Stem:       "题面",
		Options:    []string{"甲", "乙", "丙"},
		Answer:     "参考答案无法确定",
		Difficulty: .5,
	})
	if err != nil || q.Status != "needs_review" || len(q.Warnings) == 0 {
		t.Fatalf("malformed choice = %+v, err=%v", q, err)
	}
}

func TestFinishNormalizesDoubleEscapedNewlinesAndEnglishAnswers(t *testing.T) {
	g := New(nil, nil)
	spec := models.SpecEntry{
		ID: "english-cloze", Type: models.TypeChoice, Score: 0.5,
		DiffBand: models.BandMedium, Points: []string{"英语一", "完形填空", "词汇与语法"},
	}
	q, err := g.finish(spec, generationResponse{
		Stem:       `A sentence with one blank.\\nChoose the best word.`,
		Options:    []string{"grounded", "justified", "bolstered", "inspired"},
		Answer:     `答案：A（grounded）。\\n\\n根据句意及 on evidence 的搭配，grounded 表示“以证据为基础”。分析选项：A) grounded 符合固定搭配；B) justified 表示“证明合理”，不能修饰此处的 projections；C) bolstered 表示“加强”，语义不合；D) inspired 表示“启发”，搭配和语义均不符。因此选择 A。`,
		Difficulty: 0.5,
	})
	if err != nil {
		t.Fatal(err)
	}
	if q.Status != "ready" {
		t.Fatalf("normalized English item status=%s warnings=%v answer=%q", q.Status, q.Warnings, q.Answer)
	}
	if strings.Contains(q.Stem, `\\n`) || strings.Contains(q.Answer, `\\n`) || !strings.Contains(q.Stem, "\n") {
		t.Fatalf("escaped line breaks were not normalized: stem=%q answer=%q", q.Stem, q.Answer)
	}
	if !strings.Contains(q.Answer, "C) bolstered") {
		t.Fatalf("answer explanation lost its distractor evidence: %q", q.Answer)
	}
}

func TestEnglishChoicePromptRequiresOneCompleteBlankAndClearRationale(t *testing.T) {
	spec := models.SpecEntry{Type: models.TypeChoice, Points: []string{"英语一", "完形填空", "词汇与语法"}, Score: 0.5}
	prompt := New(nil, nil).buildSystemPrompt(spec)
	for _, required := range []string{"题面必须原创", "不得照抄或近似改写", "只能留一个待答空", "第二空", "元叙述", "答案：X（正确词/短语）", "逐项说明正确项与干扰项", "分值: 0.50"} {
		if !strings.Contains(prompt, required) {
			t.Errorf("English prompt missing %q", required)
		}
	}
}

func Test408PromptRequiresPreciseGraphTraversalAndCycleReasoning(t *testing.T) {
	spec := models.SpecEntry{Type: models.TypeMajor, Points: []string{"408", "数据结构", "图的遍历"}, Score: 10}
	prompt := New(nil, nil).buildSystemPrompt(spec)
	for _, required := range []string{"递归栈/颜色状态", "不能只凭 visited 布尔标志", "访问序列、队列/递归栈"} {
		if !strings.Contains(prompt, required) {
			t.Errorf("408 prompt missing %q", required)
		}
	}
}

func TestMath2PromptForbidsInfiniteSeries(t *testing.T) {
	spec := models.SpecEntry{
		ID: "math2-slot", Type: models.TypeMajor, Score: 10,
		Points: []string{"高等数学", "一元函数积分学", "定积分的计算"},
	}
	prompt := New(nil, nil).buildSystemPromptForSubject("math2", spec)
	for _, required := range []string{"数学二不考无穷级数", "禁止考查", "题位数据错误"} {
		if !strings.Contains(prompt, required) {
			t.Errorf("math2 prompt missing %q", required)
		}
	}
}

func TestPoliticsPromptForbidsCrossSubjectQuestionContent(t *testing.T) {
	spec := models.SpecEntry{
		ID: "politics-slot", Type: models.TypeChoice, Score: 1,
		Points: []string{"马克思主义基本原理", "唯物辩证法与矛盾分析"},
	}
	prompt := New(nil, nil).buildSystemPromptForSubject("政治", spec)
	for _, required := range []string{"考研思想政治理论", "六个考纲模块", "严禁生成数学、概率统计、计算机或英语题", "不得臆造最新政策"} {
		if !strings.Contains(prompt, required) {
			t.Errorf("politics prompt missing %q", required)
		}
	}
}

func TestGenerateAllRejectsSyntheticPoliticsPointsBeforeLLM(t *testing.T) {
	var calls int
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"choices":[{"message":{"content":"{}"}}]}`))
	}))
	defer server.Close()
	gen := New(llm.New(server.URL, "test-key"), nil)
	gen.DefaultModel = "test-model"
	spec := models.SpecTable{
		Subject: "政治",
		Entries: []models.SpecEntry{{
			ID: "politics-auto", Type: models.TypeChoice, Score: 1,
			Points: []string{"choice_auto_0"},
		}},
	}
	if _, err := gen.GenerateAll(context.Background(), spec); err == nil || !strings.Contains(err.Error(), "政治题位") {
		t.Fatalf("synthetic non-syllabus politics spec was accepted: %v", err)
	}
	if calls != 0 {
		t.Fatalf("made %d model requests for an invalid politics spec", calls)
	}
}

func TestMath2GenerationRejectsOutOfSyllabusSpecBeforeLLM(t *testing.T) {
	spec := models.SpecEntry{
		ID: "math2-series", Type: models.TypeMajor, Score: 10,
		Points: []string{"高等数学", "无穷级数", "常数项级数敛散性的判定"},
	}
	if _, err := New(nil, nil).GenerateForSubject(context.Background(), "math2", spec); err == nil || !strings.Contains(err.Error(), "无穷级数") {
		t.Fatalf("out-of-syllabus spec was not rejected before generation: %v", err)
	}
}

func TestGenerateAllRejectsOutOfSyllabusMath2SpecBeforeAnyGeneration(t *testing.T) {
	var calls int
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"choices":[{"message":{"content":"{\"stem\":\"题面\",\"options\":[],\"answer\":\"根据条件计算可得结论。\",\"difficulty\":0.5}"}}]}`))
	}))
	defer server.Close()
	gen := New(llm.New(server.URL, "test-key"), nil)
	gen.DefaultModel = "test-model"
	spec := models.SpecTable{
		Subject: "数学二",
		Entries: []models.SpecEntry{
			{ID: "math2-valid", Type: models.TypeMajor, Score: 10, Points: []string{"高等数学", "定积分的计算"}},
			{ID: "math2-series", Type: models.TypeMajor, Score: 10, Points: []string{"高等数学", "无穷级数"}},
		},
	}
	if _, err := gen.GenerateAll(context.Background(), spec); err == nil || !strings.Contains(err.Error(), "无穷级数") {
		t.Fatalf("invalid math2 spec table was not rejected: %v", err)
	}
	if calls != 0 {
		t.Fatalf("made %d model requests before rejecting the invalid spec table", calls)
	}
}

func TestMath2FinishFlagsInfiniteSeriesContentForReview(t *testing.T) {
	spec := models.SpecEntry{
		ID: "math2-slot", Type: models.TypeMajor, Score: 10,
		Points: []string{"高等数学", "一元函数积分学", "定积分的计算"},
	}
	q, err := New(nil, nil).finishForSubject("math2", spec, generationResponse{
		Stem:       `计算 $\sum_{n=1}^{\infty} 1/n^2$。`,
		Answer:     "根据 p 级数判别法，该无穷级数收敛，因此结论成立。这里给出完整理由并核对收敛条件。",
		Difficulty: 0.6,
	})
	if err != nil {
		t.Fatal(err)
	}
	if q.Status != "needs_review" || !strings.Contains(strings.Join(q.Warnings, " "), "不考的无穷级数") {
		t.Fatalf("out-of-syllabus generated content status=%q warnings=%v", q.Status, q.Warnings)
	}
}

func TestGenerateRetriesWhenDerivedSequenceConflictsWithSelectedOption(t *testing.T) {
	var calls int
	var retryPrompt string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var request llm.ChatRequest
		if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
			t.Errorf("decode request: %v", err)
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		calls++
		if calls == 2 {
			retryPrompt, _ = request.Messages[1].Content.(string)
		}
		answer := "答案：B。BFS序列为0, 1, 2, 3, 4。DFS序列为0, 1, 3, 2, 4，因此选择 B。"
		if calls == 2 {
			answer = "答案：A。BFS序列为0, 1, 2, 3, 4。DFS序列为0, 1, 3, 2, 4，因此选择 A。"
		}
		content, _ := json.Marshal(generationResponse{
			Stem: "按给定邻接表顺序进行 BFS 与 DFS，选择对应序列。",
			Options: []string{
				"BFS序列为0, 1, 2, 3, 4；DFS序列为0, 1, 3, 2, 4",
				"BFS序列为0, 1, 2, 3, 4；DFS序列为0, 1, 3, 4, 2",
				"BFS序列为0, 1, 2, 4, 3；DFS序列为0, 1, 3, 4, 2",
				"BFS序列为0, 1, 3, 2, 4；DFS序列为0, 1, 3, 2, 4",
			},
			Answer: answer, Difficulty: 0.5,
		})
		body, _ := json.Marshal(map[string]any{"choices": []any{map[string]any{"message": map[string]string{"content": string(content)}}}})
		_, _ = w.Write(body)
	}))
	defer server.Close()

	client := llm.New(server.URL, "test-key")
	g := New(client, nil)
	g.ModelByType["choice"] = "test-model"
	spec := choiceSpec(models.TypeChoice)
	spec.Points = []string{"408", "数据结构", "图的遍历"}
	question, err := g.Generate(context.Background(), spec)
	if err != nil {
		t.Fatal(err)
	}
	if question.Status != "ready" {
		t.Fatalf("question status=%s warnings=%v", question.Status, question.Warnings)
	}
	if calls != 2 || !strings.Contains(retryPrompt, "未选项 A, D") {
		t.Fatalf("calls=%d retry prompt=%q, want one retry with the sequence inconsistency", calls, retryPrompt)
	}
}

func TestFinishRejectsEnglishQuestionWithMultipleUnpairedBlanks(t *testing.T) {
	spec := models.SpecEntry{
		ID: "english-cloze", Type: models.TypeChoice, Score: 0.5,
		DiffBand: models.BandMedium, Points: []string{"英语一", "完形填空", "词汇与语法"},
	}
	q, err := New(nil, nil).finish(spec, generationResponse{
		Stem:       `The passage contains a context. One blank is \underline{\quad}; another is \underline{\quad}. Choose the second blank.`,
		Options:    []string{"grounded", "justified", "bolstered", "inspired"},
		Answer:     `答案：A（grounded）。根据上下文，grounded 符合搭配；C) bolstered 不符合，因此选择 A。`,
		Difficulty: 0.5,
	})
	if err != nil {
		t.Fatal(err)
	}
	if q.Status != "needs_review" || !strings.Contains(strings.Join(q.Warnings, " "), "2 个待答空") {
		t.Fatalf("incomplete English cloze item status=%s warnings=%v", q.Status, q.Warnings)
	}
}

func TestFinishCalibratesDifficultyToRequestedBand(t *testing.T) {
	g := New(nil, nil)
	for _, tc := range []struct {
		name  string
		band  models.DifficultyBand
		value float64
		want  float64
	}{
		{name: "easy", band: models.BandEasy, value: 0.8, want: 0.25},
		{name: "medium", band: models.BandMedium, value: 0.2, want: 0.5},
		{name: "hard", band: models.BandHard, value: 0.5, want: 0.75},
		{name: "in band", band: models.BandHard, value: 0.9, want: 0.9},
	} {
		t.Run(tc.name, func(t *testing.T) {
			q, err := g.finish(models.SpecEntry{ID: tc.name, Type: models.TypeMajor, Score: 10, DiffBand: tc.band}, generationResponse{
				Stem: "完整题面", Answer: "完整答案", Difficulty: tc.value,
			})
			if err != nil {
				t.Fatalf("finish: %v", err)
			}
			if q.Difficulty != tc.want {
				t.Errorf("difficulty = %.2f, want %.2f", q.Difficulty, tc.want)
			}
		})
	}
}

func TestGenerateRejectsInvalidSpec(t *testing.T) {
	g := New(nil, nil)
	if _, err := g.Generate(nil, models.SpecEntry{ID: "", Type: models.TypeChoice, Score: 2}); err == nil {
		t.Fatal("empty spec ID should fail")
	}
	if _, err := g.Generate(nil, models.SpecEntry{ID: "slot-1", Type: models.TypeChoice}); err == nil {
		t.Fatal("non-positive score should fail")
	}
}

func TestGenerateRetriesOnceWhenChoiceShapeIsInvalid(t *testing.T) {
	var calls int
	var firstRequest llm.ChatRequest
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var request llm.ChatRequest
		if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
			t.Errorf("decode request: %v", err)
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		calls++
		if calls == 1 {
			firstRequest = request
		}
		content := `{"stem":"计算题，结果为____","options":[],"answer":"1","difficulty":0.5}`
		if calls == 2 {
			content = `{"stem":"设函数 $f(x)=x^2$，则 $f'(1)$ 等于（ ）","options":["$0$","$1$","$2$","$3$"],"answer":"答案：C。根据幂函数求导公式，先得 $f'(x)=2x$，再代入 $x=1$ 得 $f'(1)=2，因此选择 C。","difficulty":0.5}`
		}
		body, err := json.Marshal(map[string]any{"choices": []any{map[string]any{"message": map[string]string{"content": content}}}})
		if err != nil {
			t.Errorf("marshal response: %v", err)
			w.WriteHeader(http.StatusInternalServerError)
			return
		}
		_, _ = w.Write(body)
	}))
	defer srv.Close()

	client := llm.New(srv.URL, "test-key")
	client.ReasoningEffort = "low"
	client.MaxTokens = 4096
	g := New(client, nil)
	g.ModelByType["choice"] = "glm-5.2"
	q, err := g.Generate(context.Background(), choiceSpec(models.TypeChoice))
	if err != nil {
		t.Fatal(err)
	}
	if q.Status != "ready" {
		t.Fatalf("question status = %q, warnings=%v", q.Status, q.Warnings)
	}
	if calls != 2 {
		t.Fatalf("request count = %d, want first generation plus one format retry", calls)
	}
	if firstRequest.ReasoningEffort != "low" || firstRequest.MaxTokens != 4096 {
		t.Errorf("generation controls = effort %q, max_tokens %d", firstRequest.ReasoningEffort, firstRequest.MaxTokens)
	}
	if !strings.Contains(firstRequest.Messages[0].Content.(string), "禁止生成填空题") {
		t.Fatal("choice prompt does not explicitly forbid fill-in questions")
	}
}
