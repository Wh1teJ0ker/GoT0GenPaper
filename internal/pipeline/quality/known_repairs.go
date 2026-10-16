package quality

import (
	"reflect"
	"strings"

	"GoT0GenPaper/internal/models"
)

// knownRepairGuard lists the knowledge-point keywords each reviewed repair was
// written for. The repairs are keyed by SpecID, and a SpecID only means
// something inside the paper it was composed for: without a guard, re-composing
// a paper would hand the B-tree answer to whatever topic happens to land in
// spec_001. A repair therefore applies only when the slot's own knowledge
// points still match its topic; otherwise the question is left untouched and the
// regular repair path (regeneration or manual review) stays in charge.
func knownRepairGuard(specID string) []string {
	switch specID {
	case "spec_001", "spec_042":
		return []string{"B树", "B+树", "B 树", "B+ 树"}
	case "spec_003":
		return []string{"分块查找"}
	case "spec_004":
		return []string{"三元组"}
	case "spec_008":
		return []string{"堆"}
	case "spec_009":
		return []string{"平均查找长度"}
	case "spec_012", "spec_014":
		return []string{"中断"}
	case "spec_013":
		return []string{"总线"}
	case "spec_019":
		return []string{"缺页"}
	case "spec_020":
		return []string{"TLB"}
	case "spec_021":
		return []string{"主存容量的扩展", "主存容量"}
	case "spec_023":
		return []string{"死锁"}
	case "spec_025":
		return []string{"管程", "条件变量"}
	case "spec_017":
		return []string{"CHS地址", "DMA"}
	case "spec_026":
		return []string{"进程文件管理", "文件管理"}
	case "spec_027":
		return []string{"互斥"}
	case "spec_028":
		return []string{"页面置换", "LRU"}
	case "spec_030":
		return []string{"地址翻译", "TLB"}
	case "spec_032", "spec_046":
		return []string{"CHS"}
	case "spec_035":
		return []string{"OSI"}
	case "spec_036":
		return []string{"TCP"}
	case "spec_037":
		return []string{"循环冗余", "CRC"}
	case "spec_039":
		return []string{"香农"}
	case "spec_043":
		return []string{"RAID"}
	case "spec_044":
		return []string{"总线"}
	case "spec_047":
		return []string{"FTP"}
	default:
		return nil
	}
}

// pointsMatchGuard reports whether any knowledge point carries one of the
// guard keywords (case-insensitive, substring match).
func pointsMatchGuard(points, guard []string) bool {
	for _, keyword := range guard {
		for _, point := range points {
			if strings.Contains(strings.ToLower(point), strings.ToLower(keyword)) {
				return true
			}
		}
	}
	return false
}

// RepairKnownQuestion applies deterministic repairs for cached questions that
// were already found to contain a contradictory stem, answer, or explanation.
//
// This is intentionally separate from the LLM repair loop: reassembling an
// existing paper must be safe and repeatable, and must never spend a provider
// request merely to fix a known cached artifact. Each repair is guarded by the
// slot's knowledge points (see knownRepairGuard), so it can only ever touch the
// topic it was written for.
func RepairKnownQuestion(q models.GeneratedQuestion) (models.GeneratedQuestion, bool) {
	fixed, matched, changed := repairReviewedDefect(q)
	if matched {
		return fixed, changed
	}
	return q, false
}

