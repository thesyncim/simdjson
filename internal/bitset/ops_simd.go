//go:build go1.27 && !go1.28 && goexperiment.simd && (arm64 || amd64)

package bitset

import (
	"simd/archsimd"
	"unsafe"
)

// Accelerated reports whether this build selected the SIMD word kernels.
func Accelerated() bool { return true }

// The vector bodies are deliberately textual and four-way unrolled: no helper
// call can force live vectors through the stack ABI, and array-pointer loads
// remove loop bounds checks. The scalar tails are shared only after every
// vector is stored.

func andWords(dst, a, b []uint64) {
	i := 0
	if len(dst) >= 8 {
		dp := unsafe.Pointer(unsafe.SliceData(dst))
		ap := unsafe.Pointer(unsafe.SliceData(a))
		bp := unsafe.Pointer(unsafe.SliceData(b))
		for ; i+8 <= len(dst); i += 8 {
			off := uintptr(i) * 8
			a0 := archsimd.LoadUint64x2Array((*[2]uint64)(unsafe.Add(ap, off+0)))
			a1 := archsimd.LoadUint64x2Array((*[2]uint64)(unsafe.Add(ap, off+16)))
			a2 := archsimd.LoadUint64x2Array((*[2]uint64)(unsafe.Add(ap, off+32)))
			a3 := archsimd.LoadUint64x2Array((*[2]uint64)(unsafe.Add(ap, off+48)))
			b0 := archsimd.LoadUint64x2Array((*[2]uint64)(unsafe.Add(bp, off+0)))
			b1 := archsimd.LoadUint64x2Array((*[2]uint64)(unsafe.Add(bp, off+16)))
			b2 := archsimd.LoadUint64x2Array((*[2]uint64)(unsafe.Add(bp, off+32)))
			b3 := archsimd.LoadUint64x2Array((*[2]uint64)(unsafe.Add(bp, off+48)))
			a0.And(b0).StoreArray((*[2]uint64)(unsafe.Add(dp, off+0)))
			a1.And(b1).StoreArray((*[2]uint64)(unsafe.Add(dp, off+16)))
			a2.And(b2).StoreArray((*[2]uint64)(unsafe.Add(dp, off+32)))
			a3.And(b3).StoreArray((*[2]uint64)(unsafe.Add(dp, off+48)))
		}
	}
	for ; i < len(dst); i++ {
		dst[i] = a[i] & b[i]
	}
}

func orWords(dst, a, b []uint64) {
	i := 0
	if len(dst) >= 8 {
		dp := unsafe.Pointer(unsafe.SliceData(dst))
		ap := unsafe.Pointer(unsafe.SliceData(a))
		bp := unsafe.Pointer(unsafe.SliceData(b))
		for ; i+8 <= len(dst); i += 8 {
			off := uintptr(i) * 8
			a0 := archsimd.LoadUint64x2Array((*[2]uint64)(unsafe.Add(ap, off+0)))
			a1 := archsimd.LoadUint64x2Array((*[2]uint64)(unsafe.Add(ap, off+16)))
			a2 := archsimd.LoadUint64x2Array((*[2]uint64)(unsafe.Add(ap, off+32)))
			a3 := archsimd.LoadUint64x2Array((*[2]uint64)(unsafe.Add(ap, off+48)))
			b0 := archsimd.LoadUint64x2Array((*[2]uint64)(unsafe.Add(bp, off+0)))
			b1 := archsimd.LoadUint64x2Array((*[2]uint64)(unsafe.Add(bp, off+16)))
			b2 := archsimd.LoadUint64x2Array((*[2]uint64)(unsafe.Add(bp, off+32)))
			b3 := archsimd.LoadUint64x2Array((*[2]uint64)(unsafe.Add(bp, off+48)))
			a0.Or(b0).StoreArray((*[2]uint64)(unsafe.Add(dp, off+0)))
			a1.Or(b1).StoreArray((*[2]uint64)(unsafe.Add(dp, off+16)))
			a2.Or(b2).StoreArray((*[2]uint64)(unsafe.Add(dp, off+32)))
			a3.Or(b3).StoreArray((*[2]uint64)(unsafe.Add(dp, off+48)))
		}
	}
	for ; i < len(dst); i++ {
		dst[i] = a[i] | b[i]
	}
}

func andNotWords(dst, a, b []uint64) {
	i := 0
	if len(dst) >= 8 {
		dp := unsafe.Pointer(unsafe.SliceData(dst))
		ap := unsafe.Pointer(unsafe.SliceData(a))
		bp := unsafe.Pointer(unsafe.SliceData(b))
		for ; i+8 <= len(dst); i += 8 {
			off := uintptr(i) * 8
			a0 := archsimd.LoadUint64x2Array((*[2]uint64)(unsafe.Add(ap, off+0)))
			a1 := archsimd.LoadUint64x2Array((*[2]uint64)(unsafe.Add(ap, off+16)))
			a2 := archsimd.LoadUint64x2Array((*[2]uint64)(unsafe.Add(ap, off+32)))
			a3 := archsimd.LoadUint64x2Array((*[2]uint64)(unsafe.Add(ap, off+48)))
			b0 := archsimd.LoadUint64x2Array((*[2]uint64)(unsafe.Add(bp, off+0)))
			b1 := archsimd.LoadUint64x2Array((*[2]uint64)(unsafe.Add(bp, off+16)))
			b2 := archsimd.LoadUint64x2Array((*[2]uint64)(unsafe.Add(bp, off+32)))
			b3 := archsimd.LoadUint64x2Array((*[2]uint64)(unsafe.Add(bp, off+48)))
			a0.AndNot(b0).StoreArray((*[2]uint64)(unsafe.Add(dp, off+0)))
			a1.AndNot(b1).StoreArray((*[2]uint64)(unsafe.Add(dp, off+16)))
			a2.AndNot(b2).StoreArray((*[2]uint64)(unsafe.Add(dp, off+32)))
			a3.AndNot(b3).StoreArray((*[2]uint64)(unsafe.Add(dp, off+48)))
		}
	}
	for ; i < len(dst); i++ {
		dst[i] = a[i] &^ b[i]
	}
}
