package quality

import (
	"reflect"
	"strings"
	"testing"

	"GoT0GenPaper/internal/models"
)

// specTopicPoints lists, for each reviewed repair, the knowledge points the
// repair was written for. Tests must supply them because RepairKnownQuestion
// only applies a repair when the slot still carries its topic.
var specTopicPoints = map[string][]string{
	"spec_001": {"数据结构", "B树", "B+树"},
	"spec_003": {"数据结构", "数组查找", "分块查找"},
	"spec_004": {"数据结构", "特殊矩阵", "三元组表"},
	"spec_008": {"数据结构", "堆的概念", "插入排序"},
	"spec_009": {"数据结构", "平均查找长度"},
	"spec_012": {"组成原理", "中断屏蔽", "异常和中断"},
	"spec_013": {"组成原理", "单周期处理器", "总线类型"},
	"spec_014": {"组成原理", "多级中断", "中断屏蔽"},
	"spec_017": {"组成原理", "CHS地址", "DMA"},
	"spec_019": {"组成原理", "虚拟页式管理", "cache映射方式", "缺页异常"},
	"spec_020": {"组成原理", "虚拟页式管理", "地址翻译", "cache概念", "TLB"},
	"spec_021": {"组成原理", "计算机性能指标", "cache概念", "DMA", "主存容量的扩展"},
	"spec_023": {"操作系统", "死锁预防", "临界资源"},
	"spec_025": {"操作系统", "管程", "条件变量"},
	"spec_026": {"操作系统", "系统调用", "进程文件管理"},
	"spec_027": {"操作系统", "进程的互斥", "临界资源"},
	"spec_028": {"操作系统", "页面置换算法", "LRU"},
	"spec_030": {"操作系统", "TLB", "地址翻译", "虚拟页式管理"},
	"spec_032": {"操作系统", "机械硬盘", "CHS地址", "IO软件层次"},
	"spec_035": {"计算机网络", "OSI模型", "网络设备"},
	"spec_036": {"计算机网络", "TCP", "IP模型"},
	"spec_037": {"计算机网络", "差错控制", "循环冗余码"},
	"spec_039": {"计算机网络", "香农定理", "通信指标"},
	"spec_042": {"数据结构", "B树", "B+树"},
	"spec_043": {"组成原理", "RAID"},
	"spec_044": {"组成原理", "总线定时"},
	"spec_046": {"操作系统", "CHS地址", "IO软件层次"},
	"spec_047": {"计算机网络", "FTP"},
}

func topicQuestion(specID string) models.GeneratedQuestion {
	return models.GeneratedQuestion{SpecID: specID, Points: append([]string(nil), specTopicPoints[specID]...)}
}

func repairedQuestion(t *testing.T, specID string) models.GeneratedQuestion {
	t.Helper()
	q, changed := RepairKnownQuestion(topicQuestion(specID))
	if !changed {
		t.Fatalf("%s was not repaired", specID)
	}
	if q.Status != "ready" || len(q.Warnings) != 0 {
		t.Fatalf("%s status metadata was not normalized: status=%q warnings=%v", specID, q.Status, q.Warnings)
	}
	return q
}

// TestRepairKnownQuestionRequiresMatchingTopic guards the safety property: a
// SpecID only identifies a topic inside the paper it was composed for, so a
// repair must never touch a slot whose knowledge points changed.
func TestRepairKnownQuestionRequiresMatchingTopic(t *testing.T) {
	unrelated := models.GeneratedQuestion{SpecID: "spec_001", Points: []string{"计算机网络", "TCP", "拥塞控制"}}
	if repaired, changed := RepairKnownQuestion(unrelated); changed {
		t.Fatalf("B-tree repair leaked into an unrelated topic: %#v", repaired)
	}
	// The reviewed flag must not mark an unrelated slot ready either.
	unrelated.Status = "needs_review"
	unrelated.Warnings = []string{"占位内容"}
	if repaired, changed := RepairKnownQuestion(unrelated); changed || repaired.Status == "ready" {
		t.Fatalf("unrelated slot was marked reviewed: %#v", repaired)
	}
	for specID := range specTopicPoints {
		if len(knownRepairGuard(specID)) == 0 {
			t.Errorf("%s has topic points but no guard", specID)
		}
	}
}

