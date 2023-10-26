// Copyright (C) 2019-2023, Ava Labs, Inc. All rights reserved.
// See the file LICENSE for licensing terms.

package merkledb

import (
	"errors"
	"fmt"
	"strings"
	"unsafe"

	"golang.org/x/exp/slices"
)

var (
	ErrInvalidTokenConfig = errors.New("token configuration must match one of the predefined configurations ")

	BranchFactor2TokenConfig = tokenConfiguration{
		branchFactor: 2,
		bitsPerToken: 1,
	}
	BranchFactor4TokenConfig = tokenConfiguration{
		branchFactor: 4,
		bitsPerToken: 2,
	}
	BranchFactor16TokenConfig = tokenConfiguration{
		branchFactor: 16,
		bitsPerToken: 4,
	}
	BranchFactor256TokenConfig = tokenConfiguration{
		branchFactor: 256,
		bitsPerToken: 8,
	}
	validTokenConfigurations = []tokenConfiguration{
		BranchFactor2TokenConfig,
		BranchFactor4TokenConfig,
		BranchFactor16TokenConfig,
		BranchFactor256TokenConfig,
	}
)

type BranchFactor int

const (
	BranchFactor2   BranchFactor = 2
	BranchFactor4   BranchFactor = 4
	BranchFactor16  BranchFactor = 16
	BranchFactor256 BranchFactor = 256
)

func (bf BranchFactor) Valid() error {
	switch bf {
	case BranchFactor2, BranchFactor4, BranchFactor16, BranchFactor256:
		return nil
	default:
		return fmt.Errorf("%w: %d", ErrInvalidTokenConfig, bf) // TODO fix error
	}
}

func (bf BranchFactor) BitsPerToken() int {
	switch bf {
	case BranchFactor2:
		return 1
	case BranchFactor4:
		return 2
	case BranchFactor16:
		return 4
	case BranchFactor256:
		return 8
	default:
		// This should never happen
		return -1
	}
}

type tokenConfiguration struct {
	branchFactor int
	bitsPerToken int
}

func NewToken(val byte, branchFactor BranchFactor) Token {
	return Token{
		value:  val,
		length: branchFactor.BitsPerToken(),
	}
}

type Token struct {
	length int
	value  byte
}

func (t tokenConfiguration) ToToken(val byte) Token {
	return Token{
		value:  val,
		length: t.bitsPerToken,
	}
}

func (t tokenConfiguration) Valid() error {
	for _, validConfig := range validTokenConfigurations {
		if validConfig == t {
			return nil
		}
	}
	return fmt.Errorf("%w: %d", ErrInvalidTokenConfig, t)
}

func (t tokenConfiguration) BranchFactor() int {
	return t.branchFactor
}

func (t tokenConfiguration) BitsPerToken() int {
	return t.bitsPerToken
}

type Key struct {
	// The number of bits in the key.
	length int
	value  string
}

// ToKey returns [keyBytes] as a new key
func ToKey(keyBytes []byte) Key {
	return toKey(slices.Clone(keyBytes))
}

// toKey returns [keyBytes] as a new key
// Caller must not modify [keyBytes] after this call.
func toKey(keyBytes []byte) Key {
	return Key{
		value:  byteSliceToString(keyBytes),
		length: len(keyBytes) * 8,
	}
}

// hasPartialByte returns true iff the key fits into a non-whole number of bytes
func (k Key) hasPartialByte() bool {
	return k.length%8 > 0
}

