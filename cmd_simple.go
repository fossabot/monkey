package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"hash/fnv"
	"log"
	"os"
	"os/exec"
	"time"
)

type simpleCmd struct {
	V   uint   `json:"v"`
	Cmd string `json:"cmd"`
}

type simpleCmdRep struct {
	Cmd   string  `json:"cmd"`
	V     uint    `json:"v"`
	Us    uint64  `json:"us"`
	Error *string `json:"error"`
}

func (cmd simpleCmd) Kind() string {
	return cmd.Cmd
}

func (cmd simpleCmd) Exec(cfg *ymlCfg) []byte {
	cmdRet := executeScript(cfg, cmd.Kind())
	rep, err := json.Marshal(cmdRet)
	if err != nil {
		log.Fatal(err)
	}
	return rep
}

func executeScript(cfg *ymlCfg, kind string) *simpleCmdRep {
	cmds := cfg.Script[kind]
	if len(cmds) == 0 {
		return &simpleCmdRep{V: 1, Cmd: kind}
	}

	cmdTimeout := 10 * time.Minute
	shell := os.Getenv("SHELL")
	if len(shell) == 0 {
		log.Fatal("$SHELL is unset")
	}
	ctx, cancel := context.WithTimeout(context.Background(), cmdTimeout)
	defer cancel()

	var script, stderr bytes.Buffer
	envSerializedPath := uniquePath()
	fmt.Fprintln(&script, "source", envSerializedPath, ">/dev/null")
	fmt.Fprintln(&script, "set -x")
	fmt.Fprintln(&script, "set -o errexit")
	fmt.Fprintln(&script, "set -o errtrace")
	fmt.Fprintln(&script, "set -o nounset")
	fmt.Fprintln(&script, "set -o pipefail")
	for _, cmd := range cmds {
		fmt.Fprintln(&script, cmd)
	}
	fmt.Fprintln(&script, "declare -p >", envSerializedPath)

	exe := exec.CommandContext(ctx, shell, "--", "/dev/stdin")
	exe.Stdin = &script
	exe.Stdout = os.Stdout
	exe.Stderr = &stderr

	log.Println("$", script.Bytes())
	start := time.Now()
	err := exe.Run()
	us := uint64(time.Since(start) / time.Microsecond)
	if err != nil {
		error := string(stderr.Bytes()) + "\n" + err.Error()
		return &simpleCmdRep{V: 1, Cmd: kind, Us: us, Error: &error}
	}

	return &simpleCmdRep{V: 1, Cmd: kind, Us: us}
}

func uniquePath() string {
	cwd, err := os.Getwd()
	if err != nil {
		log.Fatal(err)
	}

	h := fnv.New64a()
	h.Write([]byte(cwd))
	return "/tmp/" + coveredci + "_" + fmt.Sprintf("%d", h.Sum64()) + ".env"
}

func snapEnv(envSerializedPath string) {
	cmdTimeout := 100 * time.Millisecond
	shell := os.Getenv("SHELL")
	ctx, cancel := context.WithTimeout(context.Background(), cmdTimeout)
	defer cancel()

	cmd := fmt.Sprintf("declare -p >%s", envSerializedPath)
	exe := exec.CommandContext(ctx, shell, "-c", cmd)
	log.Println("$", cmd)

	if err := exe.Run(); err != nil {
		log.Fatal(err)
	}
}