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

func TestFinishNormalizesMicrosecondUnitsForLaTeX(t *testing.T) {
	g := New(nil, nil)
	q, err := g.finish(choiceSpec(models.TypeChoice), generationResponse{
		Stem:       `读取时间为 120\mu s，计算 $T=30\mu s$。`,
		Options:    []string{`1500\mu s`, `1590\mu s`, `1620\mu s`, `2400\mu s`},
		Answer:     `代入 $T=120\mu s$，总时间为 1590\mu s。`,
		Difficulty: 0.5,
	})
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(q.Stem, `120\mu s`) || !strings.Contains(q.Stem, `$120\,\mu\mathrm{s}$`) {
		t.Errorf("plain-text unit was not wrapped as math: %q", q.Stem)
	}
	if !strings.Contains(q.Stem, `$T=30\,\mu\mathrm{s}$`) {
		t.Errorf("existing math mode was not preserved: %q", q.Stem)
	}
	if !strings.Contains(q.Options[0], `$1500\,\mu\mathrm{s}$`) {
		t.Errorf("choice option unit was not normalized: %q", q.Options[0])
	}
	if !strings.Contains(q.Answer, `$T=120\,\mu\mathrm{s}$`) || !strings.Contains(q.Answer, `$1590\,\mu\mathrm{s}$`) {
		t.Errorf("answer units were not normalized: %q", q.Answer)
	}
}

func TestNormalizeGeneratedLaTeXWrapsCommonAddressBusMathOutsideMathMode(t *testing.T) {
	input := `CPU 的高位地址线 A15\sim A2 产生片选信号 \overline{CS}。现已知 $A_3\sim A_0$。`
	got := NormalizeGeneratedLaTeX(input)
	if !strings.Contains(got, `$A_{15}\sim A_{2}$`) || !strings.Contains(got, `$\overline{CS}$`) {
		t.Fatalf("bare address-bus math was not wrapped: %q", got)
	}
	if !strings.Contains(got, `$A_3\sim A_0$`) {
		t.Fatalf("existing math mode was changed: %q", got)
	}
}

func TestNormalizeGeneratedLaTeXRepairsTabCorruptedMathCommand(t *testing.T) {
	got := NormalizeGeneratedLaTeX("柱面编号 0$\thicksim$99")
	if got != "柱面编号 0$\\sim$99" {
		t.Fatalf("tab-corrupted math command = %q, want restored sim command", got)
	}
}

