package hashutil

import (
	"encoding/hex"
	"fmt"
)

func HashToStringMapKey(hash []byte) string {
	return hex.EncodeToString(hash)
}

func StringMapKeyToHash(str string) ([]byte, error) {
	return hex.DecodeString(str)
}

func StringMapKeyToHashNoError(str string) []byte {
	hash, err := StringMapKeyToHash(str)
	if err != nil {
		panic(fmt.Sprintf("internal error: hash string %v doesn't convert to hash", str))
	}
	return hash
}