// repairReviewedDefect applies every reviewed repair the question matches and
// reports whether a repair claimed the question and whether it changed it.
// "matched but unchanged" means the question already carries the reviewed
// content, which callers treat as an already-repaired slot.
func repairReviewedDefect(q models.GeneratedQuestion) (models.GeneratedQuestion, bool, bool) {
	if fixed, changed := repairMathReviewedDefect(q); changed {
		return fixed, true, true
	}
	if mathReviewedDefectSatisfied(q) {
		return q, true, false
	}

	guard := knownRepairGuard(q.SpecID)
	if len(guard) == 0 || !pointsMatchGuard(q.Points, guard) {
		return q, false, false
	}

	original := q
	q = repairAdditional408Question(q)
	switch q.SpecID {
	case "spec_001":
		q.Stem = `设有一棵 3 阶 B 树（每个结点最多 3 棵子树，最多 2 个关键字）。初始时该树为空，依次插入关键字 30, 20, 10, 40, 50, 60, 70。当一次插入使结点关键字数超过 2 个时，按标准 B 树算法分裂结点，并将中间关键字提升至父结点。最终生成的 B 树中，根结点包含的关键字是（ ）。`
		q.Options = []string{"20", "30", "20 和 40", "40"}
		q.Answer = `答案：D

插入 30、20、10 后，根结点溢出，提升中间关键字 20，得到根 [20]，左右子结点分别为 [10] 和 [30]。插入 40 后右子结点为 [30,40]；插入 50 后该结点溢出，提升 40，根变为 [20,40]，右侧子结点为 [50]。插入 60、70 后，右侧子结点再次溢出，提升 60，根暂为 [20,40,60]。根结点也溢出，提升其中间关键字 40，最终新根只包含关键字 [40]。因此选择 D。`

	case "spec_004":
		q.Stem = `设有一个 $5 \times 6$ 的稀疏矩阵 $A$，其非零元分布如下：$A[0][2]=5$，$A[1][1]=3$，$A[2][4]=8$，$A[3][0]=2$，$A[4][5]=7$。按行优先顺序将其存储为三元组表（行号、列号、元素值），且矩阵的行、列下标均从 $0$ 开始。下标为 $3$ 的数据项的列下标及元素值分别是多少？`
		q.Options = []string{
			"列下标为 1，元素值为 3",
			"列下标为 4，元素值为 8",
			"列下标为 0，元素值为 2",
			"列下标为 5，元素值为 7",
		}
		q.Answer = `答案：C

按行优先顺序排列，三元组下标 0 至 4 依次为 $(0,2,5)$、$(1,1,3)$、$(2,4,8)$、$(3,0,2)$、$(4,5,7)$。下标为 3 的数据项是 $(3,0,2)$，所以列下标为 0、元素值为 2，选择 C。`

	case "spec_020":
		q.Stem = `某计算机按字节编址，虚拟地址为 32 位，物理地址为 30 位。采用虚拟页式存储管理，页大小为 4 KB。系统配置一个直接映射的 TLB，共 16 项，TLB 索引取虚拟页号的低 4 位。数据 Cache 采用直接映射方式，容量为 64 KB，数据块大小为 32 B。若 CPU 访问某个变量时，虚拟地址为 $0x12345678$，物理地址为 $0x2BCDEF00$，则本次访问中 TLB 查找的索引字段和 Cache 查找的索引字段（均按位截取，从低位到高位）分别是：`
		q.Options = []string{
			"TLB 索引为 $0x04$，Cache 索引为 $0x733$",
			"TLB 索引为 $0x05$，Cache 索引为 $0x778$",
			"TLB 索引为 $0x04$，Cache 索引为 $0x778$",
			"TLB 索引为 $0x05$，Cache 索引为 $0x733$",
		}
		q.Answer = `答案：B

页大小为 $4\,\mathrm{KB}=2^{12}\,\mathrm{B}$，所以虚页号为
$0x12345678 \mathbin{>>} 12=0x12345$。TLB 有 16 项，索引取虚页号低 4 位，因此 TLB 索引为 $0x12345\mathbin{\&}0xF=0x05$。

Cache 块大小为 $32\,\mathrm{B}=2^5\,\mathrm{B}$，容量为 $64\,\mathrm{KB}$，共有 $2^{16}/2^5=2^{11}$ 个块，索引占 11 位。因此 Cache 索引为
$(0x2BCDEF00\mathbin{>>}5)\mathbin{\&}0x7FF=0x778$。故选择 B。`

	case "spec_027":
		q.Answer = `答案：B

初始时 mutex=1。若 $P_2$ 执行一次 wait 后进入临界区，退出时错误地连续执行两次 signal，信号量值会从 0 增加到 2。随后两个进程都可能各自成功执行 wait 并进入临界区，因而不能保证互斥。故直接后果是可能有多个进程同时进入临界区，选择 B。`

	case "spec_028":
		q.Answer = `答案：D

按 LRU 规则追踪页框（左侧为最久未使用，右侧为最近使用）：

| 访问 | 结果 | 访问后页框 | 缺页数 |
| --- | --- | --- | --- |
| 1 | 缺页 | 1 | 1 |
| 2 | 缺页 | 1,2 | 2 |
| 3 | 缺页 | 1,2,3 | 3 |
| 4 | 缺页 | 1,2,3,4 | 4 |
| 2 | 命中 | 1,3,4,2 | 4 |
| 1 | 命中 | 3,4,2,1 | 4 |
| 5 | 缺页，淘汰 3 | 4,2,1,5 | 5 |
| 6 | 缺页，淘汰 4 | 2,1,5,6 | 6 |
| 2 | 命中 | 1,5,6,2 | 6 |
| 1 | 命中 | 5,6,2,1 | 6 |
| 2 | 命中 | 5,6,1,2 | 6 |
| 3 | 缺页，淘汰 5 | 6,1,2,3 | 7 |
| 7 | 缺页，淘汰 6 | 1,2,3,7 | 8 |

共发生 8 次缺页，对应选项 D。`

	case "spec_030":
		q.Answer = `答案：A

页大小为 $4\,\mathrm{KB}=2^{12}\,\mathrm{B}$，所以页内偏移量占低 12 位。对虚拟地址 $0x00003FFF$ 取低 12 位，得到 $0xFFF$，即 $0x0FFF$。因此选择 A。`

	case "spec_032":
		q.Stem = `在传统磁盘 I/O 软件分层中，设备独立性软件已经将文件逻辑块映射为磁盘逻辑块地址（LBA）。某磁盘控制器采用 CHS 寻址，设备驱动程序需要根据磁盘几何参数将该 LBA 转换为柱面号、磁头号和扇区号，再向控制器发出读命令。该地址格式转换主要由哪一层完成？`
		q.Answer = `答案：B

设备独立性软件已将文件逻辑块映射为磁盘 LBA；将 LBA 按具体磁盘几何参数转换为 CHS 并设置控制器，属于设备驱动程序面向具体硬件的工作。因此选择 B。`

	case "spec_012":
		q.Stem = `某计算机有 5 个中断源 $I_1,I_2,I_3,I_4,I_5$，硬件响应优先级从高到低依次为 $I_1>I_2>I_3>I_4>I_5$。初始时所有中断均开放，CPU 正在执行主程序。$I_2$ 和 $I_4$ 同时提出请求；CPU 先处理优先级较高的请求。$I_2$ 服务程序执行期间不再有新请求；$I_2$ 返回后，CPU 处理仍待响应的 $I_4$ 请求。执行 $I_4$ 服务程序期间，$I_3$ 提出请求。每个中断服务程序执行时动态屏蔽本级及更低优先级中断。按各服务程序开始执行（被响应进入）的先后顺序，CPU 执行中断服务程序的顺序是（ ）。`
		q.Options = []string{
			`$I_4 \to I_3 \to I_2$`,
			`$I_2 \to I_4 \to I_3$`,
			`$I_2 \to I_3 \to I_4$`,
			`$I_4 \to I_2 \to I_3$`,
		}
		q.Answer = `答案：B

$I_2$ 与 $I_4$ 同时请求时，硬件先响应优先级较高的 $I_2$。$I_2$ 服务程序结束后，仍待响应的 $I_4$ 开始执行。随后 $I_3$ 的优先级高于 $I_4$，且未被 $I_4$ 的屏蔽字屏蔽，因此嵌套进入 $I_3$ 服务程序。按服务程序开始执行的顺序为 $I_2\to I_4\to I_3$，选择 B。`

	case "spec_035":
		q.Stem = `主机 $H_1$ 要向另一 IP 网络中的主机 $H_2$ 发送 IP 数据报，数据报经默认网关路由器 $R$ 转发。以太网帧到达 $R$ 后，路由器需要读取包含最终目的 IP 地址的协议数据单元，以决定下一跳。该协议数据单元所属的最高 OSI 层次是（ ）。`
		q.Answer = `答案：C

路由器收到发往其 MAC 地址的以太网帧后，会检查其中封装的 IP 数据报，并依据目的 IP 地址进行路由选择。IP 数据报属于网络层协议数据单元，因此选择 C。`

	case "spec_039":
		q.Stem = `某信道带宽为 $3000\,\mathrm{Hz}$，信噪比为 $30\,\mathrm{dB}$。按香农公式计算，该信道的理论容量约为多少？`
		q.Answer = `答案：A

由 $10\log_{10}(S/N)=30$ 得 $S/N=1000$。香农容量为
$C=W\log_2(1+S/N)=3000\log_2(1001)\approx29\,901\,\mathrm{bit/s}$，约为 $30\,\mathrm{kbps}$，选择 A。`

	case "spec_046":
		q.Stem = `设某磁盘有 $200$ 个柱面（柱面号 $0$ 至 $199$），每个柱面有 $8$ 个磁头（磁头号 $0$ 至 $7$），每个磁道有 $100$ 个扇区（扇区号 $1$ 至 $100$）。设备独立性软件已将文件逻辑块映射为磁盘逻辑块地址（LBA）；磁盘块从 $0$ 开始连续编号，顺序为柱面、柱面内磁头、磁道内扇区。设备驱动程序使用 CHS 寻址。
现有用户进程请求读取磁盘 LBA $10234$。
(1) 设备驱动程序需要将该 LBA 转换为 CHS 地址。请计算对应的柱面号、磁头号和扇区号，并写出过程。（5 分）
(2) 在上述请求处理中，设备独立性软件与设备驱动程序各自承担什么主要职责？（3 分）`
		q.Answer = `考查目标与判据：考查 LBA 到 CHS 的换算，以及设备独立性软件与设备驱动程序的职责边界。

(1) 每个柱面包含 $8\times100=800$ 个磁盘块。$10234=12\times800+634$，故柱面号为 $12$。余数 $634=6\times100+34$，故磁头号为 $6$，扇区号为 $34+1=35$。CHS 地址为 $(12,6,35)$。

(2) 设备独立性软件提供统一设备接口，并将文件逻辑块映射到磁盘 LBA；设备驱动程序处理具体设备的地址格式与控制器差异，将 LBA 转为 CHS、检查设备参数并发出读命令。`

	case "spec_047":
		q.Stem = `在某局域网内，客户端主机 $H_1$ 通过 NAT 路由器 $R$ 连接至互联网，并使用 FTP 主动模式向公网上的 FTP 服务器 $S$（IP 地址为 $203.0.113.10$）下载一个大小为 $1200\,\mathrm{KB}$ 的文件。已知：$H_1$ 的 IP 地址为 $192.168.1.100$，$R$ 的内网 IP 地址为 $192.168.1.1$，公网 IP 地址为 $198.51.100.1$。$H_1$ 与 $R$ 内网口之间的链路带宽为 $100\,\mathrm{Mbps}$，单程传播时延为 $25\,\mu\mathrm{s}$；$R$ 与 $S$ 之间的链路带宽为 $10\,\mathrm{Mbps}$，单程传播时延为 $10\,\mathrm{ms}$。FTP 控制连接与数据连接均采用 TCP，TCP 最大报文段长度（MSS）为 $1000\,\mathrm{B}$。忽略 TCP 慢启动、拥塞控制、报文头部传输时间以及 NAT 处理时间；发送方在收到前一个数据报文段的确认后立即发送下一个报文段。

(1) FTP 主动模式下，$S$ 从端口 20 向 NAT 公网地址发起数据连接。若 FTP ALG 为数据连接建立公网映射端口 $60001$，则 $S$ 发出的 SYN 报文段的目的 IP 地址和目的端口号分别是什么？
(2) $R$ 收到该 SYN 后，将其转发给 $H_1$。转发后的报文段，其源 IP、源端口、目的 IP 和目的端口分别是什么？
(3) 按 $1\,\mathrm{KB}=1024\,\mathrm{B}$ 计算，采用上述停等式发送方式，从 $S$ 发送第一个数据报文段开始，到 $H_1$ 收到最后一个数据报文段为止，所需总时间是多少？`
		q.Stem = strings.Replace(q.Stem,
			"公网 IP 地址为 $198.51.100.1$。",
			"公网 IP 地址为 $198.51.100.1$。$H_1$ 的 FTP 主动模式数据端口为 $50000$，FTP ALG 建立的映射为 $198.51.100.1:60001\\mapsto192.168.1.100:50000$。",
			1)
		q.Answer = `答案：

(1) 目的 IP 地址为 $198.51.100.1$，目的端口号为 $60001$。

(2) R 做目的地址转换后，报文段的源 IP 和源端口仍为 $203.0.113.10:20$，目的 IP 和目的端口为 $192.168.1.100:50000$。

(3) 文件大小为 $1200\times1024=1\,228\,800\,\mathrm{B}$，需要 $N=\lceil1\,228\,800/1000\rceil=1229$ 个数据报文段，其中前 $1228$ 段各为 $1000\,\mathrm{B}$，最后一段为 $800\,\mathrm{B}$。一个完整的停等周期（从某个 $1000\,\mathrm{B}$ 数据段开始发送到收到确认）为
$0.8+10+0.08+0.025+0.025+10=20.93\,\mathrm{ms}$。
前 $1228$ 个周期用于等待下一段发送。最后一个 $800\,\mathrm{B}$ 报文段从开始发送到 $H_1$ 收到需要 $0.64+10+0.064+0.025=10.729\,\mathrm{ms}$。因此总时间为
$1228\times20.93+10.729=25\,712.769\,\mathrm{ms}$，约为 $25.713\,\mathrm{s}$。`
	}

	if hasReviewedRepair(q.SpecID) {
		q.Status = "ready"
		q.Warnings = nil
	}
	return q, true, !reflect.DeepEqual(original, q)
}

