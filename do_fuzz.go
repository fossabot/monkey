package main

import (
	"errors"
	"fmt"
	"log"
	"net/http"
	"net/url"
	"os"
	"sync"
	"time"

	"github.com/golang/protobuf/proto"
	"github.com/gorilla/websocket"
)

const (
	v                 = 1
	mimeJSON          = "application/json"
	mimeYAML          = "application/x-yaml"
	headerContentType = "Content-Type"
	headerAccept      = "Accept"
	headerUserAgent   = "User-Agent"
	headerXAPIKey     = "X-Api-Key"
	headerXAuthToken  = "X-Auth-Token"
)

var ws *wsState
var wsMsgUID uint32

type wsState struct {
	req  chan []byte
	rep  chan []byte
	err  chan error
	err2 chan error
	ping chan struct{}
	done chan struct{}
}

func (ws *wsState) call(req action, cfg *UserCfg) (rep action, err error) {
	// NOTE: log in caller
	// FIXME: use buffers?

	wsMsgUID++
	// reqUID := wsMsgUID
	msg := &Msg{UID: wsMsgUID}

	switch req.(type) {
	case *DoFuzz:
		msg.Msg = &Msg_Fuzz{Fuzz: req.(*DoFuzz)}
	case *RepResetProgress:
		msg.Msg = &Msg_ResetProgress{ResetProgress: req.(*RepResetProgress)}
	case *RepCallDone:
		msg.Msg = &Msg_CallDone{CallDone: req.(*RepCallDone)}
	default:
		err = fmt.Errorf("unexpected msg: %#v", req)
	}
	if err != nil {
		return
	}

	payload, err := proto.Marshal(msg)
	if err != nil {
		return
	}

	log.Printf("[DBG] 🡱 sending %dB\n", len(payload))
	ws.req <- payload
rcv:
	start := time.Now()
	select {
	case payload = <-ws.rep:
		log.Println("[DBG] 🡳", time.Now().Sub(start), payload[:4])
		if err = proto.Unmarshal(payload, msg); err != nil {
			return
		}

		// if msg.GetUID() != reqUID {
		// 	err = errors.New("bad dialog sequence number")
		// 	return
		// }

		switch msg.GetMsg().(type) {
		case *Msg_Err500:
			err = errors.New("500 Internal Server Error")
		case *Msg_Err400:
			err = errors.New("400 Bad Request")
		case *Msg_Err401:
			err = errors.New("401 Unauthorized")
		case *Msg_Err403:
			err = errors.New("403 Forbidden")
		case *Msg_DoReset:
			rep = msg.GetDoReset()
		case *Msg_DoCall:
			rep = msg.GetDoCall()
		case *Msg_FuzzProgress:
			rep = msg.GetFuzzProgress()
			rep.exec(cfg)
			if r := rep.(*FuzzProgress); !(r.GetFailure() || r.GetSuccess()) {
				goto rcv
			}
		default:
			err = fmt.Errorf("unexpected msg: %#v", msg)
		}

	case err = <-ws.err:
		log.Println("[DBG] 🡳", time.Now().Sub(start), err)
	case <-time.After(15 * time.Second):
		err = errors.New("ws call timeout")
	}
	return
}

func newFuzz(cfg *UserCfg, vald *validator) (act action, err error) {
	if err = newWS(cfg); err != nil {
		return
	}

	msg := &DoFuzz{
		Cfg:  cfg,
		Spec: vald.Spec,
	}

	if act, err = ws.call(msg, cfg); err != nil {
		log.Println("[ERR]", err)
	}
	return
}

func (act *DoFuzz) exec(cfg *UserCfg) (nxt action, err error) {
	return
}

func (act *RepResetProgress) exec(cfg *UserCfg) (nxt action, err error) {
	return
}