func TestRepairKnownQuestionBTree(t *testing.T) {
	q := repairedQuestion(t, "spec_001")
	if !strings.Contains(q.Stem, "关键字数超过 2 个") || !strings.HasPrefix(q.Answer, "答案：D") {
		t.Fatalf("B-tree repair is not self-consistent: stem=%q answer=%q", q.Stem, q.Answer)
	}
	if len(q.Options) != 4 || q.Options[3] != "40" {
		t.Fatalf("B-tree options do not contain the correct root key: %v", q.Options)
	}
}

func TestRepairKnownQuestionUsesExplicitZeroBasedIndex(t *testing.T) {
	q := repairedQuestion(t, "spec_004")
	if !strings.Contains(q.Stem, "下标为 $3$") || !strings.HasPrefix(q.Answer, "答案：C") {
		t.Fatalf("triple-table repair did not remove indexing ambiguity: stem=%q answer=%q", q.Stem, q.Answer)
	}
	for _, option := range q.Options {
		if strings.Contains(option, "$") {
			t.Fatalf("option contains raw math delimiters that render literally: %q", option)
		}
	}
}

func TestRepairKnownQuestionAddressPartition(t *testing.T) {
	q := repairedQuestion(t, "spec_020")
	joined := strings.Join(append([]string{q.Stem, q.Answer}, q.Options...), "\n")
	if strings.Contains(joined, "0xABCDEF00") || !strings.HasPrefix(q.Answer, "答案：B") || !strings.Contains(joined, "0x2BCDEF00") {
		t.Fatalf("address-partition repair is invalid: %s", joined)
	}
}

func TestRepairKnownQuestionUnifiesAnswers(t *testing.T) {
	for _, tc := range []struct {
		specID string
		answer string
		bad    string
	}{
		{"spec_027", "答案：B", "答案：A"},
		{"spec_028", "答案：D", "答案：C"},
		{"spec_030", "答案：A", "答案：B"},
		{"spec_032", "答案：B", "答案：D"},
	} {
		q := repairedQuestion(t, tc.specID)
		if !strings.HasPrefix(q.Answer, tc.answer) || strings.HasPrefix(q.Answer, tc.bad) {
			t.Fatalf("%s answer was not unified: %q", tc.specID, q.Answer)
		}
	}
}

func TestRepairKnownQuestionMakesFTPDirectionConsistent(t *testing.T) {
	q := repairedQuestion(t, "spec_047")
	if strings.Contains(q.Answer, "重新设计") || strings.Contains(q.Answer, "题干存在理解偏差") || !strings.Contains(q.Stem, "转发后的报文段") {
		t.Fatalf("FTP repair still contains drafting text or lacks corrected subquestion: stem=%q answer=%q", q.Stem, q.Answer)
	}
	if !strings.Contains(q.Stem, "50000") || !strings.Contains(q.Stem, "60001") {
		t.Fatalf("FTP repair must state the NAT mapping endpoints: %q", q.Stem)
	}
}

func TestRepairKnownQuestionFixesInterruptEntryOrder(t *testing.T) {
	q := repairedQuestion(t, "spec_012")
	if !strings.Contains(q.Stem, "开始执行（被响应进入）的先后顺序") ||
		!strings.HasPrefix(q.Answer, "答案：B") ||
		!strings.Contains(q.Answer, `$I_2\to I_4\to I_3$`) {
		t.Fatalf("interrupt sequence is inconsistent: stem=%q answer=%q", q.Stem, q.Answer)
	}
	if strings.Contains(q.Stem, "CPU响应 $I_4$ 中断") {
		t.Fatalf("stem still contradicts the higher hardware priority: %q", q.Stem)
	}
}

func TestRepairKnownQuestionUsesCorrectInterruptMaskBitOrder(t *testing.T) {
	q := repairedQuestion(t, "spec_014")
	for _, want := range []string{"$I_4:1111$", "$I_1:1110$", "$I_3:0110$", "$I_2:0100$"} {
		if !strings.Contains(q.Answer, want) {
			t.Fatalf("interrupt mask %q missing from answer: %q", want, q.Answer)
		}
	}
	for _, wrong := range []string{"$I_1$ 次之，屏蔽", "$I_3$ 再次之，屏蔽", "屏蔽字为 0001"} {
		if strings.Contains(q.Answer, wrong) {
			t.Fatalf("stale mask explanation remains: %q", wrong)
		}
	}
}

func TestRepairKnownQuestionClarifiesRoutedIPLayer(t *testing.T) {
	q := repairedQuestion(t, "spec_035")
	if !strings.Contains(q.Stem, "另一 IP 网络") || !strings.HasPrefix(q.Answer, "答案：C") ||
		!strings.Contains(q.Answer, "IP 数据报属于网络层") {
		t.Fatalf("OSI layer question remains ambiguous: stem=%q answer=%q", q.Stem, q.Answer)
	}
}

