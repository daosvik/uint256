// uint256: Fixed size 256-bit math library
// Copyright 2021 uint256 Authors
// SPDX-License-Identifier: BSD-3-Clause

package uint256

import (
	"math/bits"
	"sync"
)

// Cache for reciprocal()
//
// Each cache set contains its own mutex to reduce lock contention in highly
// multithreaded settings.
//
// Adjust cacheIndexBits and cacheWays to scale the size of the cache.
// Total cache size is (24+72*cacheWays)*2^cacheIndexBits bytes, which is
// 96 KiB with cacheIndexBits = 8 and cacheWays = 5. There are also 16 bytes
// for hit and miss counters.
//
// Reasonable values are quite small, e.g. cacheIndexBits from 2 to 10, and
// cacheWays around 5. Note that cacheWays = 5+8n makes the set size an integer
// number of 64-byte cachelines.
//
// If you want to disable the cache, set cacheIndexBits and cacheWays to 0
//
// If you want to have a hardcoded modulus with precomputation, set
// fixedModulus = true and adjust the value stored in fixed_m. This can be
// used with or without the regular cache.

const (
	cacheIndexBits = 8
	cacheWays      = 5

	cacheSets = 1 << cacheIndexBits
	cacheMask = cacheSets - 1

	fixedModulus = true
)

type cacheSet struct {
	rw  sync.RWMutex
	mod [cacheWays]Int
	inv [cacheWays][5]uint64
}

type ReciprocalCache struct {
	set [cacheSets]cacheSet
	hit  uint64
	miss uint64
}

// NewCache returns a new ReciprocalCache.
func NewCache() *ReciprocalCache{
	return &ReciprocalCache{}
}

func (c *ReciprocalCache) Stats() (hit, miss uint64) {
	return c.hit, c.miss
}

var (
	// Fixed modulus and its reciprocal
	fixed_m Int
	fixed_r [5]uint64
)

func init() {
	if fixedModulus {
		// Initialise fixed modulus
		fixed_m[3] = 0xffffffff00000001
		fixed_m[2] = 0x0000000000000000
		fixed_m[1] = 0x00000000ffffffff
		fixed_m[0] = 0xffffffffffffffff

		fixed_r = reciprocal(fixed_m, nil)
	}
}

func (cache *ReciprocalCache) has(m Int, index uint64, dest *[5]uint64) bool {
	if fixedModulus && m.Eq(&fixed_m) {
		dest[0] = fixed_r[0]
		dest[1] = fixed_r[1]
		dest[2] = fixed_r[2]
		dest[3] = fixed_r[3]
		dest[4] = fixed_r[4]

		return true
	}

	if cacheWays == 0 {
		return false
	}

	cache.set[index].rw.RLock()
	defer cache.set[index].rw.RUnlock()

	for w := 0; w < cacheWays; w++ {
		if cache.set[index].mod[w].Eq(&m) {
			copy(dest[:], cache.set[index].inv[w][:])
			cache.hit++
			return true
		}
	}
	cache.miss++
	return false
}

func (cache *ReciprocalCache) put(m Int, index uint64, mu [5]uint64) {
	if cacheWays == 0 {
		return
	}

	cache.set[index].rw.Lock()
	defer cache.set[index].rw.Unlock()

	var w int
	for w = 0; w < cacheWays; w++ {
		if cache.set[index].mod[w].IsZero() {
			// Found an empty slot
			cache.set[index].mod[w] = m
			cache.set[index].inv[w] = mu
			return
		}
	}
	// Shift old elements, evicting the oldest
	for w = cacheWays - 1; w > 0; w-- {
		cache.set[index].mod[w] = cache.set[index].mod[w-1]
		cache.set[index].inv[w] = cache.set[index].inv[w-1]
	}
	// w == 0
	cache.set[index].mod[w] = m
	cache.set[index].inv[w] = mu
}

// Some utility functions

func leadingZeros(x Int) (z int) {
	z  = bits.LeadingZeros64(x[3]); if z <  64 { return z }
	z += bits.LeadingZeros64(x[2]); if z < 128 { return z }
	z += bits.LeadingZeros64(x[1]); if z < 192 { return z }
	z += bits.LeadingZeros64(x[0]); return z
}