func TestNormalizeGeneratedLaTeXUsesReadableRegisterRange(t *testing.T) {
	got := NormalizeGeneratedLaTeX("通用寄存器R0~R3")
	if got != "通用寄存器R0 至 R3" {
		t.Fatalf("register range = %q, want readable text range", got)
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

func Test408BPlusTreePromptDefinesOrderAndSplitRules(t *testing.T) {
	spec := models.SpecEntry{Type: models.TypeMajor, Points: []string{"408", "数据结构", "B+树"}, Score: 15}
	prompt := New(nil, nil).buildSystemPrompt(spec)
	for _, required := range []string{"阶数 m 定义为每个内部结点的最大子女数", "最多 m-1 个关键字", "右叶首关键字复制到父结点", "仍保留在右叶", "子女数=关键字数+1", "禁止将B树删除/提升规则套用于B+叶分裂"} {
		if !strings.Contains(prompt, required) {
			t.Errorf("B+ tree prompt missing %q", required)
		}
	}
}

func Test408RIPPromptUsesHopCountAndSynchronousRounds(t *testing.T) {
	spec := models.SpecEntry{Type: models.TypeMajor, Points: []string{"408", "计算机网络", "RIP路由协议", "路由表更新"}, Score: 7}
	prompt := New(nil, nil).buildSystemPrompt(spec)
	for _, required := range []string{"每条相邻链路增加 1", "不得给链路另设 2 等任意开销", "只能从拓扑中直接相邻的路由器接收距离向量", "严禁让路由器收到非邻居的路由表", "每个路由器各有一个直连局域网 N1、N2", "初始仅本地 LAN 的距离为 1", "每轮只依据上一轮已通告向量计算", "距离为 k+1"} {
		if !strings.Contains(prompt, required) {
			t.Errorf("RIP prompt missing %q", required)
		}
	}
}

func Test408ChoicePromptRequiresOneConsistentAnswer(t *testing.T) {
	spec := models.SpecEntry{Type: models.TypeChoice, Points: []string{"408", "数据结构", "图的遍历"}, Score: 2}
	prompt := New(nil, nil).buildSystemPrompt(spec)
	for _, required := range []string{"先独立算出唯一结果", "首句与末句的答案字母必须相同", "全文只能给出一个最终答案", "不得保留自我纠错"} {
		if !strings.Contains(prompt, required) {
			t.Errorf("408 choice prompt missing %q", required)
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

func TestNormalizeMovesCJKPunctuationOutOfMath(t *testing.T) {
	cases := []struct {
		name string
		in   string
		want string
	}{
		{
			// Display math keeps its punctuation: moving it out would start the
			// next line with 。, which Chinese typography forbids, and the
			// preamble's CJKmath makes it render in place.
			name: "trailing period inside display math stays put",
			in:   "定义变限积分函数\n$$F(x)=\\int_{0}^{x}(x-t)f(t)\\,dt \\quad (x\\ge 0)。$$\n(1) 求导",
			want: "定义变限积分函数\n$$F(x)=\\int_{0}^{x}(x-t)f(t)\\,dt \\quad (x\\ge 0)。$$\n(1) 求导",
		},
		{
			name: "trailing period inside inline math moves out",
			in:   "故 $x=1。$ 成立",
			want: "故 $x=1$。 成立",
		},
		{
			name: "parentheses around inline math",
			in:   "若$（x）$取最大值",
			want: "若（$x$）取最大值",
		},
		{
			name: "punctuation outside math is untouched",
			in:   "中文。$x$。$y$，结束。",
			want: "中文。$x$。$y$，结束。",
		},
		{
			name: "escaped dollar is not a delimiter",
			in:   "价格为 \\$100。",
			want: "价格为 \\$100。",
		},
		{
			name: "line break bracket is not display math",
			in:   "第一行 \\\\[2mm] 第二行。",
			want: "第一行 \\\\[2mm] 第二行。",
		},
		{
			// Bracket display math behaves like $$…$$.
			name: "bracket display math keeps inner punctuation",
			in:   "\\[a=b。\\]",
			want: "\\[a=b。\\]",
		},
		{
			// Content normalized by an older version had the period pushed out;
			// pull it back so it cannot start the next line.
			name: "punctuation after display math is pulled back inside",
			in:   "$$F(x)=x \\quad (x\\ge 0)$$。\n(1) 求导",
			want: "$$F(x)=x \\quad (x\\ge 0)。$$\n(1) 求导",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := NormalizeGeneratedLaTeX(tc.in)
			if got != tc.want {
				t.Fatalf("NormalizeGeneratedLaTeX(%q) = %q, want %q", tc.in, got, tc.want)
			}
			if twice := NormalizeGeneratedLaTeX(got); twice != got {
				t.Fatalf("normalization is not idempotent: %q then %q", got, twice)
			}
		})
	}
}

func TestNormalizeReplacesTildeFillBlanks(t *testing.T) {
	cases := []struct{ in, want string }{
		// Stored form: the model wrote tildes as the blank.
		{`则 $\lambda=$ \underline{~~~~~~~~}。`, `则 $\lambda=$ \underline{\quad\quad}。`},
		// Rendered form: the assembler already escaped the tildes.
		{`则 $\lambda=$ \underline{\textasciitilde{}\textasciitilde{}}。`, `则 $\lambda=$ \underline{\quad\quad}。`},
		// A single tilde (or one escaped tilde) is prose, not a blank.
		{`区间为 $[0,~1]$`, `区间为 $[0,~1]$`},
		{`区间为 $[0,\textasciitilde{}1]$`, `区间为 $[0,\textasciitilde{}1]$`},
	}
	for _, tc := range cases {
		got := NormalizeGeneratedLaTeX(tc.in)
		if got != tc.want {
			t.Errorf("NormalizeGeneratedLaTeX(%q) = %q, want %q", tc.in, got, tc.want)
		}
		if twice := NormalizeGeneratedLaTeX(got); twice != got {
			t.Errorf("not idempotent: %q then %q", got, twice)
		}
	}
}

func TestSyncAnswerSummaryCopiesTrailingAnswer(t *testing.T) {
	cases := []struct {
		name string
		in   string
		want string
	}{
		{
			name: "wrong leading summary is replaced by the derived answer",
			in:   "答案：最大值为 $\\dfrac{1}{e}$。\n\n解析：\n求导得单调递减，故最大值为 $y(0)=1$。\n\n最终答案：最大值为 $1$",
			want: "答案：最大值为 $1$\n\n解析：\n求导得单调递减，故最大值为 $y(0)=1$。\n\n最终答案：最大值为 $1$",
		},
		{
			name: "already consistent summary is untouched",
			in:   "答案：B\n\n推导后选择 B。\n\n最终答案：B",
			want: "答案：B\n\n推导后选择 B。\n\n最终答案：B",
		},
		{
			name: "no trailing authoritative line means no rewrite",
			in:   "答案：B\n\n解析：选择 B。",
			want: "答案：B\n\n解析：选择 B。",
		},
		{
			name: "markdown emphasis keeps its label",
			in:   "**答案：$\\dfrac{7}{2}$**\n\n解析：得到 $2$。\n\n**最终答案：$2$**",
			want: "**答案：$2$**\n\n解析：得到 $2$。\n\n**最终答案：$2$**",
		},
		{
			name: "multi-branch answer is copied whole",
			in:   "答案：$\\mathrm{d}z=\\mathrm{d}x+\\mathrm{d}y$\n\n解析：两支为 $\\pm\\dfrac{1}{\\sqrt{2}}$。\n\n最终答案：$\\mathrm{d}z=\\pm\\dfrac{1}{\\sqrt{2}}(\\mathrm{d}x+\\mathrm{d}y)$",
			want: "答案：$\\mathrm{d}z=\\pm\\dfrac{1}{\\sqrt{2}}(\\mathrm{d}x+\\mathrm{d}y)$\n\n解析：两支为 $\\pm\\dfrac{1}{\\sqrt{2}}$。\n\n最终答案：$\\mathrm{d}z=\\pm\\dfrac{1}{\\sqrt{2}}(\\mathrm{d}x+\\mathrm{d}y)$",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := SyncAnswerSummary(tc.in); got != tc.want {
				t.Fatalf("SyncAnswerSummary(%q)\n got %q\nwant %q", tc.in, got, tc.want)
			}
			if twice := SyncAnswerSummary(SyncAnswerSummary(tc.in)); twice != SyncAnswerSummary(tc.in) {
				t.Fatalf("not idempotent: %q", twice)
			}
		})
	}
}

func TestSyncAnswerSummaryHandlesBoldLabelAndMissingHead(t *testing.T) {
	// Bold label with a bold payload: the label must parse and the payload sync.
	bold := "**答案**：(2) 极限值为 $0$。\n\n解析：得到 $\\dfrac{1}{3}$。\n\n最终答案：(2) 极限值为 $\\dfrac{1}{3}$"
	want := "**答案：(2) 极限值为 $\\dfrac{1}{3}$**\n\n解析：得到 $\\dfrac{1}{3}$。\n\n最终答案：(2) 极限值为 $\\dfrac{1}{3}$"
	if got := SyncAnswerSummary(bold); got != want {
		t.Errorf("bold label case:\n got %q\nwant %q", got, want)
	}

	// No leading summary at all: one is created from the trailing answer.
	missing := "逐步推导：\n$v = \\ln x + 1 + x$。\n\n最终答案：$y = \\dfrac{1}{\\ln x + x + 1}$"
	if got := SyncAnswerSummary(missing); got != "答案：$y = \\dfrac{1}{\\ln x + x + 1}$\n\n"+missing {
		t.Errorf("missing head case:\n got %q", got)
	}
	// Idempotent: the inserted line now equals the trailing answer.
	once := SyncAnswerSummary(missing)
	if twice := SyncAnswerSummary(once); twice != once {
		t.Errorf("not idempotent: %q", twice)
	}
}

func TestNormalizeSmartenQuotes(t *testing.T) {
	cases := []struct{ in, want string }{
		{`The phrase "changed their minds" suggests that the council`, `The phrase \textquotedblleft changed their minds\textquotedblright  suggests that the council`},
		{`他说“你好”`, `他说“你好”`},                     // Chinese quotes stay
		{`单位是 $L=1"$ 的英寸标记`, `单位是 $L=1"$ 的英寸标记`}, // quotes inside math stay
		{`只有一个 " 引号`, `只有一个 " 引号`},               // unpaired stays
	}
	for _, tc := range cases {
		if got := NormalizeGeneratedLaTeX(tc.in); got != tc.want {
			t.Errorf("NormalizeGeneratedLaTeX(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}
}

func TestNormalizeRepairsBacktickQuotes(t *testing.T) {
	in := "The phrase ``changed their minds'' suggests"
	want := `The phrase \textquotedblleft changed their minds\textquotedblright  suggests`
	if got := NormalizeGeneratedLaTeX(in); got != want {
		t.Fatalf("got %q, want %q", got, want)
	}
	// A lone opening pair is repaired too (its closing quote was already lost).
	lone := "他说 ``你好 就走了"
	if got := NormalizeGeneratedLaTeX(lone); !strings.Contains(got, `\textquotedblleft`) || strings.Contains(got, "``") {
		t.Fatalf("lone backtick quote not repaired: %q", got)
	}
}