func TestRepairKnownQuestionsUseConsistentDiskAddressOwnership(t *testing.T) {
	choice := repairedQuestion(t, "spec_032")
	major := repairedQuestion(t, "spec_046")
	for _, q := range []models.GeneratedQuestion{choice, major} {
		joined := q.Stem + "\n" + q.Answer
		if !strings.Contains(joined, "LBA") || !strings.Contains(joined, "设备驱动程序") ||
			!strings.Contains(joined, "设备独立性软件") {
			t.Fatalf("disk address layers are not distinguished in %s: %s", q.SpecID, joined)
		}
		if strings.Contains(joined, "设备独立性软件负责将逻辑块号转换为物理设备的 CHS") {
			t.Fatalf("%s still assigns CHS conversion to device-independent software", q.SpecID)
		}
	}
	if !strings.Contains(major.Answer, "$(12,6,35)$") {
		t.Fatalf("LBA-to-CHS arithmetic changed unexpectedly: %q", major.Answer)
	}
}

func TestRepairKnownQuestionStatesShannonCapacityDirectly(t *testing.T) {
	q := repairedQuestion(t, "spec_039")
	if !strings.Contains(q.Stem, "香农公式") || strings.Contains(q.Stem, "多相相位调制") ||
		!strings.HasPrefix(q.Answer, "答案：A") || !strings.Contains(q.Answer, "29\\,901") {
		t.Fatalf("Shannon capacity question is not self-contained: stem=%q answer=%q", q.Stem, q.Answer)
	}
}

func TestRepairAdditional408Questions(t *testing.T) {
	tests := []struct {
		specID string
		answer string
		stem   string
		bad    []string
	}{
		{"spec_003", "答案：C", "ASL", []string{"索引表长度为 $12$"}},
		{"spec_008", "答案：A", "H[4]=49", []string{"答案：B"}},
		{"spec_009", "答案：A", "向下取整", nil},
		{"spec_013", "答案：B", "三组数据传送", nil},
		{"spec_012", "答案：B", "I_2\\to I_4\\to I_3", []string{"答案：C"}},
		{"spec_014", "答案：B", "I_1:1110", []string{"I_1$ 次之，屏蔽"}},
		{"spec_017", "答案：B", "$(12,6,35)$", []string{"磁道间寻道时间"}},
		{"spec_019", "答案：A", "0xA6", nil},
		{"spec_021", "答案：A", "不同的主存块", []string{"无法命中"}},
		{"spec_023", "答案：B", "$12$ 个同类", []string{"Wait,", "Let's"}},
		{"spec_025", "答案：A", "Hoare 风格", []string{"empty", "full", "信号量"}},
		{"spec_026", "答案：C", "每个块指针占 $4", []string{"指针大小均为"}},
		{"spec_036", "答案：A", "FlightSize", nil},
		{"spec_037", "答案：B", "余数为 $0010$", []string{"余数为 1010"}},
		{"spec_042", "答案与解析：", "根结点 $[20,35]$", []string{"关键步骤修正", "超过上限"}},
		{"spec_043", "答案与解析：", "33\\,554\\,432", []string{"131072"}},
		{"spec_044", "答案与解析：", "12.5\\,\\mathrm{MHz}", []string{"(修正", "80\\,\\mathrm{MB/s}"}},
		{"spec_035", "答案：C", "网络层", nil},
		{"spec_039", "答案：A", "29\\,901", []string{"多相相位调制"}},
		{"spec_046", "考查目标与判据：", "$(12,6,35)$", []string{"设备独立性软件层中，需将逻辑块号转换为 CHS"}},
		{"spec_047", "答案：", "最后一段为 $800\\,\\mathrm{B}$", []string{"10.905", "25\\,712.945"}},
	}
	for _, tc := range tests {
		t.Run(tc.specID, func(t *testing.T) {
			q := repairedQuestion(t, tc.specID)
			content := strings.Join(append([]string{q.Stem, q.Answer}, q.Options...), "\n")
			if tc.answer != "" && !strings.HasPrefix(q.Answer, tc.answer) {
				t.Fatalf("answer should start with %q, got %q", tc.answer, q.Answer)
			}
			if !strings.Contains(content, tc.stem) {
				t.Fatalf("repaired content missing %q: %s", tc.stem, content)
			}
			for _, bad := range tc.bad {
				if strings.Contains(content, bad) {
					t.Fatalf("repaired content still contains %q", bad)
				}
			}
			again, changed := RepairKnownQuestion(q)
			if changed || !reflect.DeepEqual(again, q) {
				t.Fatal("repair must be idempotent")
			}
		})
	}
}

