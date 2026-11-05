// Package lzma decodes the LZMA1 "alone" stream format using only the Go
// standard library, because the repository forbids third-party dependencies.
//
// It exists for one concrete reason: Dukascopy distributes historical market
// data as `.bi5` files, which are LZMA-alone streams. Decoding is the only
// operation implemented; nothing here compresses.
//
// The implementation follows the reference LzmaSpec decoder: a binary range
// decoder over adaptive bit probabilities, an LZ77 match copier, and the
// 12-state literal/match context machine.
package lzma

import (
	"encoding/binary"
	"errors"
	"fmt"
)

const (
	headerSize = 13 // 1 props byte + 4 dictionary-size bytes + 8 size bytes

	numStates        = 12
	numPosBitsMax    = 4
	matchMinLen      = 2
	numAlignBits     = 4
	endPosModelIndex = 14
	numFullDistances = 1 << (endPosModelIndex >> 1)
	numLitStates     = 7

	probInitValue = 1024 // 2048/2: a half-probability in 11-bit fixed point
	numMoveBits   = 5
	topValue      = 1 << 24

	// sizeUnknown marks a stream whose length is only discoverable by
	// reaching the end-of-stream marker.
	sizeUnknown = ^uint64(0)

	// maxOutput caps decoded output so a corrupt length field cannot drive an
	// unbounded allocation. Market-data payloads are far below this.
	maxOutput = 256 << 20
)

var (
	ErrHeader    = errors.New("lzma: malformed alone header")
	ErrCorrupt   = errors.New("lzma: corrupt stream")
	ErrTruncated = errors.New("lzma: truncated stream")
	ErrTooLarge  = errors.New("lzma: declared size exceeds the decoder limit")
)

// Decode decompresses a complete LZMA-alone stream.
//
// The 8-byte size field is honoured when present. Dukascopy writes a real
// size there; streams that write the unknown-size sentinel are decoded until
// the end-of-stream marker instead.
func Decode(source []byte) ([]byte, error) {
	if len(source) < headerSize {
		return nil, ErrHeader
	}
	properties := source[0]
	if properties >= 9*5*5 {
		return nil, fmt.Errorf("%w: properties byte %#02x", ErrHeader, properties)
	}
	literalContextBits := int(properties % 9)
	remainder := properties / 9
	literalPosBits := int(remainder % 5)
	posBits := int(remainder / 5)

	dictionarySize := binary.LittleEndian.Uint32(source[1:5])
	if dictionarySize < 1<<12 {
		dictionarySize = 1 << 12
	}
	declaredSize := binary.LittleEndian.Uint64(source[5:13])
	if declaredSize != sizeUnknown && declaredSize > maxOutput {
		return nil, ErrTooLarge
	}

	decoder := &decoder{
		literalContextBits: literalContextBits,
		literalPosBits:     literalPosBits,
		posMask:            (1 << posBits) - 1,
		dictionarySize:     dictionarySize,
		declaredSize:       declaredSize,
	}
	if declaredSize != sizeUnknown {
		decoder.out = make([]byte, 0, declaredSize)
	}
	if err := decoder.initRange(source[headerSize:]); err != nil {
		return nil, err
	}
	decoder.reset()
	if err := decoder.run(); err != nil {
		return nil, err
	}
	if declaredSize != sizeUnknown && uint64(len(decoder.out)) != declaredSize {
		return nil, fmt.Errorf("%w: decoded %d bytes, header declared %d",
			ErrCorrupt, len(decoder.out), declaredSize)
	}
	return decoder.out, nil
}

type decoder struct {
	literalContextBits int
	literalPosBits     int
	posMask            int
	dictionarySize     uint32
	declaredSize       uint64

	// Range decoder state.
	source []byte
	cursor int
	code   uint32
	window uint32

	// Output doubles as the LZ77 dictionary: distances index backwards from
	// its tail, so no separate circular buffer is needed.
	out []byte

	state                  uint32
	rep0, rep1, rep2, rep3 uint32
	literalProbs           []uint16
	isMatch                [numStates << numPosBitsMax]uint16
	isRep                  [numStates]uint16
	isRepG0                [numStates]uint16
	isRepG1                [numStates]uint16
	isRepG2                [numStates]uint16
	isRep0Long             [numStates << numPosBitsMax]uint16
	posSlot                [4][1 << 6]uint16
	posDecoders            [1 + numFullDistances - endPosModelIndex]uint16
	align                  [1 << numAlignBits]uint16
	lenCoder, repLenCoder  lengthCoder
}

