// internal/tts/provider_test.go
package tts

import "testing"

func TestClampSpeed(t *testing.T) {
	cases := []struct {
		name            string
		speed, min, max float64
		want            float64
	}{
		{"zero returns default 1.0", 0, 0.7, 1.5, 1.0},
		{"below min clamps to min", 0.1, 0.7, 1.5, 0.7},
		{"above max clamps to max", 9, 0.7, 1.5, 1.5},
		{"within range unchanged", 1.2, 0.7, 1.5, 1.2},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := ClampSpeed(c.speed, c.min, c.max); got != c.want {
				t.Errorf("ClampSpeed(%v,%v,%v) = %v, want %v", c.speed, c.min, c.max, got, c.want)
			}
		})
	}
}
