package rpc

import "testing"

func TestDashRawMempoolVerboseFeeRateMilliSatPerVByte(t *testing.T) {
	tests := []struct {
		name  string
		entry GetRawMempoolVerboseResult
		want  uint64
	}{
		{
			name: "legacy top-level fee",
			entry: GetRawMempoolVerboseResult{
				Vsize: 250,
				Fee:   0.0001,
			},
			want: 40000,
		},
		{
			name: "fractional fee rate",
			entry: GetRawMempoolVerboseResult{
				Vsize: 250,
				Fees: MempoolFees{
					Base: 0.00000375,
				},
			},
			want: 1500,
		},
		{
			name: "dash core nested fee",
			entry: GetRawMempoolVerboseResult{
				Vsize: 240,
				Fees: MempoolFees{
					Base: 0.00015,
				},
			},
			want: 62500,
		},
		{
			name: "round to nearest millisat",
			entry: GetRawMempoolVerboseResult{
				Vsize: 3,
				Fee:   0.00000002,
			},
			want: 667,
		},
		{
			name: "zero vsize",
			entry: GetRawMempoolVerboseResult{
				Vsize: 0,
				Fee:   0.0001,
			},
			want: 0,
		},
		{
			name: "negative fee",
			entry: GetRawMempoolVerboseResult{
				Vsize: 250,
				Fee:   -0.0001,
			},
			want: 0,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := tt.entry.FeeRateMilliSatPerVByte()
			if got != tt.want {
				t.Fatalf("FeeRateMilliSatPerVByte() = %d, want %d", got, tt.want)
			}
		})
	}
}