type lengthCoder struct {
	choice  uint16
	choice2 uint16
	low     [1 << numPosBitsMax][1 << 3]uint16
	mid     [1 << numPosBitsMax][1 << 3]uint16
	high    [1 << 8]uint16
}

func (c *lengthCoder) reset() {
	c.choice, c.choice2 = probInitValue, probInitValue
	for i := range c.low {
		fill(c.low[i][:])
		fill(c.mid[i][:])
	}
	fill(c.high[:])
}

func fill(probs []uint16) {
	for i := range probs {
		probs[i] = probInitValue
	}
}

func (d *decoder) reset() {
	d.state = 0
	d.rep0, d.rep1, d.rep2, d.rep3 = 0, 0, 0, 0
	d.literalProbs = make([]uint16, 0x300<<(d.literalContextBits+d.literalPosBits))
	fill(d.literalProbs)
	fill(d.isMatch[:])
	fill(d.isRep[:])
	fill(d.isRepG0[:])
	fill(d.isRepG1[:])
	fill(d.isRepG2[:])
	fill(d.isRep0Long[:])
	for i := range d.posSlot {
		fill(d.posSlot[i][:])
	}
	fill(d.posDecoders[:])
	fill(d.align[:])
	d.lenCoder.reset()
	d.repLenCoder.reset()
}

// initRange primes the range coder. The first compressed byte is required to
// be zero by the format.
func (d *decoder) initRange(source []byte) error {
	if len(source) < 5 {
		return ErrTruncated
	}
	if source[0] != 0 {
		return fmt.Errorf("%w: range coder must start with a zero byte", ErrCorrupt)
	}
	d.source = source
	d.cursor = 1
	d.window = 0xFFFFFFFF
	d.code = 0
	for i := 0; i < 4; i++ {
		d.code = d.code<<8 | uint32(d.nextByte())
	}
	return nil
}

// nextByte yields zero past the end of input. A stream that genuinely needs
// those bytes fails the final size or marker check instead of panicking.
func (d *decoder) nextByte() byte {
	if d.cursor >= len(d.source) {
		d.cursor++
		return 0
	}
	value := d.source[d.cursor]
	d.cursor++
	return value
}

func (d *decoder) overrun() bool { return d.cursor > len(d.source)+8 }

func (d *decoder) normalize() {
	if d.window < topValue {
		d.window <<= 8
		d.code = d.code<<8 | uint32(d.nextByte())
	}
}

func (d *decoder) decodeBit(prob *uint16) uint32 {
	bound := (d.window >> 11) * uint32(*prob)
	var bit uint32
	if d.code < bound {
		*prob += (2048 - *prob) >> numMoveBits
		d.window = bound
		bit = 0
	} else {
		*prob -= *prob >> numMoveBits
		d.code -= bound
		d.window -= bound
		bit = 1
	}
	d.normalize()
	return bit
}

func (d *decoder) decodeDirectBits(count int) uint32 {
	var result uint32
	for ; count > 0; count-- {
		d.window >>= 1
		d.code -= d.window
		t := 0 - (d.code >> 31)
		d.code += d.window & t
		d.normalize()
		result = result<<1 + t + 1
	}
	return result
}

func (d *decoder) bitTree(probs []uint16, bits int) uint32 {
	m := uint32(1)
	for i := 0; i < bits; i++ {
		m = m<<1 + d.decodeBit(&probs[m])
	}
	return m - uint32(1)<<bits
}

func (d *decoder) bitTreeReverse(probs []uint16, bits int) uint32 {
	m := uint32(1)
	var symbol uint32
	for i := 0; i < bits; i++ {
		bit := d.decodeBit(&probs[m])
		m = m<<1 + bit
		symbol |= bit << i
	}
	return symbol
}

func (c *lengthCoder) decode(d *decoder, posState int) uint32 {
	if d.decodeBit(&c.choice) == 0 {
		return d.bitTree(c.low[posState][:], 3)
	}
	if d.decodeBit(&c.choice2) == 0 {
		return 8 + d.bitTree(c.mid[posState][:], 3)
	}
	return 16 + d.bitTree(c.high[:], 8)
}

func (d *decoder) decodeDistance(length uint32) uint32 {
	lenState := length
	if lenState > 3 {
		lenState = 3
	}
	slot := d.bitTree(d.posSlot[lenState][:], 6)
	if slot < 4 {
		return slot
	}
	directBits := int(slot>>1) - 1
	distance := (2 | (slot & 1)) << uint(directBits)
	if slot < endPosModelIndex {
		distance += d.bitTreeReverse(d.posDecoders[distance-slot:], directBits)
	} else {
		distance += d.decodeDirectBits(directBits-numAlignBits) << numAlignBits
		distance += d.bitTreeReverse(d.align[:], numAlignBits)
	}
	return distance
}

