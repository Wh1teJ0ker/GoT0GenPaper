package models

import "strings"

// DisciplineFromPoints returns the exam-level discipline represented by the
// first knowledge-point label. ODE labels in math exams are part of calculus.
func DisciplineFromPoints(points []string) string {
	if len(points) == 0 {
		return ""
	}
	discipline := strings.TrimSpace(points[0])
	if discipline == "常微分方程" {
		return "高等数学"
	}
	return discipline
}
