package calculator

import "testing"

func TestAdd(t *testing.T) {
	cases := []struct {
		name string
		a, b int
		want int
	}{
		{name: "positive operands", a: 2, b: 3, want: 5},
		{name: "negative operand", a: -4, b: 7, want: 3},
		{name: "zero", a: 0, b: 8, want: 8},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := Add(tc.a, tc.b); got != tc.want {
				t.Fatalf("Add(%d, %d) = %d, want %d", tc.a, tc.b, got, tc.want)
			}
		})
	}
}