func (d *decoder) byteAt(distance uint32) byte {
	return d.out[uint32(len(d.out))-distance]
}

func (d *decoder) putByte(value byte) {
	d.out = append(d.out, value)
}

func (d *decoder) decodeLiteral(state uint32, rep0 uint32) {
	var previous byte
	if len(d.out) > 0 {
		previous = d.byteAt(1)
	}
	literalState := ((len(d.out) & ((1 << d.literalPosBits) - 1)) << d.literalContextBits) +
		int(previous>>(8-uint(d.literalContextBits)))
	probs := d.literalProbs[0x300*literalState:]

	symbol := uint32(1)
	if state >= numLitStates {
		matchByte := d.byteAt(rep0 + 1)
		for symbol < 0x100 {
			matchBit := uint32(matchByte>>7) & 1
			matchByte <<= 1
			bit := d.decodeBit(&probs[((1+matchBit)<<8)+symbol])
			symbol = symbol<<1 | bit
			if matchBit != bit {
				break
			}
		}
	}
	for symbol < 0x100 {
		symbol = symbol<<1 | d.decodeBit(&probs[symbol])
	}
	d.putByte(byte(symbol - 0x100))
}

func (d *decoder) run() error {
	for {
		if d.declaredSize != sizeUnknown && uint64(len(d.out)) >= d.declaredSize {
			return nil
		}
		if uint64(len(d.out)) > maxOutput {
			return ErrTooLarge
		}
		if d.overrun() {
			return ErrTruncated
		}

		posState := len(d.out) & d.posMask
		if d.decodeBit(&d.isMatch[(d.state<<numPosBitsMax)+uint32(posState)]) == 0 {
			d.decodeLiteral(d.state, d.rep0)
			switch {
			case d.state < 4:
				d.state = 0
			case d.state < 10:
				d.state -= 3
			default:
				d.state -= 6
			}
			continue
		}

		var length uint32
		if d.decodeBit(&d.isRep[d.state]) != 0 {
			if len(d.out) == 0 {
				return fmt.Errorf("%w: repeat match with an empty dictionary", ErrCorrupt)
			}
			if d.decodeBit(&d.isRepG0[d.state]) == 0 {
				if d.decodeBit(&d.isRep0Long[(d.state<<numPosBitsMax)+uint32(posState)]) == 0 {
					if d.state < numLitStates {
						d.state = 9
					} else {
						d.state = 11
					}
					d.putByte(d.byteAt(d.rep0 + 1))
					continue
				}
			} else {
				var distance uint32
				if d.decodeBit(&d.isRepG1[d.state]) == 0 {
					distance = d.rep1
				} else {
					if d.decodeBit(&d.isRepG2[d.state]) == 0 {
						distance = d.rep2
					} else {
						distance = d.rep3
						d.rep3 = d.rep2
					}
					d.rep2 = d.rep1
				}
				d.rep1 = d.rep0
				d.rep0 = distance
			}
			length = d.repLenCoder.decode(d, posState) + matchMinLen
			if d.state < numLitStates {
				d.state = 8
			} else {
				d.state = 11
			}
		} else {
			d.rep3, d.rep2, d.rep1 = d.rep2, d.rep1, d.rep0
			length = d.lenCoder.decode(d, posState)
			if d.state < numLitStates {
				d.state = 7
			} else {
				d.state = 10
			}
			d.rep0 = d.decodeDistance(length)
			length += matchMinLen
			if d.rep0 == 0xFFFFFFFF {
				// End-of-stream marker.
				if d.declaredSize == sizeUnknown || uint64(len(d.out)) == d.declaredSize {
					return nil
				}
				return fmt.Errorf("%w: early end marker at %d bytes", ErrCorrupt, len(d.out))
			}
			if d.rep0 >= d.dictionarySize || uint64(d.rep0) >= uint64(len(d.out)) {
				return fmt.Errorf("%w: match distance %d out of range at %d bytes",
					ErrCorrupt, d.rep0, len(d.out))
			}
		}

		// Copy the match byte by byte: source and destination legitimately
		// overlap when the distance is shorter than the length.
		distance := d.rep0 + 1
		for i := uint32(0); i < length; i++ {
			if d.declaredSize != sizeUnknown && uint64(len(d.out)) >= d.declaredSize {
				break
			}
			d.putByte(d.byteAt(distance))
		}
	}
}
