package main

import (
	"fmt"
	"hash/fnv"
	"log"
	"os"
	"path"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
)

var pwdID string

func envID() string {
	return pwdID + ".env"
}

func logID() string {
	return pwdID + ".log"
}

func updateID() string {
	return pwdID + "_update.bin"
}

func makePwdID() (err error) {
	cwd, err := os.Getwd()
	if err != nil {
		log.Println("[ERR]", err)
		return
	}
	realCwd, err := filepath.EvalSymlinks(cwd)
	if err != nil {
		log.Println("[ERR]", err)
		return
	}

	tmp := os.TempDir()
	if err = os.MkdirAll(tmp, 0700); err != nil {
		log.Println("[ERR]", err)
		return
	}
	h := fnv.New64a()
	h.Write([]byte(realCwd))
	id := fmt.Sprintf("%d", h.Sum64())
	prefix := path.Join(tmp, "."+binName+"_"+id)

	slot, err := findNewIDSlot(prefix)
	if err != nil {
		return
	}

	pwdID = prefix + "_" + slot
	return
}

func findNewIDSlot(prefix string) (slot string, err error) {
	prefixPattern := prefix + "_"
	pattern := prefixPattern + strings.Repeat("?", 6) + ".*"
	paths, err := filepath.Glob(pattern)
	if err != nil {
		log.Println("[ERR]", err)
		return
	}

	padder := func(n uint64) string { return fmt.Sprintf("%06d", n) }

	prefixLen := len(prefixPattern)
	nums := []string{padder(0)}
	for _, path := range paths {
		nums = append(nums, path[prefixLen:prefixLen+6])
	}
	sort.Strings(nums)

	biggest := nums[len(nums)-1]
	big, err := strconv.ParseUint(biggest, 10, 32)
	if err != nil {
		log.Println("[ERR]", err)
		return
	}

	slot = padder(big + 1)
	return
}
