package models

import "testing"

func TestDisciplineFromPointsHandlesGenerated408Namespace(t *testing.T) {
	tests := []struct {
		points []string
		want   string
	}{
		{[]string{"408", "数据结构", "图"}, "数据结构"},
		{[]string{"组成原理", "Cache"}, "组成原理"},
		{[]string{"操作系统", "虚拟内存"}, "操作系统"},
		{[]string{"计算机网络", "TCP"}, "计算机网络"},
		{[]string{"408", "计算机组成原理", "Cache"}, "组成原理"},
		{[]string{"常微分方程", "线性方程"}, "高等数学"},
	}
	for _, test := range tests {
		if got := DisciplineFromPoints(test.points); got != test.want {
			t.Errorf("DisciplineFromPoints(%v) = %q; want %q", test.points, got, test.want)
		}
	}
	if Is408Discipline("程序设计") || !Is408Discipline("组成原理") {
		t.Fatal("Is408Discipline did not enforce the four canonical domains")
	}
}
