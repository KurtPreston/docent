package model

import (
	"math"
	"strings"
)

// ColorForName returns a deterministic hex color for a name (grove ForBranch port).
func ColorForName(name string) string {
	sum := posixCksum([]byte(name))
	hue := float64(sum % 360)
	return oklchHex(0.70, 0.14, hue)
}

// ForegroundForHex picks black or white text for legibility on a background hex.
func ForegroundForHex(hex string) string {
	h := strings.TrimPrefix(strings.TrimSpace(hex), "#")
	if len(h) < 6 {
		return "#ffffff"
	}
	r := hexByte(h[0:2])
	g := hexByte(h[2:4])
	b := hexByte(h[4:6])
	lum := (r*299 + g*587 + b*114) / 1000
	if lum > 140 {
		return "#000000"
	}
	return "#ffffff"
}

func hexByte(s string) int {
	var v int
	for i := 0; i < len(s); i++ {
		c := s[i]
		v <<= 4
		switch {
		case c >= '0' && c <= '9':
			v |= int(c - '0')
		case c >= 'a' && c <= 'f':
			v |= int(c-'a') + 10
		case c >= 'A' && c <= 'F':
			v |= int(c-'A') + 10
		}
	}
	return v
}

var crcTable [256]uint32

func init() {
	const poly uint32 = 0x04C11DB7
	for i := 0; i < 256; i++ {
		c := uint32(i) << 24
		for k := 0; k < 8; k++ {
			if c&0x80000000 != 0 {
				c = (c << 1) ^ poly
			} else {
				c <<= 1
			}
		}
		crcTable[i] = c
	}
}

func posixCksum(data []byte) uint32 {
	var crc uint32
	for _, b := range data {
		idx := byte((crc>>24)^uint32(b)) & 0xff
		crc = (crc << 8) ^ crcTable[idx]
	}
	n := len(data)
	for n > 0 {
		idx := byte((crc>>24)^uint32(n&0xff)) & 0xff
		crc = (crc << 8) ^ crcTable[idx]
		n >>= 8
	}
	return ^crc
}

func oklchHex(l, c, hueDeg float64) string {
	h := hueDeg * math.Pi / 180
	a := c * math.Cos(h)
	b := c * math.Sin(h)
	lp := l + 0.3963377774*a + 0.2158037573*b
	mp := l - 0.1055613458*a - 0.0638541728*b
	sp := l - 0.0894841775*a - 1.2914855480*b
	lc := lp * lp * lp
	mc := mp * mp * mp
	sc := sp * sp * sp
	r := 4.0767416621*lc - 3.3077115913*mc + 0.2309699292*sc
	g := -1.2684380046*lc + 2.6097574011*mc - 0.3413193965*sc
	bl := -0.0041960863*lc - 0.7034186147*mc + 1.7076147010*sc
	return rgbHex(linearToByte(r), linearToByte(g), linearToByte(bl))
}

func linearToByte(v float64) int {
	if v <= 0 {
		return 0
	}
	if v >= 1 {
		return 255
	}
	if v <= 0.0031308 {
		v *= 12.92
	} else {
		v = 1.055*math.Pow(v, 1.0/2.4) - 0.055
	}
	x := int(math.Round(v * 255))
	if x < 0 {
		return 0
	}
	if x > 255 {
		return 255
	}
	return x
}

func rgbHex(r, g, b int) string {
	const hexdigits = "0123456789abcdef"
	return "#" + string([]byte{
		hexdigits[r>>4], hexdigits[r&0xf],
		hexdigits[g>>4], hexdigits[g&0xf],
		hexdigits[b>>4], hexdigits[b&0xf],
	})
}