// IsReviewedRepairSatisfied reports whether the slot's topic matches a reviewed
// repair and its content already equals the reviewed result. Repairing such a
// slot is a no-op, so callers must not treat it as still needing regeneration:
// without this check a second repair run would fall through to the provider and
// report the already-correct slot as failed.
func IsReviewedRepairSatisfied(q models.GeneratedQuestion) bool {
	_, matched, changed := repairReviewedDefect(q)
	return matched && !changed
}

// mathReviewedFixes corrects reviewed content defects in the math papers.
//
// Math papers reuse the 408 spec_XXX ids, and a knowledge point alone is not
// unique to one paper, so every fix additionally requires its own defective (or
// already-corrected) text: a slot that does not carry that text is never
// touched. Each entry below was found by reading the generated answer against
// its own derivation — the pipeline's automatic checks cannot verify
// mathematics, so these are the answers a human verified.
var mathReviewedFixes = []struct {
	points []string // every keyword must appear in the question's knowledge points
	field  string   // "answer" or "stem"
	old    string   // reviewed defective text
	new    string   // reviewed correction
}{
	{
		// Variable-limit integral: the derivation finds 1/3, the summary claimed 0.
		points: []string{"变限积分"},
		field:  "answer",
		old:    "(2) 极限值为 $0$",
		new:    "(2) 极限值为 $\\dfrac{1}{3}$",
	},
	{
		// Extremum on a disk: the derivation finds max 2, min 1/2-√2, the summary claimed 7/2 and -√6/2.
		points: []string{"多元函数"},
		field:  "answer",
		old:    "最大值为 $\\dfrac{7}{2}$，最小值为 $-\\dfrac{\\sqrt{6}}{2}$",
		new:    "最大值为 $2$，最小值为 $\\dfrac{1}{2}-\\sqrt{2}$",
	},
	{
		// Second-order ODE: the maximum is 1 at x=0, the summary claimed 1/e.
		points: []string{"常系数齐次线性微分方程"},
		field:  "answer",
		old:    "最大值为 $\\dfrac{1}{e}$",
		new:    "最大值为 $1$",
	},
	{
		// Implicit surface: the point has two branches, the summary dropped the ±.
		points: []string{"偏导数的概念与计算"},
		field:  "answer",
		old:    "\\mathrm{d}z\\big|_{(1,1)}=\\mathrm{d}x+\\mathrm{d}y",
		new:    "\\mathrm{d}z\\big|_{(1,1)}=\\pm\\dfrac{1}{\\sqrt{2}}(\\mathrm{d}x+\\mathrm{d}y)（$z=2-\\sqrt{2}$ 时取正号，$z=2+\\sqrt{2}$ 时取负号）",
	},
}

