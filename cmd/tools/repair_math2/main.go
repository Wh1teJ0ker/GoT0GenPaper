package main

import (
	"context"
	"encoding/json"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"sort"

	"GoT0GenPaper/internal/models"
	"GoT0GenPaper/internal/pipeline/assembler"
)

type questionContent struct {
	Stem    string
	Options []string
	Answer  string
}

func main() {
	root, err := os.Getwd()
	if err != nil {
		panic(err)
	}
	var spec models.SpecTable
	if err := readJSON(filepath.Join(root, "data", "spec_table.json"), &spec); err != nil {
		panic(err)
	}
	cleanMath2Spec(&spec)
	if err := sortAndValidateSpecEntries(&spec); err != nil {
		panic(err)
	}
	contents := cleanMath2Contents()
	if len(contents) != len(spec.Entries) {
		panic(fmt.Sprintf("题面数量 %d 与细目表 %d 不一致", len(contents), len(spec.Entries)))
	}

	paper := models.ExamPaper{ID: "paper_math2_repaired", Subject: "数学二", SpecTable: spec}
	for _, entry := range spec.Entries {
		content, ok := contents[entry.ID]
		if !ok {
			panic("缺少题位 " + entry.ID)
		}
		difficulty := map[models.DifficultyBand]float64{
			models.BandEasy:   0.25,
			models.BandMedium: 0.50,
			models.BandHard:   0.75,
		}[entry.DiffBand]
		q := models.GeneratedQuestion{
			ID:         "gen_" + entry.ID,
			SpecID:     entry.ID,
			Stem:       content.Stem,
			Options:    content.Options,
			Answer:     content.Answer,
			Type:       entry.Type,
			Points:     append([]string(nil), entry.Points...),
			Score:      entry.Score,
			Difficulty: difficulty,
			Status:     "ready",
		}
		paper.Questions = append(paper.Questions, q)
		paper.Rubrics = append(paper.Rubrics, rubricFor(q))
		paper.TotalScore += q.Score
	}

	if err := assembler.ValidateReady(paper); err != nil {
		panic(err)
	}
	out, err := assembler.New().Assemble(context.Background(), paper)
	if err != nil {
		panic(err)
	}
	outDir := filepath.Join(root, "output")
	for name, data := range map[string]string{
		"exam.tex":         out.ExamLaTeX,
		"answers.tex":      out.AnswerLaTeX,
		"answer_sheet.tex": out.AnswerSheetTeX,
		"spec_table.md":    out.SpecTable,
	} {
		if err := os.WriteFile(filepath.Join(outDir, name), []byte(data), 0644); err != nil {
			panic(err)
		}
	}
	if err := writeJSON(filepath.Join(outDir, "paper.json"), paper); err != nil {
		panic(err)
	}
	fmt.Printf("已写入数学二修复版: %d 题, %.0f 分\n", len(paper.Questions), paper.TotalScore)
}

func sortAndValidateSpecEntries(spec *models.SpecTable) error {
	sort.SliceStable(spec.Entries, func(i, j int) bool {
		return spec.Entries[i].Index < spec.Entries[j].Index
	})
	for i, entry := range spec.Entries {
		if entry.Index != i+1 {
			return fmt.Errorf("细目表题号必须连续: 第 %d 项 index=%d", i+1, entry.Index)
		}
	}
	return nil
}

func rubricFor(q models.GeneratedQuestion) models.Rubric {
	descriptions := []string{"结论正确，并给出与题目相符的依据"}
	switch q.Type {
	case models.TypeChoice, models.TypeMultiChoice:
		descriptions = []string{"识别考查概念并建立正确判断", "完成必要计算或推理并选出正确选项"}
	case models.TypeFillBlank:
		descriptions = []string{"列出正确公式或方法", "计算并化简得到填空结果"}
	case models.TypeMajor:
		descriptions = []string{"建立正确的解题模型或方程", "完成关键推导、计算或证明", "写出结论并说明适用条件"}
	}
	items := make([]models.RubricItem, 0, len(descriptions))
	remaining := q.Score
	for i, description := range descriptions {
		part := math.Round(q.Score/float64(len(descriptions))*100) / 100
		if i == len(descriptions)-1 {
			part = math.Round(remaining*100) / 100
		}
		remaining -= part
		items = append(items, models.RubricItem{ID: fmt.Sprintf("chk_%d", i+1), Description: description, MaxScore: part})
	}
	return models.Rubric{
		ID:                "rubric_" + q.ID,
		QuestionID:        q.ID,
		Items:             items,
		AcceptableMethods: []string{"标准教材方法；等价的规范解法"},
		Version:           1,
	}
}

