package models

import "strings"

// Canonical408Discipline normalizes the four top-level 408 exam domains.
func Canonical408Discipline(value string) (string, bool) {
	switch strings.TrimSpace(value) {
	case "数据结构":
		return "数据结构", true
	case "组成原理", "计算机组成原理":
		return "组成原理", true
	case "操作系统":
		return "操作系统", true
	case "计算机网络":
		return "计算机网络", true
	default:
		return "", false
	}
}

func Is408Discipline(value string) bool {
	_, ok := Canonical408Discipline(value)
	return ok
}

// DisciplineFromPoints returns the exam-level discipline represented by the
// first knowledge-point label. ODE labels in math exams are part of calculus.
func DisciplineFromPoints(points []string) string {
	if len(points) == 0 {
		return ""
	}
	discipline := strings.TrimSpace(points[0])
	// Generated 408 items use a subject namespace before the discipline,
	// while parsed historical items store the discipline as the first path.
	if discipline == "408" && len(points) > 1 {
		discipline = strings.TrimSpace(points[1])
	}
	if canonical, ok := Canonical408Discipline(discipline); ok {
		discipline = canonical
	}
	if discipline == "常微分方程" {
		return "高等数学"
	}
	return discipline
}