// HasPrefix returns true iff [prefix] is a prefix of [k] or equal to it.
func (k Key) HasPrefix(prefix Key) bool {
	// [prefix] must be shorter than [k] to be a prefix.
	if k.length < prefix.length {
		return false
	}

	// The number of tokens in the last byte of [prefix], or zero
	// if [prefix] fits into a whole number of bytes.
	remainderBitCount := prefix.remainderBitCount()
	if remainderBitCount == 0 {
		return strings.HasPrefix(k.value, prefix.value)
	}

	// check that the tokens in the partially filled final byte of [prefix] are
	// equal to the tokens in the final byte of [k].
	remainderBitsMask := byte(0xFF >> remainderBitCount)
	prefixRemainderTokens := prefix.value[len(prefix.value)-1] | remainderBitsMask
	remainderTokens := k.value[len(prefix.value)-1] | remainderBitsMask

	if prefixRemainderTokens != remainderTokens {
		return false
	}

	// Note that this will never be an index OOB because len(prefix.value) > 0.
	// If len(prefix.value) == 0 were true, [remainderTokens] would be 0 so we
	// would have returned above.
	prefixWithoutPartialByte := prefix.value[:len(prefix.value)-1]
	return strings.HasPrefix(k.value, prefixWithoutPartialByte)
}

// HasStrictPrefix returns true iff [prefix] is a prefix of [k]
// but is not equal to it.
func (k Key) HasStrictPrefix(prefix Key) bool {
	return k != prefix && k.HasPrefix(prefix)
}

func (k Key) remainderBitCount() int {
	return k.length % 8
}

func (k Key) Length() int {
	return k.length
}

// Token returns the token at the specified index,
func (k Key) Token(bitIndex int, tokenBitSize int) byte {
	storageByte := k.value[bitIndex/8]
	// Shift the byte right to get the token to the rightmost position.
	storageByte >>= dualBitIndex((bitIndex + tokenBitSize) % 8)
	// Apply a mask to remove any other tokens in the byte.
	return storageByte & (0xFF >> dualBitIndex(tokenBitSize))
}

// Append returns a new Path that equals the current
// Path with [token] appended to the end.
func (k Key) Append(token Token) Key {
	buffer := make([]byte, bytesNeeded(k.length+token.length))
	k.appendIntoBuffer(buffer, token)
	return Key{
		value:  byteSliceToString(buffer),
		length: k.length + token.length,
	}
}

// Greater returns true if current Key is greater than other Key
func (k Key) Greater(other Key) bool {
	return k.value > other.value || (k.value == other.value && k.length > other.length)
}

// Less returns true if current Key is less than other Key
func (k Key) Less(other Key) bool {
	return k.value < other.value || (k.value == other.value && k.length < other.length)
}

func (k Key) AppendExtend(token Token, extensionKey Key) Key {
	appendBytes := bytesNeeded(k.length + token.length)
	totalBitLength := k.length + token.length + extensionKey.length
	buffer := make([]byte, bytesNeeded(totalBitLength))
	k.appendIntoBuffer(buffer[:appendBytes], token)

	result := Key{
		value:  byteSliceToString(buffer),
		length: totalBitLength,
	}

	// the extension path will be shifted based on the number of tokens in the partial byte
	bitsRemainder := (k.length + token.length) % 8

	extensionBuffer := buffer[appendBytes-1:]
	if extensionKey.length == 0 {
		return result
	}

	// If the existing value fits into a whole number of bytes,
	// the extension path can be copied directly into the buffer.
	if bitsRemainder == 0 {
		copy(extensionBuffer[1:], extensionKey.value)
		return result
	}

	// Fill the partial byte with the first [shift] bits of the extension path
	extensionBuffer[0] |= extensionKey.value[0] >> bitsRemainder

	// copy the rest of the extension path bytes into the buffer,
	// shifted byte shift bits
	shiftCopy(extensionBuffer[1:], extensionKey.value, dualBitIndex(bitsRemainder))

	return result
}

func (k Key) appendIntoBuffer(buffer []byte, token Token) {
	copy(buffer, k.value)
	buffer[len(buffer)-1] |= token.value << dualBitIndex((k.length+token.length)%8)
}

// dualBitIndex gets the dual of the bit index
// ex: in a byte, the bit 5 from the right is the same as the bit 3 from the left
func dualBitIndex(shift int) int {
	return (8 - shift) % 8
}