func cleanMath2Contents() map[string]questionContent {
	contents := map[string]questionContent{
		"spec_001": {
			Stem:    `设 $A$ 为 $3$ 阶矩阵，$E$ 为 $3$ 阶单位矩阵。若 $A^3-3A^2+3A-E=O$，则下列结论正确的是（　　）。`,
			Options: []string{`$A-E$ 不可逆且 $A+E$ 可逆`, `$A-E$ 与 $A+E$ 均可逆`, `$A-E$ 可逆且 $A+E$ 不可逆`, `$A-E$ 与 $A+E$ 均不可逆`},
			Answer:  `由 $(A-E)^3=O$，可知 $A-E$ 的特征值全为 $0$，故 $A-E$ 不可逆。又 $A+E$ 的特征值全为 $2$，故 $A+E$ 可逆。因此选 A。`,
		},
		"spec_002": {
			Stem:    `设向量组 $\alpha_1=(1,0,1)^T,\ \alpha_2=(0,1,1)^T,\ \alpha_3=(1,1,a)^T$，且 $\beta=(1,1,2)^T$。若 $\beta$ 可由 $\alpha_1,\alpha_2,\alpha_3$ 线性表示且表示不唯一，则 $a$ 的值为（　　）。`,
			Options: []string{`$0$`, `$1$`, `$2$`, `$3$`},
			Answer:  `因为 $\beta=\alpha_1+\alpha_2$，要使表示不唯一，向量组必须线性相关。由 $\alpha_3=\alpha_1+\alpha_2$ 得 $a=2$，故选 C。`,
		},
		"spec_003": {
			Stem:    `设函数 $f(x)$ 在 $x=0$ 处可导，且 $f(0)=0$，$f'(0)=2$，则 $\displaystyle\lim_{x\to0}\frac{f(x)}{x}$ 的值为（　　）。`,
			Options: []string{`$0$`, `$1$`, `$2$`, `$4$`},
			Answer:  `由导数定义，$f'(0)=\lim_{x\to0}\frac{f(x)-f(0)}{x}=\lim_{x\to0}\frac{f(x)}{x}=2$，故选 C。`,
		},
		"spec_004": {
			Stem:    `设 $f(x)$ 在 $x=1$ 处可导，且 $f'(1)=2$。令 $y=f(\cos x)$，则 $y'(0)$ 等于（　　）。`,
			Options: []string{`$0$`, `$2$`, `$-2$`, `不存在`},
			Answer:  `由链式法则，$y' = f'(\cos x)(-\sin x)$。在 $x=0$ 时，$\cos0=1$ 且 $\sin0=0$，故 $y'(0)=0$，选 A。`,
		},
		"spec_005": {
			Stem:    `设 $F(x)=\displaystyle\int_0^x t\,dt$，则 $F'(1)$ 等于（　　）。`,
			Options: []string{`$0$`, `$1$`, `$2$`, `$\frac12$`},
			Answer:  `由变限积分求导公式，$F'(x)=x$，所以 $F'(1)=1$，故选 B。`,
		},
		"spec_006": {
			Stem:    `设函数 $z=z(x,y)$ 由方程 $x^2+2y^2+3z^2=6$ 确定，则 $z$ 在点 $(1,1,1)$ 处的全微分 $dz$ 为（　　）。`,
			Options: []string{`$-\frac13dx-\frac23dy$`, `$-\frac13dx+\frac23dy$`, `$\frac13dx-\frac23dy$`, `$\frac13dx+\frac23dy$`},
			Answer:  `令 $F=x^2+2y^2+3z^2-6$。由 $F_x+F_z z_x=0$、$F_y+F_z z_y=0$，得 $z_x=-x/(3z)$、$z_y=-2y/(3z)$。在 $(1,1,1)$ 处，$dz=-\frac13dx-\frac23dy$，故选 A。`,
		},
		"spec_007": {
			Stem:    `设函数 $f(x)$ 在 $x=0$ 处可导，且 $f(0)=0$，$f'(0)=2$，则 $\displaystyle\lim_{x\to0}\frac{f(1-\cos x)}{x^2}$ 等于（　　）。`,
			Options: []string{`$1$`, `$2$`, `$0$`, `$\frac12$`},
			Answer:  `由 $f(u)=2u+o(u)$ 及 $1-\cos x\sim x^2/2$，有 $f(1-\cos x)/x^2\to2\cdot(1/2)=1$，故选 A。`,
		},
		"spec_008": {
			Stem:    `计算定积分 $\displaystyle\int_0^1 x e^{x^2}\,dx$，其值为（　　）。`,
			Options: []string{`$e-1$`, `$\frac{e-1}{2}$`, `$\frac12$`, `$e$`},
			Answer:  `令 $u=x^2$，则 $du=2x\,dx$，所以 $\int_0^1xe^{x^2}\,dx=\frac12\int_0^1e^u\,du=\frac{e-1}{2}$，故选 B。`,
		},
		"spec_009": {
			Stem:    `设 $A$ 为 $3$ 阶幂等矩阵，即 $A^2=A$，且 $r(A)=2$，则 $|A+E|$ 等于（　　）。`,
			Options: []string{`$0$`, `$1$`, `$2$`, `$4$`},
			Answer:  `幂等矩阵的特征值只能为 $0$ 或 $1$。由 $r(A)=2$，特征值为 $1$ 的个数为 $2$，另一个为 $0$，故 $A+E$ 的特征值为 $2,2,1$，所以 $|A+E|=4$，选 D。`,
		},
		"spec_010": {
			Stem:    `设 $f(x)=x\ln x$（$x>0$），则 $f'(1)$ 等于（　　）。`,
			Options: []string{`$0$`, `$1$`, `$-1$`, `$\ln 2$`},
			Answer:  `由乘积求导法则，$f'(x)=\ln x+1$，故 $f'(1)=1$，选 B。`,
		},
		"spec_011": {
			Stem:   `曲面 $z=x^2+2y^2$ 在点 $P(1,1,3)$ 处的切平面方程为\underline{\hspace{5cm}}。`,
			Answer: `令 $F=x^2+2y^2-z$，则 $F_x(1,1,3)=2$，$F_y(1,1,3)=4$，$F_z=-1$。切平面为 $2(x-1)+4(y-1)-(z-3)=0$，即 $2x+4y-z-3=0$。`,
		},
		"spec_012": {
			Stem:   `设 $z=z(x,y)$ 由方程 $x^2+y^2+z^2=3$ 确定，则 $\left.\dfrac{\partial z}{\partial x}\right|_{(1,1)}$ 等于\underline{\hspace{3cm}}。`,
			Answer: `对方程求偏导得 $2x+2z z_x=0$，所以 $z_x=-x/z$。在点 $(1,1,1)$ 处，$z_x=-1$。`,
		},
		"spec_013": {
			Stem:   `设 $y=\ln(x+\sqrt{1+x^2})$，则 $\left.\dfrac{dy}{dx}\right|_{x=1}$ 等于\underline{\hspace{3cm}}。`,
			Answer: `求导得 $y'=1/\sqrt{1+x^2}$，所以 $y'(1)=1/\sqrt2=\sqrt2/2$。`,
		},
		"spec_014": {
			Stem:   `微分方程 $y'-\dfrac{y}{x}=1$ 满足初始条件 $y(1)=1$ 的特解为 $y=$\underline{\hspace{4cm}}。`,
			Answer: `这是线性方程。积分因子为 $1/x$，故 $(y/x)'=1/x$。积分得 $y/x=\ln x+C$，由 $y(1)=1$ 得 $C=1$，所以 $y=x(1+\ln x)$。`,
		},
		"spec_015": {
			Stem:   `微分方程 $xy'+y=x^2y^2$ 满足初始条件 $y(1)=1$ 的特解为 $y=$\underline{\hspace{3.2cm}}。`,
			Answer: `令 $z=1/y$，则 $z'-z/x=-x$。乘以积分因子 $1/x$ 得 $(z/x)'=-1$，由 $z(1)=1$ 得 $z=2x-x^2$，故 $y=1/(2x-x^2)$。`,
		},
		"spec_016": {
			Stem:   `设矩阵 $A=\begin{pmatrix}1&1&0\\1&a&0\\0&0&2\end{pmatrix}$，若 $r(A)=2$，则 $a=$\underline{\hspace{3cm}}。`,
			Answer: `矩阵 $A$ 是左上角二阶矩阵 $B=\begin{pmatrix}1&1\\1&a\end{pmatrix}$ 与数 $2$ 的分块对角矩阵，因此 $r(A)=r(B)+1$。要使 $r(A)=2$，须有 $r(B)=1$。由 $|B|=a-1=0$ 得 $a=1$；此时 $B$ 非零且秩为 $1$，条件成立。`,
		},
		"spec_017": {
			Stem:   `判断级数 $\displaystyle\sum_{n=1}^{\infty}\frac{(-1)^{n-1}}{\sqrt n}$ 的敛散性，并判断其绝对收敛性。`,
			Answer: `令 $b_n=1/\sqrt n$，则 $b_n\downarrow0$，由交错级数判别法，原级数收敛。另一方面，绝对值级数为 $\sum1/\sqrt n$，这是 $p=1/2$ 的正项级数，发散。因此原级数条件收敛。`,
		},
		"spec_018": {
			Stem:   `设一阶常微分方程初值问题 $y'+2y=e^{-x}$，$y(0)=1$。求解 $y=y(x)$，并求曲线与 $x=0$、$x=1$ 及 $x$ 轴所围图形的面积。`,
			Answer: `积分因子为 $e^{2x}$，故 $(e^{2x}y)'=e^x$。积分得 $y=e^{-x}+Ce^{-2x}$，由 $y(0)=1$ 得 $C=0$。在 $[0,1]$ 上 $y=e^{-x}>0$，面积为 $S=\int_0^1e^{-x}dx=1-e^{-1}$。`,
		},
		"spec_019": {
			Stem:   `设函数 $z=f(x,y)$ 可微，且 $f(1,1)=1$，$f_x(1,1)=2$，$f_y(1,1)=-1$。若 $g(x)=f(x,f(x,x))$，求 $g'(1)$。`,
			Answer: `由链式法则，$g'(x)=f_x(x,f(x,x))+f_y(x,f(x,x))[f_x(x,x)+f_y(x,x)]$。在 $x=1$ 时，$f(1,1)=1$，故 $g'(1)=2+(-1)(2-1)=1$。`,
		},
		"spec_020": {
			Stem:   `求微分方程 $y''-4y'+13y=0$ 的通解，并求满足 $y(0)=0$、$y'(0)=3$ 的特解。`,
			Answer: `特征方程为 $r^2-4r+13=0$，根为 $2\pm3i$，故通解为 $y=e^{2x}(C_1\cos3x+C_2\sin3x)$。由 $y(0)=0$ 得 $C_1=0$，再由 $y'(0)=3$ 得 $C_2=1$，所以特解为 $y=e^{2x}\sin3x$。`,
		},
		"spec_021": {
			Stem:   `设区域 $D=\{(x,y)\mid 1\le x^2+y^2\le4,\ y\ge0\}$，计算二重积分 $I=\iint_D\frac{x^2+y^2}{1+x^2+y^2}\,dx\,dy$。`,
			Answer: `区域 $D$ 是上半圆环，采用极坐标 $x=r\cos\theta$、$y=r\sin\theta$，有 $1\le r\le2$、$0\le\theta\le\pi$。因此 $I=\int_0^\pi d\theta\int_1^2\frac{r^2}{1+r^2}r\,dr=\pi\int_1^2\frac{r^3}{1+r^2}\,dr$。令 $u=1+r^2$，则 $I=\frac{\pi}{2}\int_2^5(1-\frac1u)\,du=\frac{\pi}{2}(3-\ln\frac52)$。`,
		},
		"spec_022": {
			Stem:   `设二次型 $f(x_1,x_2,x_3)=x_1^2+4x_2^2+x_3^2+4x_1x_2+2x_1x_3+4x_2x_3$。写出对应矩阵，求正交变换化标准形，并求正、负惯性指数及符号差。`,
			Answer: `对应矩阵为 $A=\begin{pmatrix}1&2&1\\2&4&2\\1&2&1\end{pmatrix}=vv^T$，其中 $v=(1,2,1)^T$。矩阵 $A$ 的特征值为 $6,0,0$。取 $q_1=(-2,1,0)^T/\sqrt5$、$q_2=(-1,-2,5)^T/\sqrt{30}$、$q_3=(1,2,1)^T/\sqrt6$，令 $Q=(q_1,q_2,q_3)$，则 $Q$ 为正交矩阵且 $Q^TAQ=\operatorname{diag}(0,0,6)$。作 $x=Qy$，得 $f=6y_3^2$。因此正惯性指数为 $1$，负惯性指数为 $0$，符号差为 $1$。`,
		},
	}
	return reorderMath2ChoiceContents(contents)
}

