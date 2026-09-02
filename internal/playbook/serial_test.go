package playbook

import (
	"reflect"
	"testing"
)

func TestSerialSpec_Batches(t *testing.T) {
	tests := []struct {
		name  string
		spec  SerialSpec
		total int
		want  []int
	}{
		{"empty is one batch", SerialSpec{}, 5, []int{5}},
		{"fixed size", SerialSpec{"2"}, 5, []int{2, 2, 1}},
		{"one at a time", SerialSpec{"1"}, 3, []int{1, 1, 1}},
		{"percentage rounds up", SerialSpec{"25%"}, 10, []int{3, 3, 3, 1}},
		{"ramp then repeat last", SerialSpec{"1", "2"}, 6, []int{1, 2, 2, 1}},
		{"ramp with percent", SerialSpec{"1", "50%"}, 5, []int{1, 3, 1}},
		{"size exceeds total", SerialSpec{"10"}, 3, []int{3}},
		{"zero total", SerialSpec{"2"}, 0, nil},
		{"malformed token takes rest", SerialSpec{"abc"}, 4, []int{4}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := tt.spec.Batches(tt.total)
			if !reflect.DeepEqual(got, tt.want) {
				t.Errorf("Batches(%d) = %v, want %v", tt.total, got, tt.want)
			}
			// Batches must always sum to total (when total > 0).
			if tt.total > 0 {
				sum := 0
				for _, n := range got {
					sum += n
				}
				if sum != tt.total {
					t.Errorf("batch sizes %v sum to %d, want %d", got, sum, tt.total)
				}
			}
		})
	}
}