// Treats [src] as a bit array and copies it into [dst] shifted by [shift] bits.
// For example, if [src] is [0b0000_0001, 0b0000_0010] and [shift] is 4,
// we copy [0b0001_0000, 0b0010_0000] into [dst].
// Assumes len(dst) >= len(src)-1.
// If len(dst) == len(src)-1 the last byte of [src] is only partially copied
// (i.e. the rightmost bits are not copied).
func shiftCopy(dst []byte, src string, shift int) {
	i := 0
	dualShift := dualBitIndex(shift)
	for ; i < len(src)-1; i++ {
		dst[i] = src[i]<<shift | src[i+1]>>dualShift
	}

	if i < len(dst) {
		// the last byte only has values from byte i, as there is no byte i+1
		dst[i] = src[i] << shift
	}
}

// Skip returns a new Key that contains the last
// k.length-tokensToSkip tokens of [k].
func (k Key) Skip(bitsToSkip int) Key {
	if k.length <= bitsToSkip {
		return Key{}
	}
	result := Key{
		value:  k.value[bitsToSkip/8:],
		length: k.length - bitsToSkip,
	}

	// if the tokens to skip is a whole number of bytes,
	// the remaining bytes exactly equals the new key.
	if bitsToSkip%8 == 0 {
		return result
	}

	// tokensToSkip does not remove a whole number of bytes.
	// copy the remaining shifted bytes into a new buffer.
	buffer := make([]byte, bytesNeeded(result.length))
	bitsRemovedFromFirstRemainingByte := bitsToSkip % 8
	shiftCopy(buffer, result.value, bitsRemovedFromFirstRemainingByte)

	result.value = byteSliceToString(buffer)
	return result
}

// Take returns a new Key that contains the first tokensToTake tokens of the current Key
func (k Key) Take(bitsToTake int) Key {
	if k.length <= bitsToTake {
		return k
	}

	result := Key{
		length: bitsToTake,
	}

	remainderBits := result.remainderBitCount()
	if remainderBits == 0 {
		result.value = k.value[:bitsToTake/8]
		return result
	}

	// We need to zero out some bits of the last byte so a simple slice will not work
	// Create a new []byte to store the altered value
	buffer := make([]byte, bytesNeeded(bitsToTake))
	copy(buffer, k.value)

	// We want to zero out everything to the right of the last token, which is at index [tokensToTake] - 1
	// Mask will be (8-remainderBits) number of 1's followed by (remainderBits) number of 0's
	buffer[len(buffer)-1] &= byte(0xFF << dualBitIndex(remainderBits))

	result.value = byteSliceToString(buffer)
	return result
}

// Bytes returns the raw bytes of the Key
// Invariant: The returned value must not be modified.
func (k Key) Bytes() []byte {
	// avoid copying during the conversion
	// "safe" because we never edit the value, only used as DB key
	return stringToByteSlice(k.value)
}

// iteratedHasPrefix checks if the provided prefix path is a prefix of the current path after having skipped [skipTokens] tokens first
// this has better performance than constructing the actual path via Skip() then calling HasPrefix because it avoids the []byte allocation
func (k Key) iteratedHasPrefix(prefix Key, bitsToSkip int, tokenBitSize int) bool {
	if k.length-bitsToSkip < prefix.length {
		return false
	}
	for i := 0; i < prefix.length; i += tokenBitSize {
		if k.Token(bitsToSkip+i, tokenBitSize) != prefix.Token(i, tokenBitSize) {
			return false
		}
	}
	return true
}

// byteSliceToString converts the []byte to a string
// Invariant: The input []byte must not be modified.
func byteSliceToString(bs []byte) string {
	// avoid copying during the conversion
	// "safe" because we never edit the []byte, and it is never returned by any functions except Bytes()
	return unsafe.String(unsafe.SliceData(bs), len(bs))
}

// stringToByteSlice converts the string to a []byte
// Invariant: The output []byte must not be modified.
func stringToByteSlice(value string) []byte {
	// avoid copying during the conversion
	// "safe" because we never edit the []byte
	return unsafe.Slice(unsafe.StringData(value), len(value))
}

// Returns the number of bytes needed to store [bits] bits.
func bytesNeeded(bits int) int {
	size := bits / 8
	if bits%8 != 0 {
		size++
	}
	return size
}
