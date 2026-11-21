package agent

import (
	"crypto/des"
	"crypto/rand"
	"math/big"
	"math/bits"
	"strings"
)

// VNC passwords that the agent makes itself (D-64). Each time the agent starts the
// VNC server it gives it a new random password, and the server hands that password
// only to a person allowed to open the screen. Without one, any program on the host
// could connect to 127.0.0.1:5900 and control the desktop, the Windows login and
// UAC prompts included, whenever VNC is on.
//
// VNC servers keep their password in one old, fixed format (TightVNC in the registry,
// x11vnc in a file): at most 8 characters, encrypted with DES under a key that every
// VNC program knows. That is not secrecy, it only keeps the text from being read at a
// glance, so what protects the password is who may read where it is kept.

// vncPasswordLen is the most VNC authentication uses: it ignores anything after 8.
const vncPasswordLen = 8

// vncFixedKey is the DES key of the VNC password format. The VNC code that defined it
// reverses the bits of each key byte before using it, so the key Go's DES needs is
// this one with every byte reversed (vncDESKey).
var vncFixedKey = [8]byte{23, 82, 107, 6, 35, 78, 88, 7}

func vncDESKey() []byte {
	key := make([]byte, len(vncFixedKey))
	for i, b := range vncFixedKey {
		key[i] = bits.Reverse8(b)
	}
	return key
}

// newVNCPassword returns vncPasswordLen random letters and digits: 62^8, about 2^47,
// which is plenty for a password that only a program on this machine could try, and
// that changes every time the screen is turned on.
func newVNCPassword() (string, error) {
	const letters = "abcdefghijklmnopqrstuvwxyzABCDEFGHIJKLMNOPQRSTUVWXYZ0123456789"
	var b strings.Builder
	for i := 0; i < vncPasswordLen; i++ {
		n, err := rand.Int(rand.Reader, big.NewInt(int64(len(letters))))
		if err != nil {
			return "", err
		}
		b.WriteByte(letters[n.Int64()])
	}
	return b.String(), nil
}

// vncEncryptPassword turns a password into the 8 bytes VNC servers store.
func vncEncryptPassword(password string) []byte {
	plain := make([]byte, vncPasswordLen) // padded with zero bytes, cut at 8
	copy(plain, password)
	block, _ := des.NewCipher(vncDESKey()) // cannot fail: the key is 8 bytes
	out := make([]byte, vncPasswordLen)
	block.Encrypt(out, plain)
	return out
}

// vncDecryptPassword is the reverse. Anything that is not 8 bytes gives "".
func vncDecryptPassword(stored []byte) string {
	if len(stored) < vncPasswordLen {
		return ""
	}
	block, _ := des.NewCipher(vncDESKey())
	plain := make([]byte, vncPasswordLen)
	block.Decrypt(plain, stored[:vncPasswordLen])
	return strings.TrimRight(string(plain), "\x00")
}