func fuzzNext(cfg *UserCfg, curr action) (nxt action, err error) {
	// Sometimes sets cfg.Runtime.Final* fields
	log.Printf(">>> curr %#v\n", curr)
	if nxt, err = curr.exec(cfg); err != nil {
		ws.err2 <- err
		return
	}
	log.Printf(">>> nxt %#v\n", nxt)
	if nxt == nil {
		return
	}

	if nxt, err = ws.call(nxt, cfg); err != nil {
		log.Println("[ERR]", err)
	}
	return
}

func fuzzOutcome(done *FuzzProgress) int {
	os.Stdout.Write([]byte{'\n'})
	fmt.Printf("Ran %d tests totalling %d requests\n", lastLane.T, totalR)

	if done.GetFailure() {
		d, m := shrinkingFrom.T, lastLane.T-shrinkingFrom.T
		if m != 1 {
			fmt.Printf("A bug was detected after %d tests then shrunk %d times!\n", d, m)
		} else {
			fmt.Printf("A bug was detected after %d tests then shrunk once!\n", d)
		}
		return 6
	}

	if !done.GetSuccess() {
		log.Fatalln("[ERR] there should be success!")
	}
	fmt.Println("No bugs found... yet.")
	return 0
}

func newWS(cfg *UserCfg) error {
	headers := http.Header{
		headerUserAgent:  {binTitle},
		headerXAuthToken: {cfg.AuthToken},
	}
	//FIXME
	u := &url.URL{Scheme: "ws", Host: "localhost:7077", Path: "/1/fuzz"}

	log.Printf("connecting to %s", u.String())
	c, _, err := websocket.DefaultDialer.Dial(u.String(), headers)
	if err != nil {
		log.Println("[ERR]", err)
		return err
	}

	ws = &wsState{
		req:  make(chan []byte, 1),
		rep:  make(chan []byte, 1),
		err:  make(chan error, 1),
		err2: make(chan error, 1),
		ping: make(chan struct{}, 1),
		done: make(chan struct{}, 1),
	}

	go func() {
		defer func() { ws.done <- struct{}{} }()
		for {
			_, rep, err := c.ReadMessage()
			if err != nil {
				log.Println("[ERR]", err)
				ws.err <- err
				return
			}
			log.Printf("recv %dB: %s...\n", len(rep), rep[:4])
			if len(rep) == 4 && string(rep) == `PING` {
				ws.ping <- struct{}{}
			} else {
				ws.rep <- rep
			}
		}
		log.Println("ws reader ending")
	}()

	var once sync.Once
	pong := make(chan struct{}, 1)
	srvTimeout := 15 * time.Second

	go func() {
		defer close(ws.req)
		defer close(ws.rep)
		defer close(ws.err)
		defer close(ws.err2)
		defer close(ws.ping)
		defer close(ws.done)

		func() {
			for {
				select {
				case data := <-ws.req:
					log.Println("ws.req")
					if err := c.WriteMessage(websocket.BinaryMessage, data); err != nil {
						log.Println("[ERR]", err)
						ws.err <- err
						return
					}
				case <-ws.ping:
					log.Println("ws.ping")
					data := []byte(`PONG`)
					if err := c.WriteMessage(websocket.TextMessage, data); err != nil {
						log.Println("[ERR]", err)
						ws.err <- err
						return
					}
					once.Do(func() { pong <- struct{}{} })
				case err := <-ws.err2:
					log.Println("ws.err2")
					data := []byte(err.Error())
					if err := c.WriteMessage(websocket.TextMessage, data); err != nil {
						log.Println("[ERR]", err)
						return
					}
				case <-ws.done:
					log.Println("ws.done")
					return
				case <-time.After(srvTimeout):
					log.Fatalln("[ERR] srvTimeout")
					return
				}
			}
		}()
		log.Println("ws manager ending")
		if err := c.Close(); err != nil {
			log.Println("[ERR]", err)
		}
	}()

	select {
	case <-pong:
		log.Println("pong!")
		close(pong)
		return nil
	case err := <-ws.err:
		log.Println("err!")
		return err
	case <-time.After(srvTimeout):
		err := errors.New("timeout waiting for PING from server")
		log.Println("[ERR]", err)
		return err
	}
}
