package hashutil

import (
	"encoding/hex"
	"fmt"
)

func HashToStringMapKey(hash []byte) string {
	return hex.EncodeToString(hash)
}

func HashToSlashedStringMapKey(hash []byte) string {
	hashString := HashToStringMapKey(hash)
	slashed := ""
	for _, c := range hashString {
		slashed += string(c) + "/"
	}
	return slashed[:len(slashed)-1]
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

func SlashedStringMapKeyToHashNoError(str string) []byte {
	if len(str)%2 != 1 {
		panic(fmt.Sprintf("internal error: slashed hash string %v doesn't convert to hash. It needs to have an un-even length. Actual length: %v", str, len(str)))
	}
	unslashed := str[0:1]
	for i := 1; i < len(str)-1; i += 2 {
		if str[i] != '/' {
			panic(fmt.Sprintf("internal error: slashed hash string %v doesn't convert to hash. Its missing a / at position %v", str, i))
		}
		unslashed += str[i+1 : i+2]
	}
	return StringMapKeyToHashNoError(unslashed)
}