func reorderMath2ChoiceContents(contents map[string]questionContent) map[string]questionContent {
	// The current math2 blueprint places seven calculus choices before three
	// linear-algebra choices; the original repair set used a different order.
	sources := map[string]string{
		"spec_001": "spec_003",
		"spec_002": "spec_004",
		"spec_003": "spec_005",
		"spec_004": "spec_006",
		"spec_005": "spec_007",
		"spec_006": "spec_008",
		"spec_007": "spec_010",
		"spec_008": "spec_001",
		"spec_009": "spec_002",
		"spec_010": "spec_009",
	}
	reordered := make(map[string]questionContent, len(contents))
	for id, source := range sources {
		reordered[id] = contents[source]
	}
	for id, content := range contents {
		if _, moved := sources[id]; !moved {
			reordered[id] = content
		}
	}
	return reordered
}

func cleanMath2Spec(spec *models.SpecTable) {
	points := map[string][]string{
		"spec_001": {"高等数学", "函数、极限、连续", "导数定义"},
		"spec_002": {"高等数学", "一元函数微分学", "复合函数求导"},
		"spec_003": {"高等数学", "一元函数积分学", "变限积分"},
		"spec_004": {"高等数学", "多元函数微分学", "全微分的概念与计算"},
		"spec_005": {"高等数学", "函数、极限、连续", "等价无穷小"},
		"spec_006": {"高等数学", "一元函数积分学", "定积分计算"},
		"spec_007": {"高等数学", "一元函数微分学", "导数的计算"},
		"spec_008": {"线性代数", "矩阵", "矩阵的幂零性"},
		"spec_015": {"高等数学", "常微分方程", "其他方程"},
		"spec_016": {"线性代数", "矩阵", "矩阵的秩"},
		"spec_010": {"线性代数", "矩阵", "幂等矩阵"},
		"spec_021": {"高等数学", "多元函数积分学", "重积分的计算"},
		"spec_022": {"线性代数", "二次型", "正交变换与规范形"},
	}
	for i := range spec.Entries {
		entry := &spec.Entries[i]
		if replacement, ok := points[entry.ID]; ok {
			entry.Points = replacement
			entry.TemplateKey = string(entry.Type) + "_" + replacement[0]
		}
	}
	for id, score := range map[string]float64{"spec_017": 10, "spec_018": 12} {
		for i := range spec.Entries {
			if spec.Entries[i].ID == id {
				spec.Entries[i].Score = score
			}
		}
	}
	for i := range spec.Entries {
		if spec.Entries[i].ID == "spec_021" {
			spec.Entries[i].Cognitive = models.CogApply
			spec.Entries[i].DiffBand = models.BandMedium
		}
	}
	if spec.Subject == "" {
		spec.Subject = "数学二"
	}
}

func readJSON(path string, out interface{}) error {
	data, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	return json.Unmarshal(data, out)
}

func writeJSON(path string, value interface{}) error {
	data, err := json.MarshalIndent(value, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(path, data, 0644)
}