func TestRepairCHSAndDMAKeepsSpecPoints(t *testing.T) {
	question := topicQuestion("spec_017")
	points := append([]string(nil), question.Points...)
	q, changed := RepairKnownQuestion(question)
	if !changed {
		t.Fatal("spec_017 repair did not change the question")
	}
	if !reflect.DeepEqual(q.Points, points) {
		t.Fatalf("repair changed blueprint points: got %v, want %v", q.Points, points)
	}
	if q.Status != "ready" || !strings.Contains(q.Stem, "LBA $10234$") ||
		!strings.Contains(q.Answer, "设置传输参数并启动") || !strings.Contains(q.Answer, "$(12,6,35)$") {
		t.Fatalf("spec_017 does not cover CHS and DMA correctly: %+v", q)
	}
}

func TestRepairDiskRangeFormatting(t *testing.T) {
	question := topicQuestion("spec_046")
	question.Stem = "柱面号 0~199，磁头号 0~7，扇区号 1~100。"
	q, changed := RepairKnownQuestion(question)
	if !changed || !strings.Contains(q.Stem, "$0$ 至 $199$") ||
		!strings.Contains(q.Stem, "$0$ 至 $7$") || !strings.Contains(q.Stem, "$1$ 至 $100$") ||
		strings.Contains(q.Stem, "~") {
		t.Fatalf("disk address range should render as ordinary text: stem=%q changed=%v", q.Stem, changed)
	}
}

func TestRepairKnownQuestionIsIdempotent(t *testing.T) {
	q, changed := RepairKnownQuestion(topicQuestion("spec_028"))
	if !changed {
		t.Fatal("first repair did not change the question")
	}
	q2, changedAgain := RepairKnownQuestion(q)
	if changedAgain || !reflect.DeepEqual(q2, q) {
		t.Fatalf("repair should produce the same normalized result on repeated application")
	}
}

func TestMathReviewedFixesAreTargeted(t *testing.T) {
	defective := models.GeneratedQuestion{
		SpecID: "spec_019", Type: models.TypeMajor, Score: 12,
		Points: []string{"高等数学", "常微分方程", "常系数齐次线性微分方程"},
		Status: "ready",
		Stem:   "设二阶常系数线性微分方程的通解为 $y=(C_1+C_2 x)e^{-2x}$，求最大值。",
		Answer: "答案：最大值为 $\\dfrac{1}{e}$。\n\n解析：求导后最大值为 $y(0)=1$。",
	}
	fixed, changed := RepairKnownQuestion(defective)
	if !changed {
		t.Fatal("reviewed math defect was not repaired")
	}
	if !strings.Contains(fixed.Answer, "最大值为 $1$") || strings.Contains(fixed.Answer, "\\dfrac{1}{e}") {
		t.Errorf("summary still wrong: %q", fixed.Answer)
	}
	if again, changedAgain := RepairKnownQuestion(fixed); changedAgain {
		t.Errorf("repair is not idempotent: %q", again.Answer)
	}
	if !IsReviewedRepairSatisfied(fixed) {
		t.Error("repaired math slot should count as satisfied")
	}

	// A slot that merely shares the knowledge point but carries different
	// content must not be rewritten.
	other := models.GeneratedQuestion{
		SpecID: "spec_019", Points: defective.Points, Status: "ready",
		Stem: "另一道题", Answer: "答案：最大值为 $\\dfrac{1}{4}$。",
	}
	if out, changed := RepairKnownQuestion(other); changed || out.Answer != other.Answer {
		t.Errorf("unrelated content was rewritten: %q", out.Answer)
	}

	// A slot with a CS topic never matches a math fix even if the text is present.
	cs := models.GeneratedQuestion{
		SpecID: "spec_099", Points: []string{"操作系统", "虚拟页式管理", "缺页异常"}, Status: "ready",
		Stem: "缺页处理", Answer: "答案：最大值为 $\\dfrac{1}{e}$。",
	}
	if out, changed := RepairKnownQuestion(cs); changed {
		t.Errorf("math fix leaked into a 408 slot: %q", out.Answer)
	}
}
