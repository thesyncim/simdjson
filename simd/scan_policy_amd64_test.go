//go:build goexperiment.simd && amd64

package simd

import "testing"

func TestAMD64ScannerSelectionRequiresProvenWidth(t *testing.T) {
	cases := []struct {
		name     string
		features CPUFeatures
		want     bool
	}{
		{"scalar", 0, false},
		{"avx2", CPUFeatureAVX2.mask(), true},
		{"avx512 alone", CPUFeatureAVX512.mask(), false},
		{"avx512 cpu uses avx2", CPUFeatureAVX2.mask() | CPUFeatureAVX512.mask(), true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := useAVX2Scanner(tc.features); got != tc.want {
				t.Fatalf("useAVX2Scanner(%v) = %v, want %v", tc.features, got, tc.want)
			}
		})
	}
}