// repairMathReviewedDefect applies a math reviewed correction when the question
// carries the defective text.
func repairMathReviewedDefect(q models.GeneratedQuestion) (models.GeneratedQuestion, bool) {
	for _, fix := range mathReviewedFixes {
		if !pointsContainAll(q.Points, fix.points) {
			continue
		}
		target := q.Answer
		if fix.field == "stem" {
			target = q.Stem
		}
		if !strings.Contains(target, fix.old) {
			continue
		}
		switch fix.field {
		case "stem":
			q.Stem = strings.Replace(q.Stem, fix.old, fix.new, 1)
		default:
			q.Answer = strings.Replace(q.Answer, fix.old, fix.new, 1)
		}
		if q.Status != "ready" {
			q.Status = "ready"
			q.Warnings = nil
		}
		return q, true
	}
	return q, false
}

// mathReviewedDefectSatisfied reports whether the question already carries a
// math reviewed correction, so a repeated repair stays a no-op.
func mathReviewedDefectSatisfied(q models.GeneratedQuestion) bool {
	for _, fix := range mathReviewedFixes {
		if !pointsContainAll(q.Points, fix.points) {
			continue
		}
		target := q.Answer
		if fix.field == "stem" {
			target = q.Stem
		}
		if strings.Contains(target, fix.new) {
			return true
		}
	}
	return false
}

