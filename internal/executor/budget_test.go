package executor

import (
	"testing"

	"github.com/tackhq/tack/internal/playbook"
)

func TestBatchExceedsBudget(t *testing.T) {
	tests := []struct {
		name      string
		play      *playbook.Play
		batchSize int
		failed    int
		want      bool
	}{
		{"no failures never aborts", &playbook.Play{}, 4, 0, false},
		{"default budget aborts on any failure", &playbook.Play{}, 4, 1, true},
		{"any_errors_fatal aborts on one", &playbook.Play{AnyErrorsFatal: true}, 10, 1, true},
		{"within max_fail_percentage continues", &playbook.Play{MaxFailPercentage: 50}, 4, 2, false},
		{"exceeding max_fail_percentage aborts", &playbook.Play{MaxFailPercentage: 50}, 4, 3, true},
		{"exactly at threshold continues", &playbook.Play{MaxFailPercentage: 25}, 4, 1, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := batchExceedsBudget(tt.play, tt.batchSize, tt.failed); got != tt.want {
				t.Errorf("batchExceedsBudget(size=%d, failed=%d) = %v, want %v",
					tt.batchSize, tt.failed, got, tt.want)
			}
		})
	}
}
