package wcdbapi

import (
	"crypto/aes"
	"crypto/cipher"
	"fmt"
)

const (
	walHeaderSize = 32
	walFrameHdr   = 24
	pageSize      = 4096
	reserveSize   = 80
	saltSize      = 16
)

func decryptWALPage(key []byte, pageData []byte, pageNo uint32) ([]byte, error) {
	// WAL page 1 contains a complete encrypted database page rather than the
	// main-file salt substitution, so it follows the ordinary page branch.
	if pageNo == 1 {
		pageNo = 2
	}
	return decryptPageRaw(key, pageData, int(pageNo))
}

func decryptPageRaw(key []byte, pageData []byte, pageNo int) ([]byte, error) {
	if len(pageData) < pageSize || len(key) != 32 {
		return nil, fmt.Errorf("invalid page or key")
	}
	ivOffset := pageSize - reserveSize
	iv := pageData[ivOffset : ivOffset+aes.BlockSize]
	out := make([]byte, pageSize)

	if pageNo == 1 {
		decoded, err := aesCBCDecryptRaw(key, iv, pageData[saltSize:ivOffset])
		if err != nil {
			return nil, err
		}
		copy(out[:saltSize], []byte("SQLite format 3\x00"))
		copy(out[saltSize:ivOffset], decoded)
		return out, nil
	}
	decoded, err := aesCBCDecryptRaw(key, iv, pageData[:ivOffset])
	if err != nil {
		return nil, err
	}
	copy(out[:ivOffset], decoded)
	return out, nil
}

func aesCBCDecryptRaw(key, iv, data []byte) ([]byte, error) {
	if len(data) == 0 || len(data)%aes.BlockSize != 0 {
		return nil, fmt.Errorf("invalid cipher length: %d", len(data))
	}
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, err
	}
	out := append([]byte(nil), data...)
	cipher.NewCBCDecrypter(block, iv).CryptBlocks(out, out)
	return out, nil
}