func onesCount(x Int) (z int) {
	z =	bits.OnesCount64(x[0]) +
		bits.OnesCount64(x[1]) +
		bits.OnesCount64(x[2]) +
		bits.OnesCount64(x[3])
	return z
}

// shiftright320 shifts a 320-bit value 0-63 bits right
// z = x >> (s % 64)

func shiftright320(x [5]uint64, s uint) (z [5]uint64) {
	r := s % 64	// right shift
	l := 64 - r	// left shift

	z[0] = (x[0] >> r) | (x[1] << l)
	z[1] = (x[1] >> r) | (x[2] << l)
	z[2] = (x[2] >> r) | (x[3] << l)
	z[3] = (x[3] >> r) | (x[4] << l)
	z[4] = (x[4] >> r)

	return z
}

// reciprocal computes a 320-bit value representing 1/m
//
// Notes:
// - reciprocal(0) = 0
// - reciprocal(1) = 2^320-1
// - otherwise, the result is normalised to have non-zero most significant word
// - starts with a 32-bit division, refines with newton-raphson iterations

func reciprocal(m Int, cache *ReciprocalCache) (mu [5]uint64) {

	s := leadingZeros(m)
	p := 255 - s // floor(log_2(m)), m>0

	// 0 or a power of 2?

	if onesCount(m) <= 1 {
		if s >= 255 { // m <= 1
			mu[4] = -m[0]
			mu[3] = -m[0]
			mu[2] = -m[0]
			mu[1] = -m[0]
			mu[0] = -m[0]
		} else {
			mu[4] = ^uint64(0) >> uint(p & 63)
			mu[3] = ^uint64(0)
			mu[2] = ^uint64(0)
			mu[1] = ^uint64(0)
			mu[0] = ^uint64(0)
		}
		return mu
	}

	// Check for reciprocal in the cache
	var cacheIndex uint64
	if cache != nil{
		cacheIndex = (m[3] ^ m[2] ^ m[1] ^ m[0]) & cacheMask
	    if cache.has(m, cacheIndex, &mu) {
			return mu
		}
	}

	// Maximise division precision by left-aligning divisor

	var (
		y Int		// left-aligned copy of m
		r0 uint32	// estimate of 2^31/y
	)

	y.Lsh(&m, uint(s))	// 1/2 < y < 1

	// Extract most significant 32 bits

	yh := uint32(y[3] >> 32)


	if yh == 0x80000000 { // Avoid overflow in division
		r0 = 0xffffffff
	} else {
		r0, _ = bits.Div32(0x80000000, 0, yh)
	}

	// First iteration: 32 -> 64

	t1 := uint64(r0)		// 2^31/y
	t1 *= t1			// 2^62/y^2
	t1, _ = bits.Mul64(t1, y[3])	// 2^62/y^2 * 2^64/y / 2^64 = 2^62/y

	r1 := uint64(r0) << 32		// 2^63/y
	r1 -= t1			// 2^63/y - 2^62/y = 2^62/y
	r1 *= 2				// 2^63/y

	if (r1 | (y[3]<<1)) == 0 {
		r1 = ^uint64(0)
	}

	// Second iteration: 64 -> 128

	// square: 2^126/y^2
	a2h, a2l := bits.Mul64(r1, r1)

	// multiply by y: e2h:e2l:b2h = 2^126/y^2 * 2^128/y / 2^128 = 2^126/y
	b2h, _   := bits.Mul64(a2l, y[2])
	c2h, c2l := bits.Mul64(a2l, y[3])
	d2h, d2l := bits.Mul64(a2h, y[2])
	e2h, e2l := bits.Mul64(a2h, y[3])

	b2h, c   := bits.Add64(b2h, c2l, 0)
	e2l, c    = bits.Add64(e2l, c2h, c)
	e2h, _    = bits.Add64(e2h,   0, c)

	_,   c    = bits.Add64(b2h, d2l, 0)
	e2l, c    = bits.Add64(e2l, d2h, c)
	e2h, _    = bits.Add64(e2h,   0, c)

	// subtract: t2h:t2l = 2^127/y - 2^126/y = 2^126/y
	t2l, b   := bits.Sub64( 0, e2l, 0)
	t2h, _   := bits.Sub64(r1, e2h, b)

	// double: r2h:r2l = 2^127/y
	r2l, c   := bits.Add64(t2l, t2l, 0)
	r2h, _   := bits.Add64(t2h, t2h, c)

	if (r2h | r2l | (y[3]<<1)) == 0 {
		r2h = ^uint64(0)
		r2l = ^uint64(0)
	}

	// Third iteration: 128 -> 192

	// square r2 (keep 256 bits): 2^190/y^2
	a3h, a3l := bits.Mul64(r2l, r2l)
	b3h, b3l := bits.Mul64(r2l, r2h)
	c3h, c3l := bits.Mul64(r2h, r2h)

	a3h, c    = bits.Add64(a3h, b3l, 0)
	c3l, c    = bits.Add64(c3l, b3h, c)
	c3h, _    = bits.Add64(c3h,   0, c)

	a3h, c    = bits.Add64(a3h, b3l, 0)
	c3l, c    = bits.Add64(c3l, b3h, c)
	c3h, _    = bits.Add64(c3h,   0, c)

	// multiply by y: q = 2^190/y^2 * 2^192/y / 2^192 = 2^190/y

	x0 := a3l
	x1 := a3h
	x2 := c3l
	x3 := c3h

	var q0, q1, q2, q3, q4, t0 uint64

	q0, _  = bits.Mul64(x2, y[0])
	q1, t0 = bits.Mul64(x3, y[0]);	q0, c = bits.Add64(q0, t0, 0);	q1, _ = bits.Add64(q1,  0, c)


	t1, _  = bits.Mul64(x1, y[1]);	q0, c = bits.Add64(q0, t1, 0)
	q2, t0 = bits.Mul64(x3, y[1]);	q1, c = bits.Add64(q1, t0, c);	q2, _ = bits.Add64(q2,  0, c)

	t1, t0 = bits.Mul64(x2, y[1]);	q0, c = bits.Add64(q0, t0, 0);	q1, c = bits.Add64(q1, t1, c);	q2, _ = bits.Add64(q2, 0, c)


	t1, t0 = bits.Mul64(x1, y[2]);	q0, c = bits.Add64(q0, t0, 0);	q1, c = bits.Add64(q1, t1, c)
	q3, t0 = bits.Mul64(x3, y[2]);	q2, c = bits.Add64(q2, t0, c);	q3, _ = bits.Add64(q3,  0, c)

	t1, _ = bits.Mul64(x0, y[2]);	q0, c = bits.Add64(q0, t1, 0)
	t1, t0 = bits.Mul64(x2, y[2]);	q1, c = bits.Add64(q1, t0, c);	q2, c = bits.Add64(q2, t1, c);	q3, _ = bits.Add64(q3, 0, c)


	t1, t0 = bits.Mul64(x1, y[3]);	q1, c = bits.Add64(q1, t0, 0);	q2, c = bits.Add64(q2, t1, c)
	q4, t0 = bits.Mul64(x3, y[3]);	q3, c = bits.Add64(q3, t0, c);	q4, _ = bits.Add64(q4,  0, c)

	t1, t0 = bits.Mul64(x0, y[3]);	q0, c = bits.Add64(q0, t0, 0);	q1, c = bits.Add64(q1, t1, c)
	t1, t0 = bits.Mul64(x2, y[3]);	q2, c = bits.Add64(q2, t0, c);	q3, c = bits.Add64(q3, t1, c);	q4, _ = bits.Add64(q4, 0, c)

	// subtract: t3 = 2^191/y - 2^190/y = 2^190/y
	_,   b  = bits.Sub64(  0, q0, 0)
	_,   b  = bits.Sub64(  0, q1, b)
	t3l, b := bits.Sub64(  0, q2, b)
	t3m, b := bits.Sub64(r2l, q3, b)
	t3h, _ := bits.Sub64(r2h, q4, b)

	// double: r3 = 2^191/y
	r3l, c := bits.Add64(t3l, t3l, 0)
	r3m, c := bits.Add64(t3m, t3m, c)
	r3h, _ := bits.Add64(t3h, t3h, c)

	if (r3h | r3m | r3l | (y[3]<<1)) == 0 {
		r3h = ^uint64(0)
		r3m = ^uint64(0)
		r3l = ^uint64(0)
	}

	// Fourth iteration: 192 -> 320

	// square r3

	a4h, a4l := bits.Mul64(r3l, r3l)
	b4h, b4l := bits.Mul64(r3l, r3m)
	c4h, c4l := bits.Mul64(r3l, r3h)
	d4h, d4l := bits.Mul64(r3m, r3m)
	e4h, e4l := bits.Mul64(r3m, r3h)
	f4h, f4l := bits.Mul64(r3h, r3h)

	b4h, c = bits.Add64(b4h, c4l, 0)
	e4l, c = bits.Add64(e4l, c4h, c)
	e4h, _ = bits.Add64(e4h,   0, c)

	a4h, c = bits.Add64(a4h, b4l, 0)
	d4l, c = bits.Add64(d4l, b4h, c)
	d4h, c = bits.Add64(d4h, e4l, c)
	f4l, c = bits.Add64(f4l, e4h, c)
	f4h, _ = bits.Add64(f4h,   0, c)

	a4h, c = bits.Add64(a4h, b4l, 0)
	d4l, c = bits.Add64(d4l, b4h, c)
	d4h, c = bits.Add64(d4h, e4l, c)
	f4l, c = bits.Add64(f4l, e4h, c)
	f4h, _ = bits.Add64(f4h,   0, c)

	// multiply by y

	x1, x0  = bits.Mul64(d4h, y[0])
	x3, x2  = bits.Mul64(f4h, y[0])
	t1, t0  = bits.Mul64(f4l, y[0]); x1, c = bits.Add64(x1, t0, 0); x2, c = bits.Add64(x2, t1, c)
					 x3, _ = bits.Add64(x3,  0, c)

	t1, t0  = bits.Mul64(d4h, y[1]); x1, c = bits.Add64(x1, t0, 0); x2, c = bits.Add64(x2, t1, c)
	x4, t0 := bits.Mul64(f4h, y[1]); x3, c = bits.Add64(x3, t0, c); x4, _ = bits.Add64(x4,  0, c)
	t1, t0  = bits.Mul64(d4l, y[1]); x0, c = bits.Add64(x0, t0, 0); x1, c = bits.Add64(x1, t1, c)
	t1, t0  = bits.Mul64(f4l, y[1]); x2, c = bits.Add64(x2, t0, c); x3, c = bits.Add64(x3, t1, c)
									x4, _ = bits.Add64(x4,  0, c)

	t1, t0  = bits.Mul64(a4h, y[2]); x0, c = bits.Add64(x0, t0, 0); x1, c = bits.Add64(x1, t1, c)
	t1, t0  = bits.Mul64(d4h, y[2]); x2, c = bits.Add64(x2, t0, c); x3, c = bits.Add64(x3, t1, c)
	x5, t0 := bits.Mul64(f4h, y[2]); x4, c = bits.Add64(x4, t0, c); x5, _ = bits.Add64(x5,  0, c)
	t1, t0  = bits.Mul64(d4l, y[2]); x1, c = bits.Add64(x1, t0, 0); x2, c = bits.Add64(x2, t1, c)
	t1, t0  = bits.Mul64(f4l, y[2]); x3, c = bits.Add64(x3, t0, c); x4, c = bits.Add64(x4, t1, c)
					 x5, _ = bits.Add64(x5,  0, c)

	t1, t0  = bits.Mul64(a4h, y[3]); x1, c = bits.Add64(x1, t0, 0); x2, c = bits.Add64(x2, t1, c)
	t1, t0  = bits.Mul64(d4h, y[3]); x3, c = bits.Add64(x3, t0, c); x4, c = bits.Add64(x4, t1, c)
	x6, t0 := bits.Mul64(f4h, y[3]); x5, c = bits.Add64(x5, t0, c); x6, _ = bits.Add64(x6,  0, c)
	t1, t0  = bits.Mul64(a4l, y[3]); x0, c = bits.Add64(x0, t0, 0); x1, c = bits.Add64(x1, t1, c)
	t1, t0  = bits.Mul64(d4l, y[3]); x2, c = bits.Add64(x2, t0, c); x3, c = bits.Add64(x3, t1, c)
	t1, t0  = bits.Mul64(f4l, y[3]); x4, c = bits.Add64(x4, t0, c); x5, c = bits.Add64(x5, t1, c)
									x6, _ = bits.Add64(x6,  0, c)

	// subtract
	_,   b	 = bits.Sub64(  0, x0, 0)
	_,   b	 = bits.Sub64(  0, x1, b)
	r4l, b	:= bits.Sub64(  0, x2, b)
	r4k, b	:= bits.Sub64(  0, x3, b)
	r4j, b	:= bits.Sub64(r3l, x4, b)
	r4i, b	:= bits.Sub64(r3m, x5, b)
	r4h, _	:= bits.Sub64(r3h, x6, b)

	// Multiply candidate for 1/4y by y, with full precision

	x0 = r4l
	x1 = r4k
	x2 = r4j
	x3 = r4i
	x4 = r4h

	q1, q0	 = bits.Mul64(x0, y[0])
	q3, q2	 = bits.Mul64(x2, y[0])
	q5, q4	:= bits.Mul64(x4, y[0])

	t1, t0	 = bits.Mul64(x1, y[0]); q1, c = bits.Add64(q1, t0, 0); q2, c = bits.Add64(q2, t1, c)
	t1, t0	 = bits.Mul64(x3, y[0]); q3, c = bits.Add64(q3, t0, c); q4, c = bits.Add64(q4, t1, c); q5, _ = bits.Add64(q5, 0, c)

	t1, t0	 = bits.Mul64(x0, y[1]); q1, c = bits.Add64(q1, t0, 0); q2, c = bits.Add64(q2, t1, c)
	t1, t0	 = bits.Mul64(x2, y[1]); q3, c = bits.Add64(q3, t0, c); q4, c = bits.Add64(q4, t1, c)
	q6, t0	:= bits.Mul64(x4, y[1]); q5, c = bits.Add64(q5, t0, c); q6, _ = bits.Add64(q6,  0, c)

	t1, t0	 = bits.Mul64(x1, y[1]); q2, c = bits.Add64(q2, t0, 0); q3, c = bits.Add64(q3, t1, c)
	t1, t0	 = bits.Mul64(x3, y[1]); q4, c = bits.Add64(q4, t0, c); q5, c = bits.Add64(q5, t1, c); q6, _ = bits.Add64(q6, 0, c)

	t1, t0	 = bits.Mul64(x0, y[2]); q2, c = bits.Add64(q2, t0, 0); q3, c = bits.Add64(q3, t1, c)
	t1, t0	 = bits.Mul64(x2, y[2]); q4, c = bits.Add64(q4, t0, c); q5, c = bits.Add64(q5, t1, c)
	q7, t0	:= bits.Mul64(x4, y[2]); q6, c = bits.Add64(q6, t0, c); q7, _ = bits.Add64(q7,  0, c)

	t1, t0	 = bits.Mul64(x1, y[2]); q3, c = bits.Add64(q3, t0, 0); q4, c = bits.Add64(q4, t1, c)
	t1, t0	 = bits.Mul64(x3, y[2]); q5, c = bits.Add64(q5, t0, c); q6, c = bits.Add64(q6, t1, c); q7, _ = bits.Add64(q7, 0, c)

	t1, t0	 = bits.Mul64(x0, y[3]); q3, c = bits.Add64(q3, t0, 0); q4, c = bits.Add64(q4, t1, c)
	t1, t0	 = bits.Mul64(x2, y[3]); q5, c = bits.Add64(q5, t0, c); q6, c = bits.Add64(q6, t1, c)
	q8, t0	:= bits.Mul64(x4, y[3]); q7, c = bits.Add64(q7, t0, c); q8, _ = bits.Add64(q8,  0, c)

	t1, t0	 = bits.Mul64(x1, y[3]); q4, c = bits.Add64(q4, t0, 0); q5, c = bits.Add64(q5, t1, c)
	t1, t0	 = bits.Mul64(x3, y[3]); q6, c = bits.Add64(q6, t0, c); q7, c = bits.Add64(q7, t1, c); q8, _ = bits.Add64(q8, 0, c)

	// Final adjustment

	// subtract q from 1/4
	_, b = bits.Sub64(0, q0, 0)
	_, b = bits.Sub64(0, q1, b)
	_, b = bits.Sub64(0, q2, b)
	_, b = bits.Sub64(0, q3, b)
	_, b = bits.Sub64(0, q4, b)
	_, b = bits.Sub64(0, q5, b)
	_, b = bits.Sub64(0, q6, b)
	_, b = bits.Sub64(0, q7, b)
	_, b = bits.Sub64(uint64(1) << 62, q8, b)

	if b != 0 {
		r4l, b	= bits.Sub64(r4l, 1, 0)
		r4k, b	= bits.Sub64(r4k, 0, b)
		r4j, b	= bits.Sub64(r4j, 0, b)
		r4i, b	= bits.Sub64(r4i, 0, b)
		r4h, _	= bits.Sub64(r4h, 0, b)
	}

	mu[0] = r4l
	mu[1] = r4k
	mu[2] = r4j
	mu[3] = r4i
	mu[4] = r4h

	// Shift into appropriate bit alignment, truncating excess bits

	switch (p & 63) - 1 {
	case -1:
		mu[0], c = bits.Add64(mu[0], mu[0], 0)
		mu[1], c = bits.Add64(mu[1], mu[1], c)
		mu[2], c = bits.Add64(mu[2], mu[2], c)
		mu[3], c = bits.Add64(mu[3], mu[3], c)
		mu[4], _ = bits.Add64(mu[4], mu[4], c)
	case 0:
	default:
		mu = shiftright320(mu, uint((p & 63) - 1))
	}

	// Store the reciprocal in the cache
	if cache != nil{
		cache.put(m, cacheIndex, mu)
	}

	return mu
}