// pointsContainAll reports whether every keyword appears in some knowledge point
// (case-insensitive substring match).
func pointsContainAll(points, keywords []string) bool {
	for _, keyword := range keywords {
		matched := false
		for _, point := range points {
			if strings.Contains(strings.ToLower(point), strings.ToLower(keyword)) {
				matched = true
				break
			}
		}
		if !matched {
			return false
		}
	}
	return true
}

func repairAdditional408Question(q models.GeneratedQuestion) models.GeneratedQuestion {
	switch q.SpecID {
	case "spec_003":
		q.Stem = `长度为 $144$ 的顺序表采用分块查找。索引表和数据块内均采用顺序查找，且成功查找时各数据元素等概率。以下划分方式中，哪一种使查找成功时的平均查找长度（$ASL$）最小？`
		q.Answer = `答案：C

设划分为 $b$ 个等长数据块，每块含 $s=144/b$ 个元素。成功查找时，索引表和块内的平均比较次数分别为 $(b+1)/2$ 和 $(s+1)/2$，故 $ASL=(b+s+2)/2$。

逐项计算：A 为 $16$，B 为 $14$，C 为 $13$，D 为 $13.5$。因此 12 块、每块 12 个元素时平均查找长度最小，选择 C。`
	case "spec_008":
		q.Answer = `答案：A

依次插入并执行最小堆上滤后，最终数组为 $[13,49,27,49,76,65,38,97]$。因此 $H[4]=49$，选择 A。`
	case "spec_009":
		q.Stem = `已知一个含有 $12$ 个数据元素的线性表，其关键字为 $\{1,2,\ldots,12\}$。采用二分查找法，且每次取当前区间中间位置（向下取整）作为比较元素。查找成功时的平均查找长度（$ASL$）为（ ）。`
		q.Answer = `答案：A

按题设规则构造二分查找判定树，各层结点数依次为 $1,2,4,5$，对应比较次数为 $1,2,3,4$。成功查找的总比较次数为 $1+2\times2+3\times4+4\times5=37$，故 $ASL=37/12$，选择 A。`
	case "spec_013":
		q.Stem = `某 32 位单周期处理器在同一个时钟周期内必须并行完成以下三组数据传送：① ALU 结果送入数据存储器的写数据端；② 数据存储器的读数据送入寄存器堆写数据端；③ 指令存储器的指令送入指令寄存器。指令存储器与数据存储器相互独立，数据存储器具有独立的读、写端口。仅统计数据总线，不计地址线和控制线；每条总线每周期只能承载一组数据，且不进行总线复用。至少需要多少条相互独立的 32 位数据总线？`
		q.Options = []string{"2 条", "3 条", "4 条", "5 条"}
		q.Answer = `答案：B

题目要求三组彼此独立的数据同时传送，且不允许复用总线，因此每组传送各需一条总线，至少需要 $3$ 条，选择 B。`
	case "spec_014":
		q.Answer = `答案：B

屏蔽字按 $M_1M_2M_3M_4$ 排列，对应 $I_1,I_2,I_3,I_4$，位值 1 表示屏蔽。目标优先级为 $I_4>I_1>I_3>I_2$，因此各服务程序的屏蔽字分别为：$I_4:1111$，$I_1:1110$，$I_3:0110$，$I_2:0100$。

四个请求同时到达时，硬件先响应 $I_1$；其屏蔽字开放更高软件优先级的 $I_4$，故 $I_4$ 嵌套处理并先完成，然后 $I_1$ 完成。返回主程序后，硬件在剩余的 $I_2,I_3$ 请求中先响应 $I_2$；$I_2$ 的屏蔽字允许 $I_3$ 嵌套，因此 $I_3$ 先于 $I_2$ 完成。实际完成顺序为 $I_4\to I_1\to I_3\to I_2$，选择 B。`
	case "spec_019":
		q.Stem = `某按字节编址的计算机采用虚拟页式存储管理，页大小为 $4\,\mathrm{KB}$。Cache 采用直接映射方式，容量为 $8\,\mathrm{KB}$，行大小为 $32\,\mathrm{B}$。发生缺页并调入页面后，CPU 重新执行指令；此时该次访存的物理地址为 $0x1234CD$。该地址的页内偏移和 Cache 组号分别是多少？`
		q.Options = []string{
			`页内偏移为 $0x4CD$，Cache 组号为 $0xA6$`,
			`页内偏移为 $0x4CD$，Cache 组号为 $0xA4$`,
			`页内偏移为 $0xCD$，Cache 组号为 $0xA6$`,
			`页内偏移为 $0x4CD$，Cache 组号为 $0x91$`,
		}
		q.Answer = `答案：A

页大小为 $2^{12}\,\mathrm{B}$，故页内偏移为物理地址低 12 位：$0x1234CD\mathbin{\&}0xFFF=0x4CD$。Cache 共 $8\,\mathrm{KB}/32\,\mathrm{B}=256$ 组，需要 8 位组号；块偏移占低 5 位，因此 $(0x1234CD\mathbin{>>}5)\mathbin{\&}0xFF=0xA6$。故选择 A。`
	case "spec_021":
		q.Stem = `某计算机主存容量为 $4\,\mathrm{MiB}$，按字节编址、字长为 $32$ 位，由 $256\mathrm{K}\times8\,\mathrm{bit}$ SRAM 芯片通过字扩展和位扩展构成。CPU 通过 DMA 将从物理地址 $0x40000000$ 开始的连续 $4\,\mathrm{KiB}$ 数据传输至外设；数据 Cache 采用直接映射，块大小为 $64\,\mathrm{B}$。所需 SRAM 芯片数和该 DMA 区域覆盖的不同 Cache 块数分别是多少？`
		q.Options = []string{
			`$16$ 片，$64$ 块`,
			`$16$ 片，$32$ 块`,
			`$8$ 片，$64$ 块`,
			`$8$ 片，$32$ 块`,
		}
		q.Answer = `答案：A

每片 $256\mathrm{K}\times8\,\mathrm{bit}$ 芯片容量为 $256\,\mathrm{KiB}$。主存容量为 $4\,\mathrm{MiB}$，故共需 $4\,\mathrm{MiB}/256\,\mathrm{KiB}=16$ 片。

DMA 区域大小为 $4\,\mathrm{KiB}=4096\,\mathrm{B}$，Cache 块大小为 $64\,\mathrm{B}$，覆盖 $4096/64=64$ 个不同的主存块。题目问的是区域覆盖块数，不据此推断 Cache 命中情况，选择 A。`
	case "spec_023":
		q.Stem = `系统中有 $12$ 个同类、可重复使用的资源实例，由 $4$ 个进程共享。每个进程最多需要 $N$ 个资源；进程一次申请一个资源，并在完成后一次性释放所持资源。为保证无论请求交错顺序如何系统都不会发生死锁，$N$ 的最大值为（ ）。`
		q.Options = []string{`$2$`, `$3$`, `$4$`, `$5$`}
		q.Answer = `答案：B

最坏情况下，每个进程都可能各持有 $N-1$ 个资源并等待最后一个。为保证仍有至少一个资源可使某进程完成，需满足 $12\ge4(N-1)+1$。因此 $N\le3.75$；$N$ 为整数时最大为 $3$，选择 B。`
	case "spec_017":
		q.Stem = `某磁盘按柱面、磁头、扇区顺序对 LBA 从 $0$ 开始连续编号，每个柱面有 $8$ 个磁头，每个磁道有 $100$ 个扇区，扇区号从 $1$ 开始。设备驱动程序向控制器提交 LBA $10234$，数据阶段采用 DMA。以下叙述哪项正确？`
		q.Options = []string{
			`A. CHS 地址为 $(12,6,34)$；数据由 CPU 逐字搬运到内存`,
			`B. CHS 地址为 $(12,6,35)$；CPU 初始化 DMA，DMA 控制器负责设备与内存间的数据传送`,
			`C. CHS 地址为 $(12,7,35)$；DMA 传送不占用主存总线`,
			`D. CHS 地址为 $(13,6,35)$；DMA 完成后无需通知 CPU`,
		}
		q.Answer = `答案：B

每个柱面包含 $8\times100=800$ 个扇区。$10234=12\times800+634$，故柱面号为 $12$；余数 $634=6\times100+34$，故磁头号为 $6$，扇区号为 $34+1=35$，CHS 地址为 $(12,6,35)$。DMA 传送由 CPU 设置传输参数并启动，随后 DMA 控制器在设备与内存之间传送数据，完成后通常向 CPU 发出中断。因此选择 B。`
	case "spec_025":
		q.Stem = `使用 Hoare 风格的管程实现有界缓冲区，条件变量 \texttt{notFull} 表示“缓冲区未满”，条件变量 \texttt{notEmpty} 表示“缓冲区非空”。生产者进入管程后发现缓冲区已满，应执行哪项操作？`
		q.Options = []string{
			`A. 调用 \texttt{wait(notFull)}，阻塞等待，并释放管程互斥权`,
			`B. 调用 \texttt{wait(notEmpty)}，阻塞等待，并释放管程互斥权`,
			`C. 调用 \texttt{signal(notFull)}，唤醒另一个生产者`,
			`D. 调用 \texttt{signal(notEmpty)}，唤醒消费者`,
		}
		q.Answer = `答案：A

生产者插入产品的前提是缓冲区未满。题目已说明缓冲区已满，因此生产者必须等待 \texttt{notFull} 条件成立。Hoare 风格管程中的 \texttt{wait} 会将进程放入对应条件变量的等待队列，并释放管程互斥权，使消费者能够进入管程取走产品；条件变量 \texttt{notEmpty} 用于消费者等待缓冲区出现产品，不能用于本情形。`
	case "spec_026":
		q.Stem = `某 UNIX 文件系统的 inode 含有 10 个直接块指针，以及各 1 个一级、二级和三级间接块指针。磁盘块大小为 $4\,\mathrm{KiB}$，每个块指针占 $4\,\mathrm{B}$。inode 已在内存中，但间接索引块和目标数据块均不在缓存中。读取从 0 开始编号的逻辑块 $4095$ 时，仅统计读取索引块和数据块所需的磁盘 I/O 次数（不计系统调用等额外开销），需要多少次？`
		q.Options = []string{"1 次", "2 次", "3 次", "4 次"}
		q.Answer = `答案：C

每个索引块可存放 $4\,\mathrm{KiB}/4\,\mathrm{B}=1024$ 个指针。10 个直接指针覆盖逻辑块 $0$ 至 $9$，一级间接覆盖 $10$ 至 $1033$，故逻辑块 $4095$ 位于二级间接范围内。需读取二级索引块、其指向的一级索引块和目标数据块，共 $3$ 次磁盘 I/O，选择 C。`
	case "spec_036":
		q.Stem = `在 TCP Reno 拥塞控制中，发送方当前 $cwnd=24\,\mathrm{MSS}$，且 $FlightSize=24\,\mathrm{MSS}$，$ssthresh=32\,\mathrm{MSS}$。收到 3 个重复 ACK 后进入快恢复；当丢失报文得到确认并退出快恢复时，新的 $ssthresh$ 和 $cwnd$ 分别是多少？`
		q.Answer = `答案：A

进入快恢复时将 $ssthresh$ 设为 $FlightSize/2=12\,\mathrm{MSS}$。丢失报文被确认并退出快恢复后，$cwnd$ 设为 $ssthresh$，也是 $12\,\mathrm{MSS}$，选择 A。`
	case "spec_037":
		q.Options = []string{"1010010101", "1010010010", "1010011100", "1010010011"}
		q.Answer = `答案：B

$G(x)=x^4+x+1$ 对应的生成比特串为 $10011$。将数据 $101001$ 末尾补 4 个 0 后作模 2 除法，余数为 $0010$。将余数附加到原数据，得到 $1010010010$，选择 B。`
	case "spec_042":
		q.Stem = `设有一棵 $4$ 阶 $B+$ 树：每个结点最多有 $4$ 个孩子；内部结点中的分隔关键字均取其右侧孩子所含的最小关键字；叶结点最多含 $3$ 个关键字，溢出为 $4$ 个时按从小到大各分为 $2$ 个，并将新右叶的最小关键字插入父结点。所有叶结点处于同一层，并按关键字递增顺序链接。
初始树为空，依次插入 $\langle20,35,25,50,10,5,15,45,30\rangle$。
(1) 写出最终树的内部结点、叶结点及叶链。
(2) 查找 $28$ 时，按结点内关键字从左到右比较，写出比较序列；查找 $15$ 时，写出访问路径，并说明是否需要沿叶链移动。`
		q.Answer = `答案与解析：

根据题设的分裂规则，插入 $50$ 后，叶结点 $[20,25,35,50]$ 分裂为 $[20,25]$ 和 $[35,50]$，根为 $[35]$。插入 $5$ 后左叶分裂为 $[5,10]$ 和 $[20,25]$，根的分隔关键字更新为 $[20,35]$。插入 $15$ 后最左叶成为 $[5,10,15]$；插入 $45$ 后最右叶成为 $[35,45,50]$；插入 $30$ 后中间叶成为 $[20,25,30]$，无需再分裂。

最终结构为：根结点 $[20,35]$，其三个叶孩子依次为 $[5,10,15]$、$[20,25,30]$、$[35,45,50]$；叶链为 $[5,10,15]\to[20,25,30]\to[35,45,50]$。

查找 $28$：根结点依次比较 $20,35$，进入中间叶；再比较 $20,25,30$，未找到。完整比较序列为 $20,35,20,25,30$。

查找 $15$：根结点比较 $20$ 后进入最左叶 $[5,10,15]$，依次比较 $5,10,15$ 后命中，无需沿叶链移动到下一叶。`
	case "spec_043":
		q.Stem = `某 RAID 5 阵列由 $5$ 块硬盘组成，每块容量为 $2\,\mathrm{TiB}$。请回答：
(1) 阵列的可用数据容量是多少？
(2) 若每个条带单元（单块硬盘在一个完整条带中承载的块）大小为 $64\,\mathrm{KiB}$，该阵列最多可容纳多少个完整条带？
(3) 任意一块硬盘发生故障时，阵列能否继续提供数据读写服务？说明原因。
取 $1\,\mathrm{TiB}=2^{40}\,\mathrm{B}$、$1\,\mathrm{KiB}=2^{10}\,\mathrm{B}$。`
		q.Answer = `答案与解析：

(1) RAID 5 的校验信息合计占用相当于一块硬盘的容量，可用数据容量为 $(5-1)\times2=8\,\mathrm{TiB}$。

(2) 一个完整条带含 $4$ 个数据条带单元，每个单元为 $64\,\mathrm{KiB}$，故每个完整条带可存放 $256\,\mathrm{KiB}$ 数据。可用容量中的完整条带数为
$\frac{8\times2^{40}}{4\times64\times2^{10}}=33\,554\,432$。

(3) 能。RAID 5 的分布式校验可在单盘故障时由其余硬盘上的数据和校验信息重建缺失内容；阵列进入降级状态，性能可能下降，但可继续提供服务。`
	case "spec_044":
		q.Stem = `某同步总线的数据宽度为 $32$ 位，主设备到从设备及返回方向的单程信号传播延迟均最多为 $15\,\mathrm{ns}$。从设备收到地址和读命令后，准备数据需要 $45\,\mathrm{ns}$；主设备采样前的数据建立时间为 $5\,\mathrm{ns}$。忽略其他时序开销，地址和读命令在时钟上升沿发出，数据须在下一上升沿前满足建立时间。
(1) 若一项读操作必须在一个时钟周期内完成，允许的最高时钟频率是多少？在该频率下的理论最大数据传输率是多少？
(2) 另考虑一条时钟频率为 $50\,\mathrm{MHz}$ 的突发总线。从设备在稳态流水阶段可保证每个时钟周期传送一个 $32$ 位数据字，忽略突发启动开销。其稳态最大数据传输率是多少？`
		q.Answer = `答案与解析：

(1) 一个读周期至少须覆盖地址传播、从设备准备、数据返回传播和建立时间：
$T_{\min}=15+45+15+5=80\,\mathrm{ns}$。
因此最高频率为 $1/T_{\min}=12.5\,\mathrm{MHz}$。每周期传输 $32$ 位，即 $4\,\mathrm{B}$，理论最大速率为 $4/(80\times10^{-9})=50\,\mathrm{MB/s}$（按 $1\,\mathrm{MB}=10^6\,\mathrm{B}$）。

(2) $50\,\mathrm{MHz}$ 对应周期 $20\,\mathrm{ns}$。题设稳态下每周期传输一个 $4\,\mathrm{B}$ 数据字，速率为 $4/(20\times10^{-9})=200\,\mathrm{MB/s}$。该部分使用题目另行给出的流水突发能力，不套用第 (1) 问的单周期设备时序约束。`
	case "spec_046":
		q.Stem = strings.ReplaceAll(q.Stem, "柱面号 0~199", "柱面号 $0$ 至 $199$")
		q.Stem = strings.ReplaceAll(q.Stem, "柱面号 0 至 199", "柱面号 $0$ 至 $199$")
		q.Stem = strings.ReplaceAll(q.Stem, "磁头号 0~7", "磁头号 $0$ 至 $7$")
		q.Stem = strings.ReplaceAll(q.Stem, "扇区号 1~100", "扇区号 $1$ 至 $100$")
	}
	return q
}

func hasReviewedRepair(specID string) bool {
	switch specID {
	case "spec_001", "spec_003", "spec_004", "spec_008", "spec_009", "spec_012", "spec_013", "spec_014", "spec_017", "spec_019", "spec_020", "spec_021", "spec_023", "spec_025", "spec_026", "spec_027", "spec_028", "spec_030", "spec_032", "spec_035", "spec_036", "spec_037", "spec_039", "spec_042", "spec_043", "spec_044", "spec_046", "spec_047":
		return true
	default:
		return false
	}
}
