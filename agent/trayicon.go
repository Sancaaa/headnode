package agent

import (
	"encoding/binary"
	"math"
)

// The tray icon is the logo's mark (docs/logo/README.md, section 4) drawn here in one
// color, so the code carries no picture files: green when headnode is connected, grey
// when it is not. A Windows icon file (.ico) is a small header followed by one bitmap
// per size, and the tray library wants those bytes.

// trayIcon returns an .ico holding the mark in the given color, at 16 and 32 pixels.
func trayIcon(r, g, b uint8) []byte {
	sizes := []int{16, 32}

	var images [][]byte
	for _, size := range sizes {
		images = append(images, iconImage(size, r, g, b))
	}

	header := make([]byte, 6+16*len(sizes))
	binary.LittleEndian.PutUint16(header[2:], 1) // type 1: an icon
	binary.LittleEndian.PutUint16(header[4:], uint16(len(sizes)))
	offset := len(header)
	for i, size := range sizes {
		entry := header[6+16*i:]
		entry[0] = byte(size) // width
		entry[1] = byte(size) // height
		binary.LittleEndian.PutUint16(entry[4:], 1)  // planes
		binary.LittleEndian.PutUint16(entry[6:], 32) // bits per pixel
		binary.LittleEndian.PutUint32(entry[8:], uint32(len(images[i])))
		binary.LittleEndian.PutUint32(entry[12:], uint32(offset))
		offset += len(images[i])
	}
	out := header
	for _, img := range images {
		out = append(out, img...)
	}
	return out
}

// iconImage is one size of the icon: a bitmap header, the pixels (blue, green, red,
// alpha; the bottom row first, as a Windows bitmap has it), and an all-zero mask that
// the alpha channel makes unnecessary but the format still requires.
func iconImage(size int, r, g, b uint8) []byte {
	maskStride := (size + 31) / 32 * 4
	img := make([]byte, 40+size*size*4+maskStride*size)
	binary.LittleEndian.PutUint32(img[0:], 40)            // header size
	binary.LittleEndian.PutUint32(img[4:], uint32(size))  // width
	binary.LittleEndian.PutUint32(img[8:], uint32(size*2)) // height: the bitmap and its mask
	binary.LittleEndian.PutUint16(img[12:], 1)            // planes
	binary.LittleEndian.PutUint16(img[14:], 32)           // bits per pixel

	scale := 64 / float64(size) // the mark is drawn on a 64 x 64 grid
	const sub = 4               // 4 x 4 samples per pixel: smooth edges
	for y := 0; y < size; y++ {
		for x := 0; x < size; x++ {
			inside := 0
			for sy := 0; sy < sub; sy++ {
				for sx := 0; sx < sub; sx++ {
					px := (float64(x) + (float64(sx)+0.5)/sub) * scale
					py := (float64(y) + (float64(sy)+0.5)/sub) * scale
					if inMark(px, py) {
						inside++
					}
				}
			}
			alpha := byte(255 * inside / (sub * sub))
			p := img[40+((size-1-y)*size+x)*4:]
			p[0], p[1], p[2], p[3] = b, g, r, alpha
		}
	}
	return img
}

// The mark: six outer nodes on a hexagon, a larger node in the middle, and a spoke
// from the middle to each outer node. Numbers are the logo's own.
var markNodes = [][2]float64{
	{32, 8}, {52.78, 20}, {52.78, 44}, {32, 56}, {11.22, 44}, {11.22, 20},
}

const (
	markCenter     = 32.0
	markHeadRadius = 8.0
	markNodeRadius = 5.0
	markSpokeWidth = 3.0
)

// inMark says whether a point of the 64 x 64 grid is part of the drawing.
func inMark(x, y float64) bool {
	if math.Hypot(x-markCenter, y-markCenter) <= markHeadRadius {
		return true
	}
	for _, n := range markNodes {
		if math.Hypot(x-n[0], y-n[1]) <= markNodeRadius {
			return true
		}
		if distanceToSegment(x, y, markCenter, markCenter, n[0], n[1]) <= markSpokeWidth/2 {
			return true
		}
	}
	return false
}

// distanceToSegment is how far the point (px, py) is from the line piece (ax, ay) to (bx, by).
func distanceToSegment(px, py, ax, ay, bx, by float64) float64 {
	dx, dy := bx-ax, by-ay
	t := ((px-ax)*dx + (py-ay)*dy) / (dx*dx + dy*dy)
	t = math.Max(0, math.Min(1, t))
	return math.Hypot(px-(ax+t*dx), py-(ay+t*dy))
}