// reduce4 computes the least non-negative residue of x modulo m
//
// requires a four-word modulus (m[3] > 1) and its inverse (mu)

func reduce4(x [8]uint64, m Int, mu [5]uint64) (z Int) {

	// NB: Most variable names in the comments match the pseudocode for
	// 	Barrett reduction in the Handbook of Applied Cryptography.

	// q1 = x/2^192

	x0 := x[3]
	x1 := x[4]
	x2 := x[5]
	x3 := x[6]
	x4 := x[7]

	// q2 = q1 * mu; q3 = q2 / 2^320

	var q0, q1, q2, q3, q4, q5, t0, t1, c uint64

	q0, _  = bits.Mul64(x3, mu[0])
	q1, t0 = bits.Mul64(x4, mu[0]);	q0, c = bits.Add64(q0, t0, 0);	q1, _ = bits.Add64(q1,  0, c)


	t1, _  = bits.Mul64(x2, mu[1]);	q0, c = bits.Add64(q0, t1, 0)
	q2, t0 = bits.Mul64(x4, mu[1]);	q1, c = bits.Add64(q1, t0, c);	q2, _ = bits.Add64(q2,  0, c)

	t1, t0 = bits.Mul64(x3, mu[1]);	q0, c = bits.Add64(q0, t0, 0);	q1, c = bits.Add64(q1, t1, c); q2, _ = bits.Add64(q2, 0, c)


	t1, t0 = bits.Mul64(x2, mu[2]);	q0, c = bits.Add64(q0, t0, 0);	q1, c = bits.Add64(q1, t1, c)
	q3, t0 = bits.Mul64(x4, mu[2]);	q2, c = bits.Add64(q2, t0, c);	q3, _ = bits.Add64(q3,  0, c)

	t1, _  = bits.Mul64(x1, mu[2]);	q0, c = bits.Add64(q0, t1, 0)
	t1, t0 = bits.Mul64(x3, mu[2]);	q1, c = bits.Add64(q1, t0, c);	q2, c = bits.Add64(q2, t1, c); q3, _ = bits.Add64(q3, 0, c)


	t1, _  = bits.Mul64(x0, mu[3]);	q0, c = bits.Add64(q0, t1, 0)
	t1, t0 = bits.Mul64(x2, mu[3]);	q1, c = bits.Add64(q1, t0, c);	q2, c = bits.Add64(q2, t1, c)
	q4, t0 = bits.Mul64(x4, mu[3]);	q3, c = bits.Add64(q3, t0, c);	q4, _ = bits.Add64(q4,  0, c)

	t1, t0 = bits.Mul64(x1, mu[3]);	q0, c = bits.Add64(q0, t0, 0);	q1, c = bits.Add64(q1, t1, c)
	t1, t0 = bits.Mul64(x3, mu[3]);	q2, c = bits.Add64(q2, t0, c);	q3, c = bits.Add64(q3, t1, c); q4, _ = bits.Add64(q4, 0, c)


	t1, t0 = bits.Mul64(x0, mu[4]);	_,  c = bits.Add64(q0, t0, 0);	q1, c = bits.Add64(q1, t1, c)
	t1, t0 = bits.Mul64(x2, mu[4]);	q2, c = bits.Add64(q2, t0, c);	q3, c = bits.Add64(q3, t1, c)
	q5, t0 = bits.Mul64(x4, mu[4]);	q4, c = bits.Add64(q4, t0, c);	q5, _ = bits.Add64(q5,  0, c)

	t1, t0 = bits.Mul64(x1, mu[4]);	q1, c = bits.Add64(q1, t0, 0);	q2, c = bits.Add64(q2, t1, c)
	t1, t0 = bits.Mul64(x3, mu[4]);	q3, c = bits.Add64(q3, t0, c);	q4, c = bits.Add64(q4, t1, c); q5, _ = bits.Add64(q5, 0, c)

	// Drop the fractional part of q3

	q0 = q1
	q1 = q2
	q2 = q3
	q3 = q4
	q4 = q5

	// r1 = x mod 2^320

	x0 = x[0]
	x1 = x[1]
	x2 = x[2]
	x3 = x[3]
	x4 = x[4]

	// r2 = q3 * m mod 2^320

	var r0, r1, r2, r3, r4 uint64

	r4, r3 = bits.Mul64(q0, m[3])
	_,  t0 = bits.Mul64(q1, m[3]);	r4, _ = bits.Add64(r4, t0, 0)


	t1, r2 = bits.Mul64(q0, m[2]);	r3, c = bits.Add64(r3, t1, 0)
	_,  t0 = bits.Mul64(q2, m[2]);	r4, _ = bits.Add64(r4, t0, c)

	t1, t0 = bits.Mul64(q1, m[2]);	r3, c = bits.Add64(r3, t0, 0);	r4, _ = bits.Add64(r4, t1, c)


	t1, r1 = bits.Mul64(q0, m[1]);	r2, c = bits.Add64(r2, t1, 0)
	t1, t0 = bits.Mul64(q2, m[1]);	r3, c = bits.Add64(r3, t0, c);	r4, _ = bits.Add64(r4, t1, c)

	t1, t0 = bits.Mul64(q1, m[1]);	r2, c = bits.Add64(r2, t0, 0);	r3, c = bits.Add64(r3, t1, c)
	_,  t0 = bits.Mul64(q3, m[1]);	r4, _ = bits.Add64(r4, t0, c)


	t1, r0 = bits.Mul64(q0, m[0]);	r1, c = bits.Add64(r1, t1, 0)
	t1, t0 = bits.Mul64(q2, m[0]);	r2, c = bits.Add64(r2, t0, c);	r3, c = bits.Add64(r3, t1, c)
	_,  t0 = bits.Mul64(q4, m[0]);	r4, _ = bits.Add64(r4, t0, c)

	t1, t0 = bits.Mul64(q1, m[0]);	r1, c = bits.Add64(r1, t0, 0);	r2, c = bits.Add64(r2, t1, c)
	t1, t0 = bits.Mul64(q3, m[0]);	r3, c = bits.Add64(r3, t0, c);	r4, _ = bits.Add64(r4, t1, c)


	// r = r1 - r2

	var b uint64

	r0, b = bits.Sub64(x0, r0, 0)
	r1, b = bits.Sub64(x1, r1, b)
	r2, b = bits.Sub64(x2, r2, b)
	r3, b = bits.Sub64(x3, r3, b)
	r4, b = bits.Sub64(x4, r4, b)

	// if r<0 then r+=m

	if b != 0 {
		r0, c = bits.Add64(r0, m[0], 0)
		r1, c = bits.Add64(r1, m[1], c)
		r2, c = bits.Add64(r2, m[2], c)
		r3, c = bits.Add64(r3, m[3], c)
		r4, _ = bits.Add64(r4,    0, c)
	}

	// while (r>=m) r-=m

	for {
		// q = r - m
		q0, b = bits.Sub64(r0, m[0], 0)
		q1, b = bits.Sub64(r1, m[1], b)
		q2, b = bits.Sub64(r2, m[2], b)
		q3, b = bits.Sub64(r3, m[3], b)
		q4, b = bits.Sub64(r4,    0, b)

		// if borrow break
		if b != 0 {
			break
		}

		// r = q
		r4, r3, r2, r1, r0 = q4, q3, q2, q1, q0
	}

	z[3], z[2], z[1], z[0] = r3, r2, r1, r0

	return z
}
