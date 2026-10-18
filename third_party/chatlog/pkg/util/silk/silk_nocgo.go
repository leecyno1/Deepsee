//go:build !cgo

package silk

import "fmt"

func Silk2MP3(data []byte) ([]byte, error) {
	_ = data
	return nil, fmt.Errorf("silk to mp3 requires cgo-enabled build")
}
